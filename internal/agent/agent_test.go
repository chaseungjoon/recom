package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chaseungjoon/recom/internal/protocol"
)

func mustWrite(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func outputsByPath(res diffResult) map[string]protocol.Output {
	m := map[string]protocol.Output{}
	for _, o := range res.Outputs {
		m[o.Path] = o
	}
	return m
}

func TestDiffSnapshots(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, root, "src/run.py", "print(1)")
	mustWrite(t, root, "touched.txt", "same")
	mustWrite(t, root, "preserved.txt", "aaaa")
	mustWrite(t, root, "gone.txt", "bye")
	mustWrite(t, root, "__pycache__/x.pyc", "c")
	pol := protocol.OutputPolicy{MaxFileSize: 1 << 20, MaxRunSize: 1 << 30, HashLimit: 1 << 20, SkipSecrets: true, Include: nil}
	f, err := newFilters(pol)
	if err != nil {
		t.Fatal(err)
	}
	cache := hashCache{}
	// Backdate everything so the racy-timestamp window does not apply.
	old := time.Now().Add(-time.Hour)
	filepath.Walk(root, func(p string, _ os.FileInfo, _ error) error { return os.Chtimes(p, old, old) })
	pre, err := takeSnapshot(root, f, cache, pol.HashLimit)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pre.Entries["__pycache__/x.pyc"]; ok {
		t.Fatal("excluded directory was snapshotted")
	}
	// Run effects.
	mustWrite(t, root, "outputs/new.json", "{}")
	now := time.Now()
	os.Chtimes(filepath.Join(root, "touched.txt"), now, now)
	st, _ := os.Stat(filepath.Join(root, "preserved.txt"))
	mustWrite(t, root, "preserved.txt", "bbbb")
	os.Chtimes(filepath.Join(root, "preserved.txt"), st.ModTime(), st.ModTime())
	os.Remove(filepath.Join(root, "gone.txt"))
	mustWrite(t, root, "__pycache__/y.pyc", "c")
	mustWrite(t, root, ".env", "SECRET=1")
	os.Symlink("/etc/passwd", filepath.Join(root, "bad-link"))
	os.Symlink("src/run.py", filepath.Join(root, "good-link"))

	post, err := takeSnapshot(root, f, cache, 0)
	if err != nil {
		t.Fatal(err)
	}
	res := diffSnapshots(root, "r1", pre, post, f, pol, cache)
	got := outputsByPath(res)
	if o, ok := got["outputs/new.json"]; !ok || o.PreExisted || o.Status != protocol.StatusPending || o.SHA256 == "" {
		t.Errorf("new file: %+v", o)
	}
	if _, ok := got["touched.txt"]; ok {
		t.Error("touched-only file reported as output")
	}
	if o, ok := got["preserved.txt"]; !ok || !o.PreExisted || o.PreSHA256 == "" || o.PreSHA256 == o.SHA256 {
		t.Errorf("mtime-preserving modification: %+v", o)
	}
	if _, ok := got["src/run.py"]; ok {
		t.Error("unchanged input reported as output")
	}
	if _, ok := got["__pycache__/y.pyc"]; ok {
		t.Error("excluded path reported")
	}
	if _, ok := got[".env"]; ok {
		t.Error("secret reported as output")
	}
	if o := got["bad-link"]; o.Status != protocol.StatusSkipped {
		t.Errorf("escaping symlink: %+v", o)
	}
	if o := got["good-link"]; o.Status != protocol.StatusPending || o.Type != "symlink" {
		t.Errorf("safe symlink: %+v", o)
	}
	if res.Deleted != 1 {
		t.Errorf("deleted = %d", res.Deleted)
	}
}

func TestDiffSizeLimitsAndInclude(t *testing.T) {
	root := t.TempDir()
	pol := protocol.OutputPolicy{MaxFileSize: 10, MaxRunSize: 15, HashLimit: 1 << 20, Include: []string{"*.txt"}}
	f, _ := newFilters(pol)
	pre, _ := takeSnapshot(root, f, hashCache{}, pol.HashLimit)
	mustWrite(t, root, "a.txt", "12345678")      // 8 bytes: ok
	mustWrite(t, root, "b.txt", "12345678")      // run total 16 > 15: held
	mustWrite(t, root, "big.txt", "12345678901") // 11 > 10: held
	mustWrite(t, root, "c.log", "x")             // not included: held
	post, _ := takeSnapshot(root, f, hashCache{}, 0)
	got := outputsByPath(diffSnapshots(root, "r", pre, post, f, pol, hashCache{}))
	want := map[string]string{"a.txt": "pending", "b.txt": "held", "big.txt": "held", "c.log": "held"}
	for p, st := range want {
		if got[p].Status != st {
			t.Errorf("%s: status %q want %q (%s)", p, got[p].Status, st, got[p].Reason)
		}
	}
	if got["big.txt"].SHA256 != "" {
		t.Error("held file over the limit should not be hashed")
	}
}

