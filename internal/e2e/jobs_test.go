package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type jobOut struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	ExitCode *int   `json:"exit_code"`
	Tmux     bool   `json:"tmux"`
	Pending  int    `json:"pending"`
	PID      int    `json:"pid"`
}

func jobs(t *testing.T, dir string) []jobOut {
	t.Helper()
	r := mustRun(t, dir, "jobs", "--json")
	var js []jobOut
	if err := json.Unmarshal([]byte(r.stdout), &js); err != nil {
		t.Fatalf("%v: %s", err, r)
	}
	return js
}

func waitJob(t *testing.T, dir, id string, states ...string) jobOut {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		for _, j := range jobs(t, dir) {
			if j.ID == id {
				for _, s := range states {
					if j.State == s {
						return j
					}
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("job %s never reached %v: %+v", id, states, jobs(t, dir))
	return jobOut{}
}

func detach(t *testing.T, dir string, args ...string) string {
	t.Helper()
	r := mustRun(t, dir, append([]string{"--detach"}, args...)...)
	id := strings.TrimSpace(r.stdout)
	if id == "" || strings.Contains(id, " ") {
		t.Fatalf("detach did not print a job id: %s", r)
	}
	return id
}

func TestDetachedJobLifecycle(t *testing.T) {
	for _, mode := range []string{"auto", "never"} {
		t.Run("tmux="+mode, func(t *testing.T) {
			dir := newProject(t, "train-"+mode)
			cfg := strings.Replace(readFile(filepath.Join(dir, ".recom.toml")), `tmux = "auto"`, `tmux = "`+mode+`"`, 1)
			write(t, dir, ".recom.toml", cfg)
			write(t, dir, "train.py", `import os, sys, time
for i in range(3):
    print("epoch", i, flush=True)
    time.sleep(0.7)
print("warn", file=sys.stderr)
os.makedirs("ckpt", exist_ok=True)
open("ckpt/model.txt", "w").write("weights")
`)
			id := detach(t, dir, "python3", "-u", "train.py")
			j := waitJob(t, dir, id, "running", "exited")
			if j.Tmux != (mode == "auto") {
				t.Fatalf("tmux mode: %+v", j)
			}
			// logs -f streams until the job ends.
			r := mustRun(t, dir, "logs", id[:8], "-f")
			if !strings.Contains(r.stdout, "epoch 2") {
				t.Fatalf("logs -f: %s", r)
			}
			if mode == "never" && !strings.Contains(r.stderr, "warn") {
				t.Fatalf("stderr log missing: %s", r)
			}
			j = waitJob(t, dir, id, "exited")
			if j.ExitCode == nil || *j.ExitCode != 0 || j.Pending != 1 {
				t.Fatalf("finished job: %+v", j)
			}
			if exists(filepath.Join(dir, "ckpt/model.txt")) {
				t.Fatal("output appeared locally without a recom invocation")
			}
			// The next ordinary command collects the job's outputs first.
			r = mustRun(t, dir, "true")
			if !strings.Contains(r.stderr, "collecting 1 pending output") || !exists(filepath.Join(dir, "ckpt/model.txt")) {
				t.Fatalf("deferred collection: %s", r)
			}
			// attach on a finished job shows its log.
			r = mustRun(t, dir, "attach", id)
			if !strings.Contains(r.stderr, "no longer running") || !strings.Contains(r.stdout, "epoch 0") {
				t.Fatalf("attach finished: %s", r)
			}
			r = mustRun(t, dir, "logs", id, "--tail", "1")
			if strings.Contains(r.stdout, "epoch 0") {
				t.Fatalf("--tail: %s", r)
			}
		})
	}
}

func TestDetachedJobOwnsReplicaAndCanBeStopped(t *testing.T) {
	dir := newProject(t, "busy")
	id := detach(t, dir, "--shell", "echo started > started.txt; trap 'echo term > term.txt; exit 7' TERM; while true; do sleep 0.1; done")
	j := waitJob(t, dir, id, "running")

	r := run(t, dir, "echo", "hi")
	if r.code != 125 || !strings.Contains(r.stderr, "detached job "+id) || !strings.Contains(r.stderr, "recom stop") {
		t.Fatalf("foreground run while a job owns the replica: %s", r)
	}
	r = mustRun(t, dir, "--no-sync", "echo", "quick")
	if strings.TrimSpace(r.stdout) != "quick" || !strings.Contains(r.stderr, "without output tracking") {
		t.Fatalf("--no-sync during job: %s", r)
	}
	r = mustRun(t, dir, "stop", id, "--grace", "5s")
	if !strings.Contains(r.stderr, "stopped") {
		t.Fatalf("stop: %s", r)
	}
	j = waitJob(t, dir, id, "stopped")
	if j.ExitCode == nil || *j.ExitCode != 7 {
		t.Fatalf("stopped job exit: %+v", j)
	}
	if syscall.Kill(j.PID, 0) == nil {
		t.Fatalf("job process %d still alive", j.PID)
	}
	r = mustRun(t, dir, "true")
	for _, f := range []string{"started.txt", "term.txt"} {
		if !exists(filepath.Join(dir, f)) {
			t.Errorf("%s from the stopped job was not collected: %s", f, r)
		}
	}
	r = mustRun(t, dir, "stop", id)
	if !strings.Contains(r.stderr, "not running") {
		t.Fatalf("stop ended job: %s", r)
	}
}

func TestStopEscalatesToKill(t *testing.T) {
	dir := newProject(t, "kill")
	id := detach(t, dir, "--shell", "trap '' TERM; while true; do sleep 0.1; done")
	waitJob(t, dir, id, "running")
	r := mustRun(t, dir, "stop", id, "--grace", "1s")
	if !strings.Contains(r.stderr, "SIGKILL") {
		t.Fatalf("expected SIGKILL escalation: %s", r)
	}
	waitJob(t, dir, id, "stopped")
}

func TestJobSurvivesClientAndLostSupervisorIsRecovered(t *testing.T) {
	dir := newProject(t, "lost")
	id := detach(t, dir, "--shell", "echo before > before.txt; sleep 300")
	waitJob(t, dir, id, "running")
	// Simulate a backend crash: kill the supervisor and the job's process group.
	st := status(t, dir)
	stateDir := filepath.Join(filepath.Dir(filepath.Dir(st.Remote.ReplicaPath)), "state", st.Replica)
	var rec struct {
		Supervisor int `json:"supervisor_pid"`
		PID        int `json:"pid"`
	}
	if err := json.Unmarshal([]byte(readFile(filepath.Join(stateDir, "jobs", id, "job.json"))), &rec); err != nil {
		t.Fatal(err)
	}
	_ = syscall.Kill(rec.Supervisor, syscall.SIGKILL)
	_ = syscall.Kill(-rec.PID, syscall.SIGKILL)
	time.Sleep(300 * time.Millisecond)
	waitJob(t, dir, id, "lost")
	r := mustRun(t, dir, "true")
	if !strings.Contains(r.stderr, "recovered lost job "+id) || !exists(filepath.Join(dir, "before.txt")) {
		t.Fatalf("lost job recovery: %s", r)
	}
}

func TestConcurrentRunsAreSerialized(t *testing.T) {
	dir := newProject(t, "concurrent")
	mustRun(t, dir, "true")
	var wg sync.WaitGroup
	results := make([]result, 2)
	start := time.Now()
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = run(t, dir, "--shell", "sleep 1.5; echo run >> shared.txt")
		}(i)
		time.Sleep(200 * time.Millisecond)
	}
	wg.Wait()
	for _, r := range results {
		if r.code != 0 {
			t.Fatal(r)
		}
	}
	if el := time.Since(start); el < 3*time.Second {
		t.Fatalf("runs overlapped (%s)", el)
	}
	if !strings.Contains(results[1].stderr, "waiting for run") {
		t.Fatalf("second run did not report waiting: %s", results[1])
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "shared.txt")); string(b) != "run\nrun\n" {
		t.Fatalf("serialized outputs: %q", b)
	}
}
