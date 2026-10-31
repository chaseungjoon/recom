package sshx

import (
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
