package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func TestIncrementalUploadSendsOnlyChanges(t *testing.T) {
	dir := newProject(t, "inc")
	for i := 0; i < 20; i++ {
		write(t, dir, filepath.Join("pkg", "m"+string(rune('a'+i))+".py"), "x = 1\n")
	}
	r := mustRun(t, dir, "true")
	if !strings.Contains(r.stderr, "synced 21 changed file(s)") { // 20 + .recom.toml
		t.Fatalf("first upload: %s", r)
	}
	write(t, dir, "pkg/mc.py", "x = 2\n")
	r = mustRun(t, dir, "true")
	if !strings.Contains(r.stderr, "synced 1 changed file(s)") {
		t.Fatalf("second upload should send one file: %s", r)
	}
	r = mustRun(t, dir, "true")
	if strings.Contains(r.stderr, "synced") {
		t.Fatalf("third upload should send nothing: %s", r)
	}
}

func TestUploadedInputIsNotAnOutput(t *testing.T) {
	dir := newProject(t, "inputs")
	write(t, dir, "data/input.csv", "a,b\n1,2\n")
	r := mustRun(t, dir, "python3", "-c", "print(open('data/input.csv').read())")
	if strings.Contains(r.stderr, "←") {
		t.Fatalf("uploaded input was treated as an output: %s", r)
	}
	write(t, dir, "data/input.csv", "a,b\n3,4\n") // changed input, uploaded again
	r = mustRun(t, dir, "cat", "data/input.csv")
	if strings.Contains(r.stderr, "←") || !strings.Contains(r.stdout, "3,4") {
		t.Fatalf("re-uploaded input mishandled: %s", r)
	}
}

func TestOutputRules(t *testing.T) {
	dir := newProject(t, "rules")
	// Run 1 creates an output.
	mustRun(t, dir, "--shell", "mkdir -p out && echo one > out/a.txt")
	if b, _ := os.ReadFile(filepath.Join(dir, "out/a.txt")); string(b) != "one\n" {
		t.Fatalf("output not collected: %q", b)
	}
	// Run 2 does not touch it: deleting the local copy must not bring it back.
	os.Remove(filepath.Join(dir, "out/a.txt"))
	r := mustRun(t, dir, "true")
	if exists(filepath.Join(dir, "out/a.txt")) || strings.Contains(r.stderr, "←") {
		t.Fatalf("unchanged remote artifact was downloaded: %s", r)
	}
	// Touch only: not an output.
	r = mustRun(t, dir, "touch", "out/a.txt")
	if strings.Contains(r.stderr, "←") {
		t.Fatalf("touched-only file downloaded: %s", r)
	}
	// Modified with the original mtime restored: still detected (ctime/hash).
	r = mustRun(t, dir, "python3", "-c", `
import os
st = os.stat("out/a.txt")
open("out/a.txt", "w").write("two\n")
os.utime("out/a.txt", ns=(st.st_atime_ns, st.st_mtime_ns))
`)
	if b, _ := os.ReadFile(filepath.Join(dir, "out/a.txt")); string(b) != "two\n" {
		t.Fatalf("mtime-preserving modification missed: %q\n%s", b, r)
	}
}

func TestRemoteModifiedInputIsCollected(t *testing.T) {
	dir := newProject(t, "modify")
	write(t, dir, "log.txt", "start\n")
	mustRun(t, dir, "--shell", "echo appended >> log.txt")
	if b, _ := os.ReadFile(filepath.Join(dir, "log.txt")); string(b) != "start\nappended\n" {
		t.Fatalf("remote modification of an unchanged local file not collected: %q", b)
	}
}

func TestLocalEditDuringRunIsNeverOverwritten(t *testing.T) {
	dir := newProject(t, "conflict")
	write(t, dir, "notes.txt", "original\n")
	done := make(chan result)
	go func() {
		done <- run(t, dir, "--shell", "sleep 2; echo remote > notes.txt; echo new > fresh.txt")
	}()
	time.Sleep(1 * time.Second)
	write(t, dir, "notes.txt", "my local edit\n")
	write(t, dir, "fresh.txt", "created locally meanwhile\n")
	r := <-done
	if r.code != 0 {
		t.Fatal(r)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "notes.txt")); string(b) != "my local edit\n" {
		t.Fatalf("local edit overwritten: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "fresh.txt")); string(b) != "created locally meanwhile\n" {
		t.Fatalf("locally created file overwritten: %q", b)
	}
	if !strings.Contains(r.stderr, "conflict: notes.txt") || !strings.Contains(r.stderr, "conflict: fresh.txt") {
		t.Fatalf("conflicts not reported: %s", r)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, ".recom/conflicts/*/notes.txt"))
	if len(matches) != 1 {
		t.Fatalf("remote version not staged: %v", matches)
	}
	if b, _ := os.ReadFile(matches[0]); string(b) != "remote\n" {
		t.Fatalf("staged conflict content: %q", b)
	}
	// The next run uploads the local version (local wins after a conflict).
	r = mustRun(t, dir, "cat", "notes.txt")
	if !strings.Contains(r.stdout, "my local edit") {
		t.Fatalf("local version not uploaded after conflict: %s", r)
	}
}

