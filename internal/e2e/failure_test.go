package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeRsync writes a wrapper around the real rsync that misbehaves for
// uploads ("upload") or downloads ("download").
func fakeRsync(t *testing.T, mode string) string {
	t.Helper()
	real, err := exec.LookPath("rsync")
	if err != nil {
		t.Fatal(err)
	}
	var body string
	switch mode {
	case "upload":
		body = `for a in "$@"; do case "$a" in --exclude-from=*) echo "rsync: simulated network failure" >&2; exit 12;; esac; done
exec ` + real + ` "$@"`
	case "download":
		// Transfer, then corrupt what arrived and report an interruption.
		body = `dl=0; for a in "$@"; do case "$a" in --files-from=*) dl=1;; esac; done
if [ $dl = 0 ]; then exec ` + real + ` "$@"; fi
` + real + ` "$@"
for last in "$@"; do :; done
find "$last" -type f -exec sh -c 'printf X >> "$1"' _ {} \;
echo "rsync: connection unexpectedly closed" >&2
exit 12`
	}
	p := filepath.Join(t.TempDir(), "rsync")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFailedUploadPreventsExecution(t *testing.T) {
	dir := newProject(t, "upfail")
	mustRun(t, dir, "true")
	write(t, dir, "new.py", "print(1)")
	r := runEnv(t, dir, []string{"RECOM_RSYNC=" + fakeRsync(t, "upload")}, "", "touch", "ran.txt")
	if r.code != 125 || !strings.Contains(r.stderr, "command was not run") {
		t.Fatalf("upload failure: %s", r)
	}
	if exists(filepath.Join(replicaPath(t, dir), "ran.txt")) {
		t.Fatal("command ran despite failed upload")
	}
}

func TestInterruptedDownloadStaysPendingAndResumes(t *testing.T) {
	dir := newProject(t, "dlfail")
	r := runEnv(t, dir, []string{"RECOM_RSYNC=" + fakeRsync(t, "download")}, "", "--shell", "echo result > result.txt")
	if r.code != 75 || !strings.Contains(r.stderr, "could not collect result.txt") {
		t.Fatalf("interrupted download: %s", r)
	}
	if exists(filepath.Join(dir, "result.txt")) {
		t.Fatal("a partial/corrupt download was installed")
	}
	st := status(t, dir)
	if len(st.Remote.Pending) != 1 || st.Remote.Pending[0].Path != "result.txt" {
		t.Fatalf("output not pending: %+v", st.Remote.Pending)
	}
	r = mustRun(t, dir, "true")
	if b, _ := os.ReadFile(filepath.Join(dir, "result.txt")); string(b) != "result\n" {
		t.Fatalf("pending output not recovered: %q\n%s", b, r)
	}
}

func TestClientDisconnectKeepsOutputs(t *testing.T) {
	dir := newProject(t, "disconnect")
	mustRun(t, dir, "true")
	rp := replicaPath(t, dir)
	cmd := exec.Command(env.recom, "--shell", "echo early > early.txt; sleep 60")
	cmd.Dir = dir
	cmd.Env = env.baseEnv
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100 && !exists(filepath.Join(rp, "early.txt")); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	// Laptop "drops": kill recom and its ssh client abruptly.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Wait()
	var r result
	for i := 0; i < 50; i++ { // the helper needs a moment to notice and finalize
		r = run(t, dir, "true")
		if exists(filepath.Join(dir, "early.txt")) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !exists(filepath.Join(dir, "early.txt")) {
		t.Fatalf("output written before the disconnect was lost: %s", r)
	}
}

func TestCorruptManifestFailsClosed(t *testing.T) {
	dir := newProject(t, "corrupt")
	mustRun(t, dir, "true")
	st := status(t, dir)
	runs := filepath.Join(filepath.Dir(filepath.Dir(st.Remote.ReplicaPath)), "state", st.Replica, "runs")
	os.WriteFile(filepath.Join(runs, "261031deadbeef00.json"), []byte(`{"version":1,"run_id":"261031deadbeef00","outputs":[{"path":"../../etc/passwd","status":"pending"`), 0o600)
	r := mustRun(t, dir, "status")
	if !strings.Contains(r.stderr, "corrupt state file") {
		t.Fatalf("corrupt manifest not reported: %s", r)
	}
	r = mustRun(t, dir, "echo", "still works")
	if !strings.Contains(r.stdout, "still works") {
		t.Fatal(r)
	}
}

func TestHelperIsReinstalledAndVerified(t *testing.T) {
	dir := newProject(t, "helper")
	mustRun(t, dir, "true")
	bins, _ := filepath.Glob(filepath.Join(env.dir, "remote", "bin", "recom-helper-*"))
	if len(bins) != 1 {
		t.Fatalf("helpers: %v", bins)
	}
	replace := func(content []byte) {
		tmp := bins[0] + ".test"
		if err := os.WriteFile(tmp, content, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, bins[0]); err != nil {
			t.Fatal(err)
		}
	}
	// A damaged helper is replaced automatically.
	replace([]byte("garbage"))
	r := mustRun(t, dir, "-v", "echo", "ok")
	if !strings.Contains(r.stderr, "installing helper") || !strings.Contains(r.stdout, "ok") {
		t.Fatalf("damaged helper not reinstalled: %s", r)
	}
	// A different (outdated or tampered) binary under the expected name is
	// detected by its hash, never trusted, and replaced.
	other := filepath.Join(t.TempDir(), "old-helper")
	build := exec.Command("go", "build", "-o", other, "-ldflags", "-X github.com/chaseungjoon/recom/internal/buildinfo.Version=old", "./cmd/recom-helper")
	build.Dir = repoRoot()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	b, _ := os.ReadFile(other)
	replace(b)
	r = runEnv(t, dir, nil, "", "echo", "fine")
	if r.code != 0 || !strings.Contains(r.stderr, "failed verification") || !strings.Contains(r.stdout, "fine") {
		t.Fatalf("mismatched helper not replaced: %s", r)
	}
	if now, _ := os.ReadFile(bins[0]); string(now) == string(b) {
		t.Fatal("tampered helper left in place")
	}
}
