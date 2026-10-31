// Package e2e runs recom end to end against a throwaway, unprivileged sshd on
// 127.0.0.1. It needs /usr/sbin/sshd, ssh, ssh-keygen and rsync, and only
// runs when RECOM_E2E=1 (see `make test-integration`).
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

var env struct {
	dir     string // harness root
	recom   string // client binary
	helper  string // helper binary
	sshCfg  string
	port    int
	sshd    *exec.Cmd
	baseEnv []string
}

func TestMain(m *testing.M) {
	if os.Getenv("RECOM_E2E") == "" {
		fmt.Println("e2e: skipped (set RECOM_E2E=1)")
		os.Exit(0)
	}
	code, err := setup()
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e setup:", err)
		teardown()
		os.Exit(1)
	}
	if code == 0 {
		code = m.Run()
	}
	teardown()
	os.Exit(code)
}

func sh(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %v\n%s", name, args, err, out)
	}
	return nil
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func setup() (int, error) {
	d, err := os.MkdirTemp("", "recom-e2e-")
	if err != nil {
		return 1, err
	}
	env.dir = d
	root := repoRoot()
	env.recom = filepath.Join(d, "bin", "recom")
	env.helper = filepath.Join(d, "bin", "recom-helper")
	build := func(out, pkg string, extra ...string) error {
		cgo := "CGO_ENABLED=0"
		if os.Getenv("RECOM_E2E_RACE") != "" { // race-instrumented binaries
			extra = append([]string{"-race"}, extra...)
			cgo = "CGO_ENABLED=1"
		}
		cmd := exec.Command("go", append([]string{"build", "-o", out}, append(extra, pkg)...)...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), cgo)
		if b, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("build %s: %v\n%s", pkg, err, b)
		}
		return nil
	}
	ld := "-X github.com/chaseungjoon/recom/internal/buildinfo.Version=e2e"
	if err := build(env.recom, "./cmd/recom", "-ldflags", ld); err != nil {
		return 1, err
	}
	if err := build(env.helper, "./cmd/recom-helper", "-ldflags", ld); err != nil {
		return 1, err
	}
	sd := filepath.Join(d, "sshd")
	if err := os.MkdirAll(sd, 0o700); err != nil {
		return 1, err
	}
	for _, k := range []string{"host_key", "user_key"} {
		if err := sh("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(sd, k)); err != nil {
			return 1, err
		}
	}
	pub, _ := os.ReadFile(filepath.Join(sd, "user_key.pub"))
	if err := os.WriteFile(filepath.Join(sd, "authorized_keys"), pub, 0o600); err != nil {
		return 1, err
	}
	env.port, err = freePort()
	if err != nil {
		return 1, err
	}
	cfg := fmt.Sprintf(`Port %d
ListenAddress 127.0.0.1
HostKey %s/host_key
AuthorizedKeysFile %s/authorized_keys
PidFile %s/sshd.pid
UsePAM no
StrictModes no
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
MaxSessions 50
MaxStartups 50
`, env.port, sd, sd, sd)
	if err := os.WriteFile(filepath.Join(sd, "sshd_config"), []byte(cfg), 0o600); err != nil {
		return 1, err
	}
	hostPub, _ := os.ReadFile(filepath.Join(sd, "host_key.pub"))
	fields := strings.Fields(string(hostPub))
	kh := fmt.Sprintf("[127.0.0.1]:%d %s %s\n", env.port, fields[0], fields[1])
	if err := os.WriteFile(filepath.Join(sd, "known_hosts"), []byte(kh), 0o600); err != nil {
		return 1, err
	}
	user := os.Getenv("USER")
	if user == "" {
		out, _ := exec.Command("id", "-un").Output()
		user = strings.TrimSpace(string(out))
	}
	env.sshCfg = filepath.Join(sd, "ssh_config")
	clientCfg := fmt.Sprintf(`Host testhost deadhost
  HostName 127.0.0.1
  User %s
  IdentityFile %s/user_key
  IdentitiesOnly yes
  UserKnownHostsFile %s/known_hosts
  StrictHostKeyChecking yes
  BatchMode yes
Host testhost
  Port %d
Host deadhost
  Port 1
  ConnectTimeout 3
`, user, sd, sd, env.port)
	if err := os.WriteFile(env.sshCfg, []byte(clientCfg), 0o600); err != nil {
		return 1, err
	}
	sshdBin := "/usr/sbin/sshd"
	if p := os.Getenv("RECOM_E2E_SSHD"); p != "" {
		sshdBin = p
	}
	env.sshd = exec.Command(sshdBin, "-D", "-f", filepath.Join(sd, "sshd_config"), "-E", filepath.Join(sd, "sshd.log"))
	if err := env.sshd.Start(); err != nil {
		return 1, fmt.Errorf("start sshd: %w", err)
	}
	for i := 0; i < 100; i++ {
		c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", env.port))
		if err == nil {
			c.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	env.baseEnv = append(filterEnv(os.Environ()),
		"XDG_CONFIG_HOME="+filepath.Join(d, "config"),
		"XDG_CACHE_HOME="+filepath.Join(d, "cache"),
		"RECOM_SSH_CONFIG="+env.sshCfg,
		"RECOM_HELPER_PATH="+env.helper,
		"NO_COLOR=1",
	)
	// Register the backend once for all tests.
	r := run(nil, d, "ssh", "testhost", "--remote-root", filepath.Join(d, "remote"))
	if r.code != 0 {
		return 1, fmt.Errorf("recom ssh testhost failed (%d):\n%s\n%s\nsshd log:\n%s", r.code, r.stdout, r.stderr, readFile(filepath.Join(sd, "sshd.log")))
	}
	return 0, nil
}

func filterEnv(in []string) []string {
	var out []string
	for _, kv := range in {
		if strings.HasPrefix(kv, "RECOM_") || strings.HasPrefix(kv, "XDG_CONFIG_HOME=") || strings.HasPrefix(kv, "XDG_CACHE_HOME=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func teardown() {
	if env.sshd != nil && env.sshd.Process != nil {
		_ = env.sshd.Process.Kill()
		_, _ = env.sshd.Process.Wait()
	}
	// Close any ControlMaster connections.
	if env.dir != "" {
		matches, _ := filepath.Glob(filepath.Join(env.dir, "cache", "recom", "ssh", "*"))
		for _, m := range matches {
			_ = exec.Command("ssh", "-F", env.sshCfg, "-o", "ControlPath="+m, "-O", "exit", "testhost").Run()
		}
		// Detached test jobs run in tmux on a private socket name.
		_ = exec.Command("tmux", "-L", "recom", "kill-server").Run()
		if os.Getenv("RECOM_E2E_KEEP") == "" {
			_ = os.RemoveAll(env.dir)
		} else {
			fmt.Println("e2e: kept", env.dir)
		}
	}
}

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func readFile(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

type result struct {
	stdout, stderr string
	code           int
}

func (r result) String() string {
	return fmt.Sprintf("exit %d\n--- stdout ---\n%s\n--- stderr ---\n%s", r.code, r.stdout, r.stderr)
}

// run executes recom in dir. t may be nil during setup.
func run(t *testing.T, dir string, args ...string) result {
	return runEnv(t, dir, nil, "", args...)
}

func runEnv(t *testing.T, dir string, extraEnv []string, stdin string, args ...string) result {
	if t != nil {
		t.Helper()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, env.recom, args...)
	cmd.Dir = dir
	cmd.Env = append(append([]string{}, env.baseEnv...), extraEnv...)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		code = -1
		se.WriteString("\n[harness] " + err.Error())
	}
	return result{so.String(), se.String(), code}
}

// newProject creates and initializes a project directory.
func newProject(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if r := run(t, dir, "init"); r.code != 0 {
		t.Fatalf("init: %s", r)
	}
	return dir
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustRun(t *testing.T, dir string, args ...string) result {
	t.Helper()
	r := run(t, dir, args...)
	if r.code != 0 {
		t.Fatalf("recom %v: %s", args, r)
	}
	return r
}

type statusOut struct {
	Replica string `json:"replica_id"`
	Remote  struct {
		ReplicaPath string `json:"replica_path"`
		Pending     []struct {
			Path   string `json:"path"`
			Status string `json:"status"`
		} `json:"pending"`
	} `json:"remote"`
}

func status(t *testing.T, dir string) statusOut {
	t.Helper()
	r := mustRun(t, dir, "status", "--json")
	var s statusOut
	if err := json.Unmarshal([]byte(r.stdout), &s); err != nil {
		t.Fatalf("status json: %v\n%s", err, r)
	}
	return s
}

func replicaPath(t *testing.T, dir string) string {
	return status(t, dir).Remote.ReplicaPath
}