func TestFailedCommandStillCollectsOutputs(t *testing.T) {
	dir := newProject(t, "fail")
	r := run(t, dir, "--shell", "echo partial > crash.log; exit 3")
	if r.code != 3 {
		t.Fatalf("exit code: %s", r)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "crash.log")); string(b) != "partial\n" {
		t.Fatalf("output of failed run not collected: %q", b)
	}
	r = run(t, dir, "--shell", "kill -TERM $$")
	if r.code != 143 {
		t.Fatalf("signal exit code: %s", r)
	}
}

func TestCompiledBinaryAndBinaryData(t *testing.T) {
	dir := newProject(t, "cc")
	write(t, dir, "src/main.c", "#include <stdio.h>\nint main(){puts(\"compiled remotely\");return 0;}\n")
	mustRun(t, dir, "--shell", "mkdir -p bin && gcc src/main.c -O2 -o bin/main")
	fi, err := os.Stat(filepath.Join(dir, "bin/main"))
	if err != nil || fi.Mode()&0o100 == 0 {
		t.Fatalf("compiled binary not collected executable: %v %v", fi, err)
	}
	r := mustRun(t, dir, "./bin/main")
	if !strings.Contains(r.stdout, "compiled remotely") {
		t.Fatal(r)
	}
	mustRun(t, dir, "python3", "-c", "import os; open('img.bin','wb').write(bytes(range(256))*100)")
	b, _ := os.ReadFile(filepath.Join(dir, "img.bin"))
	if len(b) != 25600 || b[255] != 255 {
		t.Fatalf("binary output corrupted: %d bytes", len(b))
	}
}

func TestLargeOutputsAreHeldAndPullable(t *testing.T) {
	dir := newProject(t, "large")
	cfg := readFile(filepath.Join(dir, ".recom.toml"))
	cfg = strings.Replace(cfg, `max_file_size = "100MB"`, `max_file_size = "1KB"`, 1)
	write(t, dir, ".recom.toml", cfg)
	write(t, dir, "ckpt.bin", "v1")
	r := mustRun(t, dir, "python3", "-c", "open('ckpt.bin','w').write('v2'*2000); open('small.txt','w').write('s')")
	if !strings.Contains(r.stderr, "held") || !strings.Contains(r.stderr, "recom pull ckpt.bin") {
		t.Fatalf("held output not reported: %s", r)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "ckpt.bin")); string(b) != "v1" {
		t.Fatalf("held output downloaded anyway: %d bytes", len(b))
	}
	if !exists(filepath.Join(dir, "small.txt")) {
		t.Fatal("small output not collected")
	}
	// The stale local v1 must not overwrite the newer remote file on upload.
	rp := replicaPath(t, dir)
	mustRun(t, dir, "true")
	if b, _ := os.ReadFile(filepath.Join(rp, "ckpt.bin")); len(b) != 4000 {
		t.Fatalf("upload clobbered a held remote output: %d bytes", len(b))
	}
	st := status(t, dir)
	if len(st.Remote.Pending) != 1 || st.Remote.Pending[0].Status != "held" {
		t.Fatalf("status pending: %+v", st.Remote.Pending)
	}
	// Explicit pull: the local file is exactly what the run started from.
	r = mustRun(t, dir, "pull", "ckpt.bin")
	if b, _ := os.ReadFile(filepath.Join(dir, "ckpt.bin")); len(b) != 4000 {
		t.Fatalf("pull did not fetch: %s", r)
	}
	if st := status(t, dir); len(st.Remote.Pending) != 0 {
		t.Fatalf("still pending after pull: %+v", st.Remote.Pending)
	}
}

