// Package remote is the client side of the helper protocol: it starts the
// helper over SSH (installing or updating it when needed), performs the
// handshake and exchanges framed requests.
package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/chaseungjoon/recom/internal/buildinfo"
	"github.com/chaseungjoon/recom/internal/helperbin"
	"github.com/chaseungjoon/recom/internal/protocol"
	"github.com/chaseungjoon/recom/internal/sshx"
)

// TransportError is an SSH-level failure (host unreachable, auth, dropped).
type TransportError struct {
	Msg    string
	Stderr string
}

func (e *TransportError) Error() string {
	s := e.Msg
	if d := lastLine(e.Stderr); d != "" {
		s += ": " + d
	}
	return s
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

var errHelperMissing = errors.New("helper missing")

// Options configure a connection.
type Options struct {
	Verbose bool
	Log     func(format string, args ...any)
	OnEvent func(protocol.Event)
}

func (o Options) logf(format string, args ...any) {
	if o.Verbose && o.Log != nil {
		o.Log(format, args...)
	}
}

// Conn is a live helper session.
type Conn struct {
	Target sshx.Target
	Ack    protocol.Ack
	Noise  []byte
	Helper *helperbin.Helper

	opts   Options
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	w      *protocol.Writer
	r      *protocol.Reader
	stderr *syncBuffer
	closed bool
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.buf.Len() < 64<<10 {
		b.buf.Write(p)
	}
	return len(p), nil
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// HelperPath returns the backend path for helper h.
func HelperPath(root string, h *helperbin.Helper) string {
	return path.Join(root, "bin", h.Name())
}

// Dial connects to the backend and starts a helper session, installing the
// helper first if this exact build is not present yet.
func Dial(ctx context.Context, t sshx.Target, opts Options) (*Conn, error) {
	h, err := helperbin.For(t.Host.OS, t.Host.Arch)
	if err != nil {
		return nil, err
	}
	hp := HelperPath(t.Host.RemoteRoot, h)
	c, err := dialOnce(ctx, t, hp, h, opts)
	if errors.Is(err, errHelperMissing) {
		opts.logf("installing helper %s (%s, %d bytes) on %s", h.Name(), h.Source, len(h.Data), t.Name)
		if err := Install(ctx, t, h); err != nil {
			return nil, err
		}
		c, err = dialOnce(ctx, t, hp, h, opts)
	}
	if err != nil {
		return nil, err
	}
	if c.Ack.HelperSHA256 != h.SHA256 {
		// The file under our content-addressed name is not what we uploaded
		// (damaged or replaced). Reinstall once, then verify again.
		got := c.Ack.HelperSHA256
		c.Abort()
		if opts.OnEvent != nil {
			opts.OnEvent(protocol.Event{Kind: "warning", Message: fmt.Sprintf("helper on %s failed verification (sha256 %.16s, expected %.16s); reinstalling it", t.Name, got, h.SHA256)})
		}
		if err := Install(ctx, t, h); err != nil {
			return nil, err
		}
		if c, err = dialOnce(ctx, t, hp, h, opts); err != nil {
			return nil, err
		}
		if c.Ack.HelperSHA256 != h.SHA256 {
			c.Abort()
			return nil, fmt.Errorf("helper integrity check failed on %s: expected sha256 %.16s, got %.16s (refusing to continue)", t.Name, h.SHA256, c.Ack.HelperSHA256)
		}
	}
	return c, nil
}

func dialOnce(ctx context.Context, t sshx.Target, hp string, h *helperbin.Helper, opts Options) (*Conn, error) {
	cmd := t.Command(context.WithoutCancel(ctx), false, sshx.Quote(hp)+" __agent serve")
	opts.logf("ssh %s", strings.Join(cmd.Args[1:], " "))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	eb := &syncBuffer{}
	cmd.Stderr = eb
	if err := cmd.Start(); err != nil {
		return nil, &TransportError{Msg: "cannot start ssh (" + sshx.Binary() + ")", Stderr: err.Error()}
	}
	c := &Conn{Target: t, Helper: h, opts: opts, cmd: cmd, stdin: stdin, w: protocol.NewWriter(stdin), r: protocol.NewReader(stdout), stderr: eb}
	_ = c.w.WriteJSON(protocol.FHello, protocol.Hello{Protocol: protocol.Version, ClientVersion: buildinfo.Version, Root: t.Host.RemoteRoot})

	type result struct {
		noise []byte
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		n, err := c.r.SkipToMagic()
		ch <- result{n, err}
	}()
	var res result
	select {
	case res = <-ch:
	case <-ctx.Done():
		c.Abort()
		return nil, ctx.Err()
	}
	if res.err != nil {
		werr := cmd.Wait()
		code := -1
		var ee *exec.ExitError
		if errors.As(werr, &ee) {
			code = ee.ExitCode()
		}
		se := eb.String()
		if code == 127 || code == 126 || strings.Contains(se, "No such file or directory") || strings.Contains(se, "not found") {
			return nil, errHelperMissing
		}
		if code == 255 || code == -1 {
			return nil, &TransportError{Msg: "cannot connect to " + t.Name + " (" + t.Host.Target + ")", Stderr: se}
		}
		if len(res.noise) > 0 {
			se = string(res.noise) + se
		}
		return nil, &TransportError{Msg: fmt.Sprintf("helper failed to start on %s (exit %d)", t.Name, code), Stderr: se}
	}
	c.Noise = res.noise
	if len(res.noise) > 0 {
		opts.logf("ignored %d bytes printed by the remote shell before the helper started", len(res.noise))
	}
	f, err := c.r.ReadFrame()
	if err != nil {
		c.Abort()
		return nil, &TransportError{Msg: "helper handshake failed", Stderr: eb.String()}
	}
	if f.Type == protocol.FResponse {
		var r protocol.Response
		_ = protocol.Decode(f, &r)
		c.Abort()
		if r.Error != nil {
			return nil, fmt.Errorf("helper refused session: %s", r.Error.Message)
		}
		return nil, errors.New("helper refused session")
	}
	if f.Type != protocol.FAck || protocol.Decode(f, &c.Ack) != nil {
		c.Abort()
		return nil, errors.New("helper handshake: malformed acknowledgement")
	}
	if c.Ack.Protocol != protocol.Version {
		c.Abort()
		return nil, fmt.Errorf("helper protocol v%d is incompatible with this client (v%d)", c.Ack.Protocol, protocol.Version)
	}
	return c, nil
}

// Install uploads a helper to the backend's private bin directory. The file
// name is content-addressed, so existing files are never overwritten with
// different content and no path outside the recom root is touched.
func Install(ctx context.Context, t sshx.Target, h *helperbin.Helper) error {
	dir := path.Join(t.Host.RemoteRoot, "bin")
	final := path.Join(dir, h.Name())
	tmp := path.Join(dir, fmt.Sprintf(".upload-%d", time.Now().UnixNano()))
	script := fmt.Sprintf("umask 077 && mkdir -p %s && cat > %s && chmod 700 %s && mv -f %s %s",
		sshx.Quote(dir), sshx.Quote(tmp), sshx.Quote(tmp), sshx.Quote(tmp), sshx.Quote(final))
	cmd := t.Command(ctx, false, script)
	cmd.Stdin = bytes.NewReader(h.Data)
	var eb bytes.Buffer
	cmd.Stderr = &eb
	cmd.Stdout = io.Discard
	if err := cmd.Run(); err != nil {
		return &TransportError{Msg: "could not install the recom helper on " + t.Name, Stderr: eb.String()}
	}
	return nil
}

// Call performs one request. Data frames (logs) go to stdout/stderr.
func (c *Conn) Call(req *protocol.Request, stdout, stderr io.Writer) (*protocol.Response, error) {
	if err := c.w.WriteJSON(protocol.FRequest, req); err != nil {
		return nil, c.transportErr(err)
	}
	for {
		f, err := c.r.ReadFrame()
		if err != nil {
			return nil, c.transportErr(err)
		}
		switch f.Type {
		case protocol.FEvent:
			var ev protocol.Event
			if protocol.Decode(f, &ev) == nil && c.opts.OnEvent != nil {
				c.opts.OnEvent(ev)
			}
		case protocol.FStdout:
			if stdout != nil {
				_, _ = stdout.Write(f.Payload)
			}
		case protocol.FStderr:
			if stderr != nil {
				_, _ = stderr.Write(f.Payload)
			}
		case protocol.FResponse:
			var r protocol.Response
			if err := protocol.Decode(f, &r); err != nil {
				return nil, err
			}
			if r.Error != nil {
				return nil, r.Error
			}
			return &r, nil
		default:
			return nil, fmt.Errorf("protocol: unexpected frame %q", f.Type)
		}
	}
}

func (c *Conn) transportErr(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.ErrClosedPipe) {
		return &TransportError{Msg: "connection to " + c.Target.Name + " lost", Stderr: c.stderr.String()}
	}
	return err
}

// Close ends the session gracefully (releasing the replica lease).
func (c *Conn) Close() {
	if c == nil || c.closed {
		return
	}
	c.closed = true
	done := make(chan struct{})
	go func() {
		defer close(done)
		if c.w.WriteJSON(protocol.FRequest, protocol.Request{Op: protocol.OpClose}) == nil {
			for {
				f, err := c.r.ReadFrame()
				if err != nil || f.Type == protocol.FResponse {
					break
				}
			}
		}
		c.stdin.Close()
		_ = c.cmd.Wait()
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		c.Abort()
	}
}

// Abort kills the session without a graceful close.
func (c *Conn) Abort() {
	if c == nil {
		return
	}
	c.closed = true
	c.stdin.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	go func() { _ = c.cmd.Wait() }()
}

// Send writes a raw frame (stdin data, signals, resizes).
func (c *Conn) Send(t byte, payload []byte) error { return c.w.Write(t, payload) }

// SendJSON writes a JSON frame.
func (c *Conn) SendJSON(t byte, v any) error { return c.w.WriteJSON(t, v) }

// ExecIO wires a remote command to local streams.
type ExecIO struct {
	Stdin  io.Reader // nil: remote stdin is closed immediately
	Stdout io.Writer
	Stderr io.Writer
	// OnStart is called once the command is running.
	OnStart func(runID string)
}

// Exec runs a command and streams its I/O until it exits.
func (c *Conn) Exec(req *protocol.ExecRequest, eio ExecIO) (*protocol.ExitResult, error) {
	if err := c.w.WriteJSON(protocol.FRequest, protocol.Request{Op: protocol.OpExec, Exec: req}); err != nil {
		return nil, c.transportErr(err)
	}
	started := false
	startStdin := func() {
		if eio.Stdin == nil {
			_ = c.w.Write(protocol.FStdinEOF, nil)
			return
		}
		go func() {
			buf := make([]byte, 32<<10)
			for {
				n, err := eio.Stdin.Read(buf)
				if n > 0 {
					if c.w.Write(protocol.FStdin, append([]byte(nil), buf[:n]...)) != nil {
						return
					}
				}
				if err != nil {
					_ = c.w.Write(protocol.FStdinEOF, nil)
					return
				}
			}
		}()
	}
	for {
		f, err := c.r.ReadFrame()
		if err != nil {
			return nil, c.transportErr(err)
		}
		switch f.Type {
		case protocol.FStdout:
			_, _ = eio.Stdout.Write(f.Payload)
		case protocol.FStderr:
			_, _ = eio.Stderr.Write(f.Payload)
		case protocol.FEvent:
			var ev protocol.Event
			if protocol.Decode(f, &ev) != nil {
				continue
			}
			if ev.Kind == "started" && !started {
				started = true
				if eio.OnStart != nil {
					eio.OnStart(ev.RunID)
				}
				if !req.Detach {
					startStdin()
				}
				continue
			}
			if c.opts.OnEvent != nil {
				c.opts.OnEvent(ev)
			}
		case protocol.FResponse:
			var r protocol.Response
			if err := protocol.Decode(f, &r); err != nil {
				return nil, err
			}
			if r.Error != nil {
				return nil, r.Error
			}
			if r.Exit == nil {
				return nil, errors.New("protocol: exec response without result")
			}
			return r.Exit, nil
		}
	}
}
