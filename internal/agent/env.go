package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chaseungjoon/recom/internal/protocol"
)

// Environment modes.
const (
	EnvLogin = "login" // capture the environment of `$SHELL -l` (sources ~/.profile etc.)
	EnvSSH   = "ssh"   // use the environment sshd gave the helper
)

const envMagic = "\x00RECOM-ENV\x00"

// dumpEnv prints the current environment for capture by captureLoginEnv.
func dumpEnv() int {
	b, _ := json.Marshal(os.Environ())
	os.Stdout.WriteString(envMagic)
	os.Stdout.Write(b)
	return 0
}

func userShell() string {
	if s := os.Getenv("SHELL"); s != "" && filepath.IsAbs(s) {
		return s
	}
	return "/bin/sh"
}

// captureLoginEnv runs the user's login shell and records its environment.
func captureLoginEnv() ([]string, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, userShell(), "-l", "-c", shellQuote(self)+" __agent dumpenv")
	cmd.Stdin = nil
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = nil
	cmd.Dir = os.Getenv("HOME")
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("login shell %s failed: %w", userShell(), err)
	}
	b := out.Bytes()
	i := bytes.LastIndex(b, []byte(envMagic))
	if i < 0 {
		return nil, errors.New("login shell produced no environment")
	}
	var env []string
	if err := json.Unmarshal(b[i+len(envMagic):], &env); err != nil {
		return nil, fmt.Errorf("login shell environment unreadable: %w", err)
	}
	return env, nil
}

// shellQuote single-quotes s for POSIX shells (and fish, for paths without quotes).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

type envMap map[string]string

func toMap(env []string) envMap {
	m := envMap{}
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			m[k] = v
		}
	}
	return m
}

func (m envMap) list() []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// buildEnv computes the environment for a remote command.
func buildEnv(mode string, extra map[string]string, pathPrepend, pathFallback []string, relEnv map[string]string, replica string) (envMap, string) {
	var warn string
	base := os.Environ()
	if mode == "" {
		mode = EnvLogin
	}
	if mode == EnvLogin {
		if env, err := captureLoginEnv(); err == nil {
			base = env
		} else {
			warn = fmt.Sprintf("could not load login environment (%v); using the plain SSH environment", err)
		}
	}
	m := toMap(base)
	// Variables that only make sense for the helper's own SSH channel.
	for _, k := range []string{"SHLVL", "_", "OLDPWD", "PWD"} {
		delete(m, k)
	}
	if _, ok := extra["PYTHONUNBUFFERED"]; !ok {
		if _, ok := m["PYTHONUNBUFFERED"]; !ok {
			m["PYTHONUNBUFFERED"] = "1"
		}
	}
	if replica != "" {
		for k, v := range relEnv {
			rel, err := CleanRel(v)
			if err != nil || k == "" || strings.ContainsAny(k, "=\x00") {
				continue
			}
			abs := filepath.Join(replica, filepath.FromSlash(rel))
			if _, err := os.Stat(abs); err == nil {
				m[k] = abs
			}
		}
	}
	for k, v := range extra {
		if v == "" {
			delete(m, k)
			continue
		}
		m[k] = v
	}
	if len(pathPrepend) > 0 {
		var parts []string
		for _, p := range pathPrepend {
			if !filepath.IsAbs(p) {
				if replica == "" {
					continue
				}
				p = filepath.Join(replica, p)
			}
			parts = append(parts, p)
		}
		if m["PATH"] != "" {
			parts = append(parts, m["PATH"])
		}
		m["PATH"] = strings.Join(parts, ":")
	}
	if m["PATH"] == "" {
		m["PATH"] = "/usr/local/bin:/usr/bin:/bin"
	}
	home := m["HOME"]
	if home == "" {
		home = os.Getenv("HOME")
	}
	m["PATH"] = appendFallback(m["PATH"], pathFallback, home)
	m["RECOM"] = "1"
	return m, warn
}

// errNotFound / errNotExec map to shell-style exit codes 127 / 126.
type lookupError struct {
	code int
	msg  string
}