func TestNoDeletionPropagation(t *testing.T) {
	dir := newProject(t, "nodelete")
	write(t, dir, "keep.txt", "k")
	mustRun(t, dir, "true")
	rp := replicaPath(t, dir)
	os.Remove(filepath.Join(dir, "keep.txt"))
	mustRun(t, dir, "true")
	if !exists(filepath.Join(rp, "keep.txt")) {
		t.Fatal("local deletion removed the remote file")
	}
	write(t, dir, "local.txt", "l")
	mustRun(t, dir, "rm", "local.txt")
	if !exists(filepath.Join(dir, "local.txt")) {
		t.Fatal("remote deletion removed the local file")
	}
}

func TestSecretsAndDefaultExcludesStayLocal(t *testing.T) {
	dir := newProject(t, "secrets")
	for _, f := range []string{".env", ".env.local", "id_rsa", "server.pem", ".aws/credentials", ".venv/bin/python", "__pycache__/x.pyc", ".git/config", "node_modules/m/index.js"} {
		write(t, dir, f, "secret")
	}
	write(t, dir, ".env.example", "TEMPLATE=1")
	write(t, dir, "main.py", "")
	mustRun(t, dir, "true")
	rp := replicaPath(t, dir)
	for _, f := range []string{".env", ".env.local", "id_rsa", "server.pem", ".aws/credentials", ".venv", "__pycache__", ".git", "node_modules", ".recom"} {
		if exists(filepath.Join(rp, f)) {
			t.Errorf("%s was uploaded", f)
		}
	}
	for _, f := range []string{".env.example", "main.py", ".recom.toml"} {
		if !exists(filepath.Join(rp, f)) {
			t.Errorf("%s was not uploaded", f)
		}
	}
	// A remotely generated secret-looking file is not downloaded either.
	mustRun(t, dir, "--shell", "echo TOKEN=x > generated.env; echo x > .env.prod")
	if exists(filepath.Join(dir, ".env.prod")) {
		t.Error("secret-looking output was downloaded")
	}
}

func TestSymlinkSafety(t *testing.T) {
	dir := newProject(t, "links")
	write(t, dir, "real.txt", "r")
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", filepath.Join(dir, "inside")); err != nil {
		t.Fatal(err)
	}
	mustRun(t, dir, "true")
	rp := replicaPath(t, dir)
	if exists(filepath.Join(rp, "escape")) {
		t.Error("symlink escaping the project was uploaded")
	}
	if l, err := os.Readlink(filepath.Join(rp, "inside")); err != nil || l != "real.txt" {
		t.Errorf("safe relative symlink not uploaded as a link: %q %v", l, err)
	}
	r := mustRun(t, dir, "--shell", "ln -s /etc/hostname remote-escape; ln -s real.txt remote-inside")
	if exists(filepath.Join(dir, "remote-escape")) {
		t.Errorf("escaping symlink output was installed: %s", r)
	}
	if l, err := os.Readlink(filepath.Join(dir, "remote-inside")); err != nil || l != "real.txt" {
		t.Errorf("safe symlink output not installed: %q %v\n%s", l, err, r)
	}
}

func TestUnusualFilenames(t *testing.T) {
	dir := newProject(t, "names")
	names := []string{"with space.txt", "ünïcödé.txt", "-leading-dash.txt", "quote'n\"dq.txt", "$(touch pwned).txt", "semi;colon.txt", "star*.txt"}
	for _, n := range names {
		write(t, dir, "in/"+n, n)
	}
	mustRun(t, dir, "true")
	rp := replicaPath(t, dir)
	for _, n := range names {
		if b, _ := os.ReadFile(filepath.Join(rp, "in", n)); string(b) != n {
			t.Errorf("upload of %q failed", n)
		}
	}
	gen := `import os
os.makedirs("out", exist_ok=True)
for n in ["a b.txt", "ü.txt", "-x.txt", "new\nline.txt", "$(touch pwned2)", "back\\slash.txt", "[br].txt"]:
    open(os.path.join("out", n), "w").write(n)
`
	r := mustRun(t, dir, "python3", "-c", gen)
	for _, n := range []string{"a b.txt", "ü.txt", "-x.txt", "new\nline.txt", "$(touch pwned2)", "back\\slash.txt", "[br].txt"} {
		if b, _ := os.ReadFile(filepath.Join(dir, "out", n)); string(b) != n {
			t.Errorf("download of %q failed\n%s", n, r)
		}
	}
	for _, p := range []string{filepath.Join(dir, "pwned"), filepath.Join(rp, "pwned"), filepath.Join(dir, "pwned2")} {
		if exists(p) {
			t.Errorf("shell injection via filename: %s exists", p)
		}
	}
}
