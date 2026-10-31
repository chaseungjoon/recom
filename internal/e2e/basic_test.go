package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFirstRunUploadsRunsRemotelyAndCollects(t *testing.T) {
	dir := newProject(t, "demo")
	write(t, dir, "src/run.py", `import json, os, sys
os.makedirs("outputs", exist_ok=True)
json.dump({"cwd": os.getcwd(), "argv": sys.argv[1:]}, open("outputs/result.json", "w"))
print("hello from", os.getcwd())
print("to stderr", file=sys.stderr)
`)
	r := mustRun(t, dir, "python3", "src/run.py", "a b")
	if !strings.Contains(r.stdout, "hello from") {
		t.Fatalf("stdout missing: %s", r)
	}
	if !strings.Contains(r.stderr, "to stderr") {
		t.Fatalf("stderr missing: %s", r)
	}
	rp := replicaPath(t, dir)
	if !strings.Contains(r.stdout, rp) {
		t.Fatalf("command did not run in the remote replica %s: %s", rp, r)
	}
	b, err := os.ReadFile(filepath.Join(dir, "outputs/result.json"))
	if err != nil {
		t.Fatalf("output not collected: %v\n%s", err, r)
	}
	if !strings.Contains(string(b), `"a b"`) {
		t.Fatalf("argv not preserved: %s", b)
	}
	if !strings.Contains(r.stderr, "synced") {
		t.Fatalf("expected upload report: %s", r)
	}
}
