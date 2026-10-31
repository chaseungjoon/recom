package agent

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"

	"github.com/chaseungjoon/recom/internal/policy"
	"github.com/chaseungjoon/recom/internal/protocol"
)

type prepared struct {
	path    string
	argv    []string
	dir     string
	env     envMap
	collect bool
	filters *filters
	warn    string
}

// prepare resolves the working directory, environment and executable of a
// request without running anything.
func (s *server) prepare(r *protocol.ExecRequest) (*prepared, error) {
	if len(r.Argv) == 0 || r.Argv[0] == "" {
		return nil, perr(protocol.ErrBadRequest, "empty command")
	}
	if s.replica == "" {
		return nil, perr(protocol.ErrBadRequest, "no replica selected")
	}
	p := &prepared{}
	replica := s.replicaPath
	switch {
	case r.Cwd == "~":
		p.dir = s.home
		replica = ""
	case !s.replicaExists():
		return nil, perr(protocol.ErrNoReplica, "the project has no replica on this backend yet; run once without --no-sync to create it")
	default:
		rel, err := CleanRel(r.Cwd)
		if err != nil {
			return nil, perr(protocol.ErrBadRequest, "%v", err)
		}
		dir, err := insideNoSymlink(s.replicaPath, rel, false)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, &protocol.Error{Code: protocol.ErrExec, ExitCode: 127, Message: fmt.Sprintf("working directory %q does not exist on the backend (is it excluded from sync?)", rel)}
			}
			return nil, perr(protocol.ErrBadRequest, "working directory: %v", err)
		}
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			return nil, &protocol.Error{Code: protocol.ErrExec, ExitCode: 127, Message: fmt.Sprintf("working directory %q is not a directory on the backend", rel)}
		}
		p.dir = dir
	}
	p.env, p.warn = envForRequest(r, replica)
	if replica != "" {
		p.env["RECOM_PROJECT_ROOT"] = replica
	}
	if r.TTY {
		term := r.Term
		if term == "" {
			term = "xterm-256color"
		}
		p.env["TERM"] = term
	}
	argv := append([]string{}, r.Argv...)
	if r.Shell {
		sh := p.env["SHELL"]
		if sh == "" || !filepath.IsAbs(sh) {
			sh = "/bin/sh"
		}
		argv = []string{sh, "-c", r.Argv[0]}
	}
	argv = append(append([]string{}, r.Wrapper...), argv...)
	path, err := lookPath(argv[0], p.env["PATH"], p.dir)
	if err != nil {
		var le *lookupError
		if errors.As(err, &le) {
			return nil, &protocol.Error{Code: protocol.ErrExec, ExitCode: le.code, Message: le.msg}
		}
		return nil, err
	}
	p.path, p.argv = path, argv
	p.collect = s.haveLease() && !r.Policy.NoCollection && r.Cwd != "~"
	if p.collect {
		if r.Policy.HashLimit == 0 {
			r.Policy.HashLimit = policy.DefaultHashLimit
		}
		p.filters, err = newFilters(r.Policy)
		if err != nil {
			return nil, perr(protocol.ErrBadRequest, "%v", err)
		}
	}
	return p, nil
}

func signalByName(n string) (syscall.Signal, bool) {
	switch strings.ToUpper(strings.TrimPrefix(strings.ToUpper(n), "SIG")) {
	case "INT":
		return syscall.SIGINT, true
	case "TERM":
		return syscall.SIGTERM, true
	case "HUP":
		return syscall.SIGHUP, true
	case "QUIT":
		return syscall.SIGQUIT, true
	case "KILL":
		return syscall.SIGKILL, true
	case "USR1":
		return syscall.SIGUSR1, true
	case "USR2":
		return syscall.SIGUSR2, true
	}
	return 0, false
}

func signalName(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGHUP:
		return "SIGHUP"
	case syscall.SIGKILL:
		return "SIGKILL"
	case syscall.SIGQUIT:
		return "SIGQUIT"
	case syscall.SIGSEGV:
		return "SIGSEGV"
	case syscall.SIGABRT:
		return "SIGABRT"
	case syscall.SIGPIPE:
		return "SIGPIPE"
	case syscall.SIGBUS:
		return "SIGBUS"
	}
	return fmt.Sprintf("signal %d", int(sig))
}

func exitStatus(ps *os.ProcessState) (int, string) {
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal()), signalName(ws.Signal())
	}
	return ps.ExitCode(), ""
}

