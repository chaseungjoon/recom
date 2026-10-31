package remote

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/chaseungjoon/recom/internal/sshx"
)

// HostInfo is what registration discovers about a backend.
type HostInfo struct {
	Home  string
	OS    string
	Arch  string
	Noise string // text printed by shell startup files before our output
}

const (
	probeBegin = "__RECOM_PROBE_BEGIN__"
	probeEnd   = "__RECOM_PROBE_END__"
)

// Probe connects with plain OpenSSH and discovers the remote home directory,
// OS and architecture. Markers make it robust to chatty shell startup files.
func Probe(ctx context.Context, t sshx.Target) (*HostInfo, error) {
	script := "echo " + probeBegin + "; printf '%s\\n' \"$HOME\"; uname -s; uname -m; echo " + probeEnd
	cmd := t.Command(ctx, false, script)
	var out, eb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &eb
	if err := cmd.Run(); err != nil {
		return nil, &TransportError{Msg: "cannot connect to " + t.Host.Target, Stderr: eb.String()}
	}
	s := out.String()
	i := strings.Index(s, probeBegin+"\n")
	j := strings.Index(s, probeEnd)
	if i < 0 || j < i {
		return nil, fmt.Errorf("unexpected output from %s; is the login shell POSIX-compatible?", t.Host.Target)
	}
	lines := strings.Split(strings.TrimSpace(s[i+len(probeBegin)+1:j]), "\n")
	if len(lines) != 3 {
		return nil, fmt.Errorf("unexpected probe output from %s: %q", t.Host.Target, s[i:j])
	}
	info := &HostInfo{Home: strings.TrimSpace(lines[0]), Noise: s[:i]}
	switch strings.TrimSpace(lines[1]) {
	case "Linux":
		info.OS = "linux"
	case "Darwin":
		info.OS = "darwin"
	default:
		info.OS = strings.ToLower(strings.TrimSpace(lines[1]))
	}
	switch m := strings.TrimSpace(lines[2]); m {
	case "x86_64", "amd64":
		info.Arch = "amd64"
	case "aarch64", "arm64":
		info.Arch = "arm64"
	default:
		info.Arch = m
	}
	return info, nil
}
