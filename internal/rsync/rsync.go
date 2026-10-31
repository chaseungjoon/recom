// Package rsync drives the local rsync binary for incremental uploads and
// output downloads over SSH. It relies only on rsync's documented options
// (filters, --files-from, --from0, itemized --out-format), never on parsing
// localized human-readable progress.
package rsync

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/chaseungjoon/recom/internal/sshx"
)

// Binary describes a usable local rsync.
type Binary struct {
	Path    string
	Version string
	Nanos   bool // supports nanosecond mtime comparison
}

// required options; openrsync and very old rsync builds lack some of them.
var required = []string{"--files-from", "--from0", "--safe-links", "--exclude-from", "--out-format", "--chmod", "--partial"}

// ErrUnsupported indicates an rsync without the required options.
var ErrUnsupported = errors.New("unsupported rsync")

func candidates() []string {
	if p := os.Getenv("RECOM_RSYNC"); p != "" {
		return []string{p}
	}
	var c []string
	if runtime.GOOS == "darwin" {
		// Prefer Homebrew's GNU rsync over macOS's bundled openrsync/rsync 2.6.9.
		c = append(c, "/opt/homebrew/bin/rsync", "/usr/local/bin/rsync")
	}
	return append(c, "rsync")
}

// Detect finds a local rsync supporting everything recom needs.
func Detect() (*Binary, error) {
	var lastErr error
	for _, c := range candidates() {
		p, err := exec.LookPath(c)
		if err != nil {
			lastErr = err
			continue
		}
		b, err := probe(p)
		if err != nil {
			lastErr = err
			continue
		}
		return b, nil
	}
	hint := "install rsync 3.x (Ubuntu: sudo apt install rsync)"
	if runtime.GOOS == "darwin" {
		hint = "install a current rsync with `brew install rsync` (macOS ships openrsync/rsync 2.6.9)"
	}
	return nil, fmt.Errorf("no usable local rsync: %v; %s", lastErr, hint)
}

func probe(p string) (*Binary, error) {
	out, err := exec.Command(p, "--version").CombinedOutput()
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("%s --version: %w", p, err)
	}
	first := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	help, _ := exec.Command(p, "--help").CombinedOutput()
	for _, opt := range required {
		if !bytes.Contains(help, []byte(opt)) {
			return nil, fmt.Errorf("%w: %s (%s) lacks %s", ErrUnsupported, p, first, opt)
		}
	}
	b := &Binary{Path: p, Version: first}
	// rsync >= 3.1.3 compares nanosecond mtimes with --modify-window=-1; the
	// default whole-second comparison can miss a same-size edit made within
	// the same second as the previous one.
	b.Nanos = bytes.Contains(help, []byte("--modify-window")) && versionAtLeast(first, 3, 1, 3)
	return b, nil
}

var versionRe = regexp.MustCompile(`version\s+(\d+)\.(\d+)\.(\d+)`)

func versionAtLeast(s string, maj, min, patch int) bool {
	m := versionRe.FindStringSubmatch(s)
	if m == nil || strings.Contains(s, "openrsync") {
		return false
	}
	v := [3]int{}
	for i := range v {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	want := [3]int{maj, min, patch}
	for i := range v {
		if v[i] != want[i] {
			return v[i] > want[i]
		}
	}
	return true
}

// UploadStats summarizes an upload.
type UploadStats struct {
	Files int
	Bytes int64
	Names []string
}

// Upload syncs localRoot/ to remoteDir/ without deleting anything remotely.
// rules is an rsync filter list ("+ pat"/"- pat"), written NUL-separated.
func (b *Binary) Upload(ctx context.Context, t sshx.Target, localRoot, remoteDir string, rules []string, stderr io.Writer, dryRun bool) (*UploadStats, error) {
	ff, err := writeList(rules)
	if err != nil {
		return nil, err
	}
	defer os.Remove(ff)
	args := []string{
		"-rlpt", "--safe-links", "--from0", "--exclude-from=" + ff,
		"--chmod=Du+rwx,Fu+rw",
		"--out-format=%i|%l|%n",
		"-e", t.RsyncShell(),
	}
	if b.Nanos {
		args = append(args, "--modify-window=-1")
	}
	if dryRun {
		args = append(args, "--dry-run")
	}
	args = append(args, strings.TrimRight(localRoot, "/")+"/", t.Remote(strings.TrimRight(remoteDir, "/")+"/"))
	cmd := exec.CommandContext(ctx, b.Path, args...)
	var out bytes.Buffer
	var errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = io.MultiWriter(&errBuf, stderr)
	if err := cmd.Run(); err != nil {
		return nil, describe("upload", err, errBuf.String())
	}
	st := &UploadStats{}
	sc := bufio.NewScanner(&out)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), "|", 3)
		if len(parts) != 3 || len(parts[0]) < 2 {
			continue
		}
		if parts[0][0] == '<' && parts[0][1] == 'f' {
			st.Files++
			n, _ := strconv.ParseInt(parts[1], 10, 64)
			st.Bytes += n
			st.Names = append(st.Names, parts[2])
		}
	}
	return st, nil
}

// Download fetches the listed paths (relative to remoteDir) into localDir,
// keeping partially transferred files there so an interrupted download
// resumes. It never touches files outside localDir.
func (b *Binary) Download(ctx context.Context, t sshx.Target, remoteDir, localDir string, paths []string, stderr io.Writer) error {
	ff, err := writeList(paths)
	if err != nil {
		return err
	}
	defer os.Remove(ff)
	args := []string{
		"-t", "-p", "--chmod=Fu+rw,Fgo-w,Fa-s,Du+rwx", "--partial", "--from0", "--files-from=" + ff,
		"-e", t.RsyncShell(),
		t.Remote(strings.TrimRight(remoteDir, "/") + "/"), strings.TrimRight(localDir, "/") + "/",
	}
	cmd := exec.CommandContext(ctx, b.Path, args...)
	var errBuf bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = io.MultiWriter(&errBuf, stderr)
	if err := cmd.Run(); err != nil {
		return describe("download", err, errBuf.String())
	}
	return nil
}

func writeList(items []string) (string, error) {
	f, err := os.CreateTemp("", "recom-rsync-*")
	if err != nil {
		return "", err
	}
	for _, it := range items {
		if strings.ContainsRune(it, 0) {
			f.Close()
			os.Remove(f.Name())
			return "", fmt.Errorf("path contains NUL")
		}
		f.WriteString(it)
		f.Write([]byte{0})
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// Error is an rsync failure.
type Error struct {
	Op     string
	Code   int
	Detail string
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("rsync %s failed (exit %d", e.Op, e.Code)
	if m, ok := exitMeaning[e.Code]; ok {
		msg += ": " + m
	}
	msg += ")"
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

var exitMeaning = map[int]string{
	1: "syntax or usage error", 2: "protocol incompatibility", 3: "errors selecting files",
	5: "error starting client-server protocol", 10: "error in socket I/O", 11: "error in file I/O (disk full?)",
	12: "error in rsync protocol data stream (remote shell printed text? see `recom doctor`)",
	20: "interrupted", 23: "partial transfer due to error", 24: "some source files vanished",
	30: "timeout", 35: "timeout waiting for daemon", 255: "SSH connection failed",
}

func describe(op string, err error, stderr string) error {
	var ee *exec.ExitError
	code := -1
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	}
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	detail := ""
	if len(lines) > 0 {
		detail = strings.TrimSpace(lines[len(lines)-1])
	}
	if code < 0 {
		return fmt.Errorf("rsync %s: %w", op, err)
	}
	return &Error{Op: op, Code: code, Detail: detail}
}