// preRun takes the baseline snapshot and writes a "running" manifest plus the
// persisted snapshot, so that a crashed helper can be recovered later.
func (s *server) preRun(runID, kind string, r *protocol.ExecRequest, p *prepared) (*Snapshot, hashCache, *Manifest, error) {
	m := &Manifest{
		RunID: runID, Kind: kind, ReplicaID: s.replica, Command: r.Display, Cwd: r.Cwd,
		Client: r.Client, Started: time.Now().UTC(), State: protocol.JobRunning,
	}
	if !p.collect {
		return nil, nil, m, nil
	}
	stateDir := s.layout.State(s.replica)
	cache := loadHashCache(stateDir)
	pre, err := takeSnapshot(s.replicaPath, p.filters, cache, r.Policy.HashLimit)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("pre-run snapshot: %w", err)
	}
	pre.Policy = r.Policy
	if err := cache.save(stateDir, pre); err != nil {
		return nil, nil, nil, fmt.Errorf("save hash cache: %w", err)
	}
	if err := saveSnapshot(filepath.Join(s.layout.Runs(s.replica), runID+".pre"), pre); err != nil {
		return nil, nil, nil, fmt.Errorf("save snapshot: %w", err)
	}
	if err := withMutex(stateDir, func() error { return saveManifest(s.layout, m) }); err != nil {
		return nil, nil, nil, err
	}
	return pre, cache, m, nil
}

// finishRun computes outputs against pre and persists the final manifest.
func finishRun(l Layout, replica, replicaPath string, m *Manifest, pre *Snapshot, f *filters, pol protocol.OutputPolicy, cache hashCache) (diffResult, error) {
	var res diffResult
	stateDir := l.State(replica)
	if pre != nil {
		if cache == nil {
			cache = loadHashCache(stateDir)
		}
		post, err := takeSnapshot(replicaPath, f, cache, 0)
		if err != nil {
			m.Note = "post-run snapshot failed: " + err.Error()
		} else {
			res = diffSnapshots(replicaPath, m.RunID, pre, post, f, pol, cache)
			m.Outputs = res.Outputs
			m.Deleted = res.Deleted
			_ = cache.save(stateDir, post)
		}
	}
	if m.Ended.IsZero() {
		m.Ended = time.Now().UTC()
	}
	err := withMutex(stateDir, func() error { return recordOutputs(l, m) })
	os.Remove(filepath.Join(l.Runs(replica), m.RunID+".pre"))
	return res, err
}