func TestSafeLink(t *testing.T) {
	cases := map[[2]string]bool{
		{"a", "b"}:            true,
		{"d/a", "../b"}:       true,
		{"a", "../b"}:         false,
		{"a", "/etc/passwd"}:  false,
		{"d/e/a", "../../x"}:  true,
		{"d/e/a", "../../.."}: false,
		{"a", ""}:             false,
	}
	for c, want := range cases {
		if got := safeLink(c[0], c[1]); got != want {
			t.Errorf("safeLink(%q,%q)=%v", c[0], c[1], got)
		}
	}
}

func TestCleanRelAndRoot(t *testing.T) {
	for _, bad := range []string{"", "/abs", "../x", "a/../../x", "a/..", "a\x00b"} {
		if _, err := CleanRel(bad); err == nil {
			t.Errorf("CleanRel(%q) accepted", bad)
		}
	}
	for in, want := range map[string]string{"a/b": "a/b", "./a": "a", ".": ".", "a//b/": "a/b"} {
		if got, err := CleanRel(in); err != nil || got != want {
			t.Errorf("CleanRel(%q)=%q,%v", in, got, err)
		}
	}
	for _, bad := range []string{"/", "rel/path", "/a/../b", "/a b", "/a'b", "/a/./b", "/home/x/"} {
		if ValidateRoot(bad) == nil {
			t.Errorf("ValidateRoot(%q) accepted", bad)
		}
	}
	if err := ValidateRoot("/home/me/.local/share/recom"); err != nil {
		t.Error(err)
	}
}

func TestLease(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	me := selfLease("run", "aaaaaaaa", "me", "cmd")
	if _, err := acquireLease(ctx, dir, me, false, nil); err != nil {
		t.Fatal(err)
	}
	other := selfLease("run", "bbbbbbbb", "other", "cmd")
	if _, err := acquireLease(ctx, dir, other, false, nil); err == nil {
		t.Fatal("second holder acquired a held lease")
	}
	// A waiting acquirer gets it once released.
	done := make(chan error)
	waited := false
	go func() {
		_, err := acquireLease(ctx, dir, other, true, func(*protocol.LeaseInfo) { waited = true })
		done <- err
	}()
	time.Sleep(200 * time.Millisecond)
	if err := releaseLease(dir, "aaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil || !waited {
		t.Fatalf("waiting acquire: %v waited=%v", err, waited)
	}
	// A dead holder is stale.
	dead := protocol.LeaseInfo{Kind: "run", ID: "cccccccc", PID: 1 << 22, Start: 1, Since: time.Now()}
	writeJSONAtomic(leasePath(dir), dead)
	if _, err := acquireLease(ctx, dir, me, false, nil); err != nil {
		t.Fatalf("stale lease not taken over: %v", err)
	}
	// A job never makes others wait.
	job := selfLease("job", "dddddddd", "", "train")
	writeJSONAtomic(leasePath(dir), job)
	if _, err := acquireLease(ctx, dir, other, true, nil); err == nil {
		t.Fatal("waited on a job lease")
	}
	// Corrupt lease fails closed.
	os.WriteFile(leasePath(dir), []byte("{"), 0o600)
	if _, err := acquireLease(ctx, dir, other, false, nil); err == nil {
		t.Fatal("corrupt lease treated as free")
	}
}

