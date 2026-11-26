package sshx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chaseungjoon/recom/internal/config"
)

func TestValidTarget(t *testing.T) {
	for _, ok := range []string{"aiworkstation", "me@10.0.0.5", "me@host.example.com", "gpu-box_2"} {
		if err := ValidTarget(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-oProxyCommand=x", "a b", "host;rm", "$(x)", "a\nb"} {
		if ValidTarget(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestValidOption(t *testing.T) {
	if ValidOption("ProxyJump=bastion") != nil {
		t.Error("ProxyJump rejected")
	}
	for _, bad := range []string{"StrictHostKeyChecking=no", "noequals", "-x=1", "A=b\nc"} {
		if ValidOption(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestArgsAndQuoting(t *testing.T) {
	t.Setenv("RECOM_NO_MULTIPLEX", "1")
	tg := Target{Name: "gpu", Host: &config.Host{Target: "me@gpu", Port: 2222, Identity: "/k/id key", SSHOptions: []string{"ProxyJump=b"}}}
	a := strings.Join(tg.BaseArgs(), " ")
	for _, want := range []string{"-p 2222", "-i /k/id key", "-o ProxyJump=b", "ServerAliveInterval"} {
		if !strings.Contains(a, want) {
			t.Errorf("args missing %q: %s", want, a)
		}
	}
	if strings.Contains(a, "StrictHostKeyChecking") {
		t.Error("host key checking must be left to OpenSSH")
	}
	if e := tg.RsyncShell(); !strings.Contains(e, "'/k/id key'") {
		t.Errorf("rsync -e quoting: %s", e)
	}
	if q := Quote("it's"); q != `'it'\''s'` {
		t.Errorf("Quote: %s", q)
	}
}

func TestControlDirRejectsUnsafeFallback(t *testing.T) {
	// Short base so XDG_RUNTIME_DIR/recom fits the socket path limit.
	base, err := os.MkdirTemp("", "rc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	// Force the fallback: a cache path too long for a socket.
	t.Setenv("XDG_CACHE_HOME", filepath.Join(base, strings.Repeat("x", 100)))
	t.Setenv("RECOM_NO_MULTIPLEX", "")
	rt := filepath.Join(base, "rt")
	if err := os.Mkdir(rt, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", rt)
	tg := Target{Name: "gpu", Host: &config.Host{Target: "me@gpu"}}

	d := filepath.Join(rt, "recom")
	if err := os.Mkdir(d, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(d, 0o777); err != nil {
		t.Fatal(err)
	}
	if a := strings.Join(tg.BaseArgs(), " "); strings.Contains(a, "ControlPath") {
		t.Fatalf("world-writable control dir used: %s", a)
	}

	os.Remove(d)
	other := filepath.Join(base, "other")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, d); err != nil {
		t.Fatal(err)
	}
	if a := strings.Join(tg.BaseArgs(), " "); strings.Contains(a, "ControlPath") {
		t.Fatalf("symlinked control dir used: %s", a)
	}

	os.Remove(d)
	a := strings.Join(tg.BaseArgs(), " ")
	if !strings.Contains(a, "ControlPath="+filepath.Join(d, "%C")) {
		t.Fatalf("private control dir not used: %s", a)
	}
	if fi, err := os.Lstat(d); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("control dir not created private: %v %v", fi, err)
	}
}