func (s *server) execForeground(r *protocol.ExecRequest) (*protocol.Response, error) {
	p, err := s.prepare(r)
	if err != nil {
		return nil, err
	}
	if p.warn != "" {
		s.event("warning", p.warn)
	}
	runID := newRunID()
	pre, cache, m, err := s.preRun(runID, "run", r, p)
	if err != nil {
		return nil, err
	}
	p.env["RECOM_RUN_ID"] = runID

	cmd := &exec.Cmd{Path: p.path, Args: p.argv, Dir: p.dir, Env: p.env.list()}
	var (
		stdinW  io.WriteCloser
		ptmx    *os.File
		readers []io.Reader
		kinds   []byte
		closers []io.Closer
	)
	if r.TTY {
		rows, cols := r.Rows, r.Cols
		if rows <= 0 || cols <= 0 {
			rows, cols = 24, 80
		}
		ptmx, err = pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
		if err != nil {
			s.abortRun(m, pre, err)
			return nil, &protocol.Error{Code: protocol.ErrExec, ExitCode: 126, Message: "start: " + err.Error()}
		}
		stdinW = ptmx
		readers, kinds = []io.Reader{ptmx}, []byte{protocol.FStdout}
		closers = []io.Closer{ptmx}
	} else {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		inR, inW, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		outR, outW, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		errR, errW, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, errW
		err = cmd.Start()
		inR.Close()
		outW.Close()
		errW.Close()
		if err != nil {
			inW.Close()
			outR.Close()
			errR.Close()
			s.abortRun(m, pre, err)
			return nil, &protocol.Error{Code: protocol.ErrExec, ExitCode: 126, Message: "start: " + err.Error()}
		}
		stdinW = inW
		readers, kinds = []io.Reader{outR, errR}, []byte{protocol.FStdout, protocol.FStderr}
		closers = []io.Closer{outR, errR}
	}
	pgid := cmd.Process.Pid
	_ = s.out.WriteJSON(protocol.FEvent, protocol.Event{Kind: "started", RunID: runID, Message: "started"})

	pumped := make(chan struct{}, len(readers))
	for i, rd := range readers {
		go func(rd io.Reader, kind byte) {
			defer func() { pumped <- struct{}{} }()
			buf := make([]byte, 32<<10)
			for {
				n, err := rd.Read(buf)
				if n > 0 {
					_ = s.out.Write(kind, buf[:n]) // keep draining even if the client is gone
				}
				if err != nil {
					return
				}
			}
		}(rd, kinds[i])
	}

	stdinCh := make(chan []byte, 1024)
	stdinDone := make(chan struct{})
	go func() {
		defer close(stdinDone)
		for b := range stdinCh {
			if b == nil {
				if !r.TTY {
					stdinW.Close()
				}
				return
			}
			if _, err := stdinW.Write(b); err != nil {
				return
			}
		}
	}()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	lost := false
	stdinClosed := false
	var killTimer <-chan time.Time
	var waitErr error
loop:
	for {
		select {
		case waitErr = <-done:
			break loop
		case <-killTimer:
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			killTimer = nil
		case f, ok := <-s.frames:
			if !ok {
				if !lost {
					lost = true
					_ = syscall.Kill(-pgid, syscall.SIGHUP)
					_ = syscall.Kill(-pgid, syscall.SIGTERM)
					killTimer = time.After(5 * time.Second)
				}
				s.frames = nil // stop selecting a closed channel
				continue
			}
			switch f.Type {
			case protocol.FStdin:
				if !stdinClosed {
					select {
					case stdinCh <- f.Payload:
					case waitErr = <-done:
						break loop
					}
				}
			case protocol.FStdinEOF:
				if !stdinClosed {
					stdinClosed = true
					stdinCh <- nil
				}
			case protocol.FSignal:
				var sg protocol.Signal
				if protocol.Decode(f, &sg) == nil {
					if sig, ok := signalByName(sg.Name); ok {
						_ = syscall.Kill(-pgid, sig)
					}
				}
			case protocol.FResize:
				var rz protocol.Resize
				if ptmx != nil && protocol.Decode(f, &rz) == nil && rz.Rows > 0 && rz.Cols > 0 {
					_ = pty.Setsize(ptmx, &pty.Winsize{Rows: uint16(rz.Rows), Cols: uint16(rz.Cols)})
				}
			}
		}
	}
	if !stdinClosed {
		close(stdinCh)
	}
	if !r.TTY {
		stdinW.Close()
	}
	// Drain output; do not wait forever for background children holding pipes.
	deadline := time.After(2 * time.Second)
	for i := 0; i < len(readers); i++ {
		select {
		case <-pumped:
		case <-deadline:
			i = len(readers)
		}
	}
	for _, c := range closers {
		c.Close()
	}
	// Hang up anything the command left behind in its process group, like a
	// terminal would; nohup'd or setsid'd processes are unaffected.
	_ = syscall.Kill(-pgid, syscall.SIGHUP)

	code, sig := 0, ""
	if cmd.ProcessState != nil {
		code, sig = exitStatus(cmd.ProcessState)
	} else if waitErr != nil {
		code = 255
	}
	m.ExitCode = &code
	m.Signal = sig
	m.State = protocol.JobExited
	if lost {
		m.Note = "client disconnected; command was terminated"
	}
	res, err := finishRun(s.layout, s.replica, s.replicaPath, m, pre, p.filters, r.Policy, cache)
	exit := &protocol.ExitResult{RunID: runID, ExitCode: code, Signal: sig, Lost: lost, Outputs: res.Outputs, Deleted: res.Deleted}
	if err != nil {
		exit.Warnings = append(exit.Warnings, "could not record outputs: "+err.Error())
	}
	if !p.collect && r.Cwd != "~" {
		exit.Warnings = append(exit.Warnings, "outputs were not tracked for this run")
	}
	return &protocol.Response{Exit: exit}, nil
}

// abortRun records a run whose command never started.
func (s *server) abortRun(m *Manifest, pre *Snapshot, err error) {
	if m == nil {
		return
	}
	m.State = protocol.JobFailed
	m.Note = err.Error()
	m.Ended = time.Now().UTC()
	_ = withMutex(s.layout.State(s.replica), func() error { return recordOutputs(s.layout, m) })
	os.Remove(filepath.Join(s.layout.Runs(s.replica), m.RunID+".pre"))
}
