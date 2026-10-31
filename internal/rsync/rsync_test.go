package rsync

import (
	"os/exec"
	"testing"
)

func TestVersionAtLeast(t *testing.T) {
	cases := map[string]bool{
		"rsync  version 3.2.7  protocol version 31":    true,
		"rsync  version 3.1.3  protocol version 31":    true,
		"rsync  version 3.1.2  protocol version 31":    false,
		"rsync  version 2.6.9  protocol version 29":    false,
		"openrsync: protocol version 29 version 3.2.0": false,
		"garbage": false,
	}
	for in, want := range cases {
		if got := versionAtLeast(in, 3, 1, 3); got != want {
			t.Errorf("%q: %v", in, got)
		}
	}
}

func TestDetectLocal(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not installed")
	}
	b, err := Detect()
	if err != nil {
		t.Fatal(err)
	}
	if b.Path == "" || b.Version == "" {
		t.Fatalf("%+v", b)
	}
}

func TestDetectRejectsMissingFeatures(t *testing.T) {
	t.Setenv("RECOM_RSYNC", "/bin/echo")
	if _, err := Detect(); err == nil {
		t.Fatal("a binary without rsync options was accepted")
	}
}
