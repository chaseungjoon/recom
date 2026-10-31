package e2e

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestArgvIsPreservedExactly(t *testing.T) {
	dir := newProject(t, "argv")
	args := []string{"a b", "", "--flag", "-x", "ünï", "$(touch pwned)", "'q'", "\"dq\"", ";ls", "*", "back\\slash", "new\nline", "$HOME"}
	r := mustRun(t, dir, append([]string{"python3", "-c", "import json,sys; print(json.dumps(sys.argv[1:]))"}, args...)...)
	var got []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(r.stdout)), &got); err != nil {
		t.Fatalf("%v: %s", err, r)
	}
	if len(got) != len(args) {
		t.Fatalf("argv length %d != %d: %q", len(got), len(args), got)
	}
	for i := range args {
		if got[i] != args[i] {
			t.Errorf("arg %d: got %q want %q", i, got[i], args[i])
		}
	}
	if exists(filepath.Join(dir, "pwned")) || exists(filepath.Join(replicaPath(t, dir), "pwned")) {
		t.Fatal("argument was interpreted by a shell")
	}
}

func TestStdinIsForwarded(t *testing.T) {
	dir := newProject(t, "stdin")
	r := runEnv(t, dir, nil, "hello\nworld\n", "python3", "-c", "import sys; print(sys.stdin.read()[::-1])")
	if r.code != 0 || !strings.Contains(r.stdout, "dlrow\nolleh") {
		t.Fatalf("stdin: %s", r)
	}
}

func TestNestedDirectoryMapsToRemote(t *testing.T) {
	dir := newProject(t, "nested")
	write(t, dir, "src/deep/run.py", "import os\nprint(os.getcwd())\nopen('made-here.txt','w').write('x')\n")
	r := mustRun(t, filepath.Join(dir, "src", "deep"), "python3", "run.py")
	if !strings.HasSuffix(strings.TrimSpace(r.stdout), "/src/deep") {
		t.Fatalf("remote cwd: %s", r)
	}
	if !exists(filepath.Join(dir, "src/deep/made-here.txt")) {
		t.Fatalf("output from nested cwd not collected in place: %s", r)
	}
	if !strings.Contains(r.stderr, "← made-here.txt") {
		t.Fatalf("output path should be shown relative to cwd: %s", r)
	}
}

func TestNoSync(t *testing.T) {
	dir := newProject(t, "nosync")
	// No replica yet: runs from the remote home, does not create one.
	r := mustRun(t, dir, "--no-sync", "pwd")
	home, _ := os.UserHomeDir()
	if strings.TrimSpace(r.stdout) != home || !strings.Contains(r.stderr, "no replica") {
		t.Fatalf("no-sync without replica: %s", r)
	}
	if st := status(t, dir); exists(st.Remote.ReplicaPath) {
		t.Fatal("--no-sync created a replica")
	}
	mustRun(t, dir, "true")
	write(t, dir, "late.txt", "x")
	r = mustRun(t, dir, "--no-sync", "ls")
	if strings.Contains(r.stdout, "late.txt") {
		t.Fatalf("--no-sync uploaded: %s", r)
	}
}

func TestOutsideProject(t *testing.T) {
	dir := t.TempDir()
	r := run(t, dir, "echo", "hi")
	if r.code != 125 || !strings.Contains(r.stderr, "recom init") {
		t.Fatalf("outside project: %s", r)
	}
	r = run(t, dir, "--no-sync", "echo", "hi")
	if r.code != 0 || strings.TrimSpace(r.stdout) != "hi" {
		t.Fatalf("backend-global --no-sync: %s", r)
	}
}

