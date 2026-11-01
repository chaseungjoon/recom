package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The v0.2 workflow: write dependencies, `recom init`, `recom python3 main.py`.
// requirements.txt has no packages so the test needs no network; what is
// checked is that recom creates the backend .venv by itself, puts it on
// PATH, and only redoes the setup when the dependency files change.
func TestInitSetsUpPythonVenvAndRunsUseIt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pyproj")
	write(t, dir, "requirements.txt", "# no third-party packages\n")
	write(t, dir, "main.py", `import os, sys
print("prefix", sys.prefix)
print("venv", os.environ.get("VIRTUAL_ENV", ""))
`)
	r := mustRun(t, dir, "init")
	if !strings.Contains(r.stderr, "detected python (requirements.txt)") || !strings.Contains(r.stderr, "setting up the python environment") || !strings.Contains(r.stderr, "python environment ready") {
		t.Fatalf("init did not set up the backend: %s", r)
	}
	cfg := readFile(filepath.Join(dir, ".recom.toml"))
	if !strings.Contains(cfg, `toolchains = ["python"]`) || !strings.Contains(cfg, `path = [".venv/bin"]`) {
		t.Fatalf(".recom.toml does not record the toolchain:\n%s", cfg)
	}
	rp := replicaPath(t, dir)
	if _, err := os.Stat(filepath.Join(rp, ".venv", "bin", "python")); err != nil {
		t.Fatalf("no backend venv: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".venv")); err == nil {
		t.Fatal("the backend venv must not come back to the laptop")
	}

	r = mustRun(t, dir, "python3", "main.py")
	venv := filepath.Join(rp, ".venv")
	if !strings.Contains(r.stdout, "prefix "+venv) || !strings.Contains(r.stdout, "venv "+venv) {
		t.Fatalf("python3 did not resolve to the project venv: %s", r)
	}
	if strings.Contains(r.stderr, "setting up") || strings.Contains(r.stderr, "updating") {
		t.Fatalf("unchanged dependencies must not trigger a setup: %s", r)
	}

	// Changing requirements.txt re-runs the setup before the command; setup
	// output goes to stderr, so stdout stays the command's own.
	write(t, dir, "requirements.txt", "# still nothing\n\n")
	r = mustRun(t, dir, "python3", "main.py")
	if !strings.Contains(r.stderr, "updating the python environment") {
		t.Fatalf("dependency change did not update the environment: %s", r)
	}
	if lines := strings.Split(strings.TrimSpace(r.stdout), "\n"); len(lines) != 2 {
		t.Fatalf("setup output leaked into stdout: %q", r.stdout)
	}

	// Deleting the environment on the backend rebuilds it.
	if err := os.RemoveAll(venv); err != nil {
		t.Fatal(err)
	}
	r = mustRun(t, dir, "python3", "main.py")
	if !strings.Contains(r.stderr, "setting up the python environment") || !strings.Contains(r.stdout, "prefix "+venv) {
		t.Fatalf("deleted venv was not rebuilt: %s", r)
	}

	r = mustRun(t, dir, "status")
	if !strings.Contains(r.stdout, "Setup     python (requirements.txt): ready") {
		t.Fatalf("status: %s", r)
	}
	r = mustRun(t, dir, "setup", "--clean")
	if !strings.Contains(r.stderr, "removing .venv") || !strings.Contains(r.stderr, "python environment ready") {
		t.Fatalf("setup --clean: %s", r)
	}
}

func TestSetupCommandWatchFailureAndNoSetup(t *testing.T) {
	dir := newProject(t, "custom")
	cfg := filepath.Join(dir, ".recom.toml")
	b := readFile(cfg)
	b = strings.Replace(b, `# command = ""`, `command = "echo built >> setup-marker.txt; test \"$(cat fail 2>/dev/null)\" != yes"`, 1)
	b = strings.Replace(b, "watch = []", `watch = ["deps/*.txt"]`, 1)
	if err := os.WriteFile(cfg, []byte(b), 0o644); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "deps/a.txt", "1\n")

	r := mustRun(t, dir, "cat", "setup-marker.txt")
	if strings.Count(r.stdout, "built") != 1 || !strings.Contains(r.stderr, "running the [setup] command") {
		t.Fatalf("custom command did not run first: %s", r)
	}
	if _, err := os.Stat(filepath.Join(dir, "setup-marker.txt")); err == nil {
		t.Fatal("files written by setup must not be collected as run outputs")
	}
	r = mustRun(t, dir, "cat", "setup-marker.txt")
	if strings.Count(r.stdout, "built") != 1 {
		t.Fatalf("custom command re-ran without a change: %s", r)
	}
	write(t, dir, "deps/a.txt", "2\n")
	r = mustRun(t, dir, "cat", "setup-marker.txt")
	if strings.Count(r.stdout, "built") != 2 {
		t.Fatalf("watched file change did not re-run the command: %s", r)
	}

	// A failing setup stops the run with 125 and the command does not run.
	write(t, dir, "fail", "yes")
	write(t, dir, "deps/a.txt", "3\n")
	r = run(t, dir, "--shell", "echo SHOULD-NOT-RUN")
	if r.code != 125 || strings.Contains(r.stdout, "SHOULD-NOT-RUN") || !strings.Contains(r.stderr, "[setup] command failed") {
		t.Fatalf("failed setup: %s", r)
	}
	r = mustRun(t, dir, "--no-setup", "--shell", "echo ran")
	if !strings.Contains(r.stdout, "ran") {
		t.Fatalf("--no-setup: %s", r)
	}
	write(t, dir, "fail", "no")
	r = mustRun(t, dir, "setup")
	if !strings.Contains(r.stderr, "[setup] command done") {
		t.Fatalf("recom setup: %s", r)
	}
}