func TestManifestSupersedeAndMark(t *testing.T) {
	l := Layout{Root: t.TempDir()}
	rep := "aaaaaaaa-bbbb"
	if err := l.EnsureState(rep); err != nil {
		t.Fatal(err)
	}
	m1 := &Manifest{RunID: "r1aaaaaa", ReplicaID: rep, Started: time.Now().Add(-time.Minute), State: "exited",
		Outputs: []protocol.Output{{RunID: "r1aaaaaa", Path: "a", Status: protocol.StatusHeld}, {RunID: "r1aaaaaa", Path: "b", Status: protocol.StatusPending}}}
	if err := recordOutputs(l, m1); err != nil {
		t.Fatal(err)
	}
	m2 := &Manifest{RunID: "r2aaaaaa", ReplicaID: rep, Started: time.Now(), State: "exited",
		Outputs: []protocol.Output{{RunID: "r2aaaaaa", Path: "a", Status: protocol.StatusPending}}}
	if err := recordOutputs(l, m2); err != nil {
		t.Fatal(err)
	}
	pend, _ := pendingOutputs(l, rep)
	if len(pend) != 2 || pend[0].Path != "a" || pend[0].RunID != "r2aaaaaa" {
		t.Fatalf("pending after supersede: %+v", pend)
	}
	if err := markOutputs(l, rep, []protocol.Mark{{RunID: "r1aaaaaa", Path: "b", Status: protocol.StatusCollected}}); err != nil {
		t.Fatal(err)
	}
	if err := markOutputs(l, rep, []protocol.Mark{{RunID: "r1aaaaaa", Path: "b", Status: "bogus"}}); err == nil {
		t.Fatal("invalid status accepted")
	}
	pend, _ = pendingOutputs(l, rep)
	if len(pend) != 1 {
		t.Fatalf("pending after mark: %+v", pend)
	}
	// A corrupt manifest is reported, not used.
	os.WriteFile(filepath.Join(l.Runs(rep), "r3aaaaaa.json"), []byte(`{"outputs":[{"path":"x","status":"pending"`), 0o600)
	pend, warns := pendingOutputs(l, rep)
	if len(pend) != 1 || len(warns) != 1 {
		t.Fatalf("corrupt manifest handling: %+v %v", pend, warns)
	}
}

func TestLookPath(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "tool")
	os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755)
	os.WriteFile(filepath.Join(dir, "noexec"), []byte(""), 0o644)
	if p, err := lookPath("tool", dir, "/"); err != nil || p != exe {
		t.Fatalf("lookPath: %q %v", p, err)
	}
	if _, err := lookPath("missing", dir, "/"); err == nil || err.(*lookupError).code != 127 {
		t.Fatalf("missing: %v", err)
	}
	if _, err := lookPath("./noexec", "", dir); err == nil || err.(*lookupError).code != 126 {
		t.Fatalf("not executable: %v", err)
	}
}

func TestTailOffset(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log")
	os.WriteFile(p, []byte("1\n2\n3\n4\n"), 0o600)
	b, _ := os.ReadFile(p)
	if got := string(b[tailOffset(p, 2):]); got != "3\n4\n" {
		t.Fatalf("tail 2 = %q", got)
	}
	if tailOffset(p, 10) != 0 {
		t.Fatal("tail beyond start")
	}
}

func TestAppendFallbackPicksNewestVersion(t *testing.T) {
	home := t.TempDir()
	for _, d := range []string{".nvm/versions/node/v9.11.2/bin", ".nvm/versions/node/v18.20.4/bin", ".nvm/versions/node/v22.1.0/bin", ".cargo/bin"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := appendFallback("/usr/bin:"+filepath.Join(home, ".cargo/bin"), []string{"~/.volta/bin", "~/.nvm/versions/node/*/bin", "~/.cargo/bin", "relative/bin"}, home)
	want := "/usr/bin:" + filepath.Join(home, ".cargo/bin") + ":" + filepath.Join(home, ".nvm/versions/node/v22.1.0/bin")
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestSetupStampsRequirePath(t *testing.T) {
	replica := t.TempDir()
	dir := filepath.Join(replica, ".recom", "setup")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "python"), []byte("fp1\n.venv/bin/python\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "go"), []byte("fp2\n\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "evil"), []byte("fp3\n../../etc/passwd\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "python.tmp"), []byte("x\n"), 0o644)
	got := setupStamps(replica)
	if len(got) != 1 || got["go"] != "fp2" {
		t.Fatalf("stamps without .venv: %v", got)
	}
	os.MkdirAll(filepath.Join(replica, ".venv", "bin"), 0o755)
	os.WriteFile(filepath.Join(replica, ".venv", "bin", "python"), nil, 0o755)
	if got := setupStamps(replica); got["python"] != "fp1" || len(got) != 2 {
		t.Fatalf("stamps with .venv: %v", got)
	}
}