func (e *lookupError) Error() string { return e.msg }

// lookPath resolves name like a shell would, against PATH from env (not the
// helper's own PATH), relative to dir for names containing a slash.
func lookPath(name, pathEnv, dir string) (string, error) {
	if name == "" {
		return "", &lookupError{127, "empty command"}
	}
	check := func(p string) error {
		fi, err := os.Stat(p)
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return &lookupError{126, name + ": is a directory"}
		}
		if fi.Mode()&0o111 == 0 {
			return &lookupError{126, name + ": permission denied (not executable)"}
		}
		return nil
	}
	if strings.Contains(name, "/") {
		p := name
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		if err := check(p); err != nil {
			var le *lookupError
			if errors.As(err, &le) {
				return "", le
			}
			if errors.Is(err, fs.ErrNotExist) {
				return "", &lookupError{127, name + ": no such file on the backend (did it get synced? is it excluded?)"}
			}
			return "", &lookupError{126, name + ": " + err.Error()}
		}
		return p, nil
	}
	var denied error
	for _, d := range filepath.SplitList(pathEnv) {
		if d == "" {
			d = dir
		} else if !filepath.IsAbs(d) {
			d = filepath.Join(dir, d)
		}
		p := filepath.Join(d, name)
		err := check(p)
		if err == nil {
			return p, nil
		}
		var le *lookupError
		if errors.As(err, &le) && denied == nil {
			denied = le
		}
	}
	if denied != nil {
		return "", denied
	}
	return "", &lookupError{127, name + ": command not found on the backend"}
}

// firstLine runs a probe command and returns the first non-empty output line.
func firstLine(ctx context.Context, env []string, path string, args ...string) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = env
	out, _ := cmd.CombinedOutput()
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			return l
		}
	}
	return ""
}

func runOutput(ctx context.Context, env []string, path string, args ...string) string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = env
	out, _ := cmd.CombinedOutput()
	return string(out)
}

func envForRequest(r *protocol.ExecRequest, replica string) (envMap, string) {
	return buildEnv(r.EnvMode, r.Env, r.Path, r.PathFallback, r.RelEnv, replica)
}

// appendFallback appends existing fallback directories to a PATH value.
// Entries may start with "~/" and contain globs; among a glob's matches the
// highest version wins (~/.nvm/versions/node/v22.1.0 over v18.20.4).
func appendFallback(pathVal string, entries []string, home string) string {
	have := map[string]bool{}
	for _, d := range filepath.SplitList(pathVal) {
		have[filepath.Clean(d)] = true
	}
	for _, e := range entries {
		if rest, ok := strings.CutPrefix(e, "~/"); ok {
			if home == "" {
				continue
			}
			e = filepath.Join(home, rest)
		}
		if !filepath.IsAbs(e) {
			continue
		}
		dir := filepath.Clean(e)
		if strings.ContainsAny(e, "*?[") {
			m, _ := filepath.Glob(e)
			if len(m) == 0 {
				continue
			}
			sort.Slice(m, func(i, j int) bool { return versionLess(m[j], m[i]) })
			dir = m[0]
		}
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() || have[dir] {
			continue
		}
		have[dir] = true
		if pathVal == "" {
			pathVal = dir
		} else {
			pathVal += ":" + dir
		}
	}
	return pathVal
}

// versionLess compares strings by their embedded number sequences.
func versionLess(a, b string) bool {
	na, nb := numbers(a), numbers(b)
	for i := 0; i < len(na) && i < len(nb); i++ {
		if na[i] != nb[i] {
			return na[i] < nb[i]
		}
	}
	if len(na) != len(nb) {
		return len(na) < len(nb)
	}
	return a < b
}

func numbers(s string) []int {
	var out []int
	n, in := 0, false
	for _, r := range s {
		if r >= '0' && r <= '9' {
			n, in = n*10+int(r-'0'), true
			continue
		}
		if in {
			out = append(out, n)
			n, in = 0, false
		}
	}
	if in {
		out = append(out, n)
	}
	return out
}