func TestCommandNotFoundAndNoLocalFallback(t *testing.T) {
	dir := newProject(t, "notfound")
	// A command that exists only locally must not be run.
	bin := filepath.Join(t.TempDir(), "only-local-tool")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho LOCAL\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := runEnv(t, dir, []string{"PATH=" + filepath.Dir(bin) + ":" + os.Getenv("PATH")}, "", "only-local-tool")
	if r.code != 127 || strings.Contains(r.stdout, "LOCAL") || !strings.Contains(r.stderr, "command not found on the backend") {
		t.Fatalf("missing remote command: %s", r)
	}
	r = run(t, dir, "stauts")
	if !strings.Contains(r.stderr, "did you mean `recom status`") {
		t.Fatalf("no subcommand hint: %s", r)
	}
	// "--" forces a remote command even if it collides with a subcommand.
	r = mustRun(t, dir, "--", "env")
	if !strings.Contains(r.stdout, "RECOM=1") {
		t.Fatalf("recom -- env: %s", r)
	}
}

func TestShellModeRunsRemotely(t *testing.T) {
	dir := newProject(t, "shell")
	r := mustRun(t, dir, "--shell", "echo $((6*7)) | tee answer.txt")
	if strings.TrimSpace(r.stdout) != "42" {
		t.Fatal(r)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "answer.txt")); string(b) != "42\n" {
		t.Fatal("remote redirection output not collected")
	}
}

func TestOutputStreamsBeforeExit(t *testing.T) {
	dir := newProject(t, "stream")
	mustRun(t, dir, "true")
	cmd := exec.Command(env.recom, "python3", "-c", "import time; print('first'); time.sleep(4); print('second')")
	cmd.Dir = dir
	cmd.Env = env.baseEnv
	out, _ := cmd.StdoutPipe()
	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || line != "first\n" {
		t.Fatalf("first line: %q %v", line, err)
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Fatalf("output was buffered until exit (%s)", el)
	}
	_, _ = io.Copy(io.Discard, out)
	_ = cmd.Wait()
}

func TestInterruptTerminatesRemoteProcess(t *testing.T) {
	dir := newProject(t, "sigint")
	mustRun(t, dir, "true")
	rp := replicaPath(t, dir)
	cmd := exec.Command(env.recom, "python3", "-c", "import os,time; open('pid','w').write(str(os.getpid())); time.sleep(60)")
	cmd.Dir = dir
	cmd.Env = env.baseEnv
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := 0
	for i := 0; i < 100 && pid == 0; i++ {
		time.Sleep(100 * time.Millisecond)
		b, _ := os.ReadFile(filepath.Join(rp, "pid"))
		pid, _ = strconv.Atoi(string(b))
	}
	if pid == 0 {
		t.Fatal("remote process did not start")
	}
	_ = cmd.Process.Signal(os.Interrupt)
	err := cmd.Wait()
	code := -1
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	}
	if code != 130 {
		t.Fatalf("exit code after Ctrl-C: %d", code)
	}
	time.Sleep(300 * time.Millisecond)
	if syscall.Kill(pid, 0) == nil {
		t.Fatalf("remote process %d survived the interrupt", pid)
	}
}

func TestTTYMode(t *testing.T) {
	dir := newProject(t, "tty")
	mustRun(t, dir, "true")
	cmd := exec.Command(env.recom, "--tty", "python3", "-c",
		"import os,sys; print('isatty', os.isatty(0), os.isatty(1)); print('size', os.get_terminal_size()); n=input('name? '); print('hi', n)")
	cmd.Dir = dir
	cmd.Env = env.baseEnv
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 33, Cols: 101})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out strings.Builder
	done := make(chan struct{})
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := f.Read(buf)
			out.Write(buf[:n])
			if err != nil {
				close(done)
				return
			}
		}
	}()
	deadline := time.Now().Add(20 * time.Second)
	for !strings.Contains(out.String(), "name? ") && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	_, _ = f.Write([]byte("bob\r"))
	_ = cmd.Wait()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
	s := out.String()
	for _, want := range []string{"isatty True True", "columns=101, lines=33", "hi bob"} {
		if !strings.Contains(s, want) {
			t.Errorf("tty output missing %q:\n%s", want, s)
		}
	}
}
