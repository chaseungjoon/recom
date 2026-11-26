// Package sshx builds OpenSSH invocations. recom never implements SSH itself:
// it reuses the user's ssh binary, ~/.ssh/config aliases, agent, keys,
// known_hosts verification, ProxyJump/ProxyCommand and Tailscale setup.
package sshx

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/chaseungjoon/recom/internal/config"
)

var (
	targetRe = regexp.MustCompile(`^[A-Za-z0-9._%+\[\]:@-]+$`)
	optRe    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*=[^\n\x00]*$`)
)

// ValidTarget checks an ssh destination string.
func ValidTarget(t string) error {
	if t == "" || strings.HasPrefix(t, "-") || !targetRe.MatchString(t) {
		return fmt.Errorf("invalid SSH target %q (use an ~/.ssh/config alias or user@host)", t)
	}
	return nil
}

// ValidOption checks an extra ssh -o option.
func ValidOption(o string) error {
	if !optRe.MatchString(o) {
		return fmt.Errorf("invalid ssh option %q (expected Key=Value)", o)
	}
	k := strings.ToLower(strings.SplitN(o, "=", 2)[0])
	if k == "stricthostkeychecking" && strings.EqualFold(strings.SplitN(o, "=", 2)[1], "no") {
		return fmt.Errorf("refusing StrictHostKeyChecking=no: recom keeps OpenSSH host key verification on")
	}
	return nil
}

// Binary returns the ssh executable (RECOM_SSH overrides).
func Binary() string {
	if s := os.Getenv("RECOM_SSH"); s != "" {
		return s
	}
	return "ssh"
}

// Target is a backend reachable over SSH.
type Target struct {
	Name string
	Host *config.Host
}

// controlDir returns a private directory for ControlMaster sockets. OpenSSH
// does not check who owns a control socket, so the directory must not be
// writable (or replaceable) by other users.
func controlDir() (string, error) {
	// Unix socket paths are limited (~104 bytes on macOS); %C expands to 40 hex chars.
	fits := func(d string) bool { return len(d)+42 <= 100 }
	if d := filepath.Join(config.CacheDir(), "ssh"); fits(d) {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return "", err
		}
		return d, checkPrivateDir(d)
	}
	d := filepath.Join(os.TempDir(), "recom-"+strconv.Itoa(os.Getuid()))
	if r := os.Getenv("XDG_RUNTIME_DIR"); r != "" && filepath.IsAbs(r) && fits(filepath.Join(r, "recom")) {
		d = filepath.Join(r, "recom")
	}
	// Mkdir, not MkdirAll: the parent may be shared (e.g. /tmp).
	if err := os.Mkdir(d, 0o700); err != nil && !os.IsExist(err) {
		return "", err
	}
	return d, checkPrivateDir(d)
}

// checkPrivateDir verifies d is a real directory owned by the current user
// and inaccessible to anyone else.
func checkPrivateDir(d string) error {
	fi, err := os.Lstat(d)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", d)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s is not owned by the current user", d)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s is accessible by other users (mode %04o)", d, fi.Mode().Perm())
	}
	return nil
}

// BaseArgs returns ssh options (everything before the destination).
func (t Target) BaseArgs() []string {
	var a []string
	if f := os.Getenv("RECOM_SSH_CONFIG"); f != "" {
		a = append(a, "-F", f)
	}
	if t.Host.Port > 0 {
		a = append(a, "-p", strconv.Itoa(t.Host.Port))
	}
	if t.Host.Identity != "" {
		a = append(a, "-i", t.Host.Identity)
	}
	a = append(a,
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=4",
		"-o", "ConnectTimeout=20",
	)
	if t.Host.UseMultiplex() && os.Getenv("RECOM_NO_MULTIPLEX") == "" {
		if d, err := controlDir(); err == nil {
			a = append(a,
				"-o", "ControlMaster=auto",
				"-o", "ControlPath="+filepath.Join(d, "%C"),
				"-o", "ControlPersist=120",
			)
		}
	}
	for _, o := range t.Host.SSHOptions {
		a = append(a, "-o", o)
	}
	return a
}

// Command returns an ssh command running remote (a shell command line that
// the remote login shell interprets; callers must quote everything).
func (t Target) Command(ctx context.Context, tty bool, remote string) *exec.Cmd {
	args := t.BaseArgs()
	if tty {
		args = append(args, "-t")
	} else {
		args = append(args, "-T")
	}
	args = append(args, "--", t.Host.Target, remote)
	return exec.CommandContext(ctx, Binary(), args...)
}

// RsyncShell returns the value for rsync's -e option.
func (t Target) RsyncShell() string {
	parts := append([]string{Binary()}, t.BaseArgs()...)
	parts = append(parts, "-T")
	for i, p := range parts {
		parts[i] = rsyncQuote(p)
	}
	return strings.Join(parts, " ")
}

// rsyncQuote quotes one word for rsync's -e parser, which splits on spaces
// and understands single and double quotes (but not backslashes).
func rsyncQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " '\"\t") {
		return s
	}
	if !strings.Contains(s, "'") {
		return "'" + s + "'"
	}
	return `"` + s + `"`
}

// Quote single-quotes s for a POSIX shell.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Remote returns "user@host:path" style rsync remote spec.
func (t Target) Remote(path string) string { return t.Host.Target + ":" + path }
