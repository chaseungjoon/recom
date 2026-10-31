package agent

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"

	"github.com/chaseungjoon/recom/internal/protocol"
)

const tmuxSocket = "recom"

// jobRecord is the durable state of a detached job (jobs/<id>/job.json).
type jobRecord struct {
	ID              string    `json:"id"`
	ReplicaID       string    `json:"replica_id"`
	Command         string    `json:"command"`
	Cwd             string    `json:"cwd"`
	State           string    `json:"state"`
	Started         time.Time `json:"started"`
	Ended           time.Time `json:"ended,omitzero"`
	ExitCode        *int      `json:"exit_code,omitempty"`
	Signal          string    `json:"signal,omitempty"`
	Tmux            bool      `json:"tmux"`
	TmuxPath        string    `json:"tmux_path,omitempty"`
	Session         string    `json:"session,omitempty"`
	SupervisorPID   int       `json:"supervisor_pid,omitempty"`
	SupervisorStart uint64    `json:"supervisor_start,omitempty"`
	PID             int       `json:"pid,omitempty"`
	PIDStart        uint64    `json:"pid_start,omitempty"`
	Message         string    `json:"message,omitempty"`
	Collect         bool      `json:"collect"`
}

// jobSpec holds what the supervisor needs to start the command. It may hold
// secrets (argv, env) and is deleted as soon as the supervisor has read it.
type jobSpec struct {
	Path    string                `json:"path"`
	Argv    []string              `json:"argv"`
	Env     []string              `json:"env"`
	Dir     string                `json:"dir"`
	Root    string                `json:"root"`
	Replica string                `json:"replica"`
	Policy  protocol.OutputPolicy `json:"policy"`
	Rows    int                   `json:"rows"`
	Cols    int                   `json:"cols"`
}

func jobFile(l Layout, replica, id string) string {
	return filepath.Join(l.Job(replica, id), "job.json")
}

func loadJob(l Layout, replica, id string) (*jobRecord, error) {
	var j jobRecord
	if err := readJSON(jobFile(l, replica, id), &j); err != nil {
		return nil, err
	}
	return &j, nil
}

func saveJob(l Layout, j *jobRecord) error {
	return writeJSONAtomic(jobFile(l, j.ReplicaID, j.ID), j)
}

func alivePID(pid int, start uint64) bool {
	if pid <= 0 {
		return false
	}
	st, ok := procStart(pid)
	return ok && (start == 0 || st == start)
}

// effectiveState refines a recorded state with process liveness.
func effectiveState(j *jobRecord) string {
	switch j.State {
	case protocol.JobRunning:
		if alivePID(j.SupervisorPID, j.SupervisorStart) || alivePID(j.PID, j.PIDStart) {
			return protocol.JobRunning
		}
		return protocol.JobLost
	case protocol.JobStarting:
		if time.Since(j.Started) < launchGrace {
			return protocol.JobStarting
		}
		return protocol.JobLost
	}
	return j.State
}

// holderAlive extends leaseAlive: a job whose supervisor died while its
// command is still running keeps the replica busy.
func holderAlive(stateDir string, h *protocol.LeaseInfo) bool {
	if leaseAlive(h) {
		return true
	}
	if h.Kind != "job" || !ValidJobID(h.ID) {
		return false
	}
	var j jobRecord
	if readJSON(filepath.Join(stateDir, "jobs", h.ID, "job.json"), &j) != nil {
		return false
	}
	return alivePID(j.PID, j.PIDStart)
}

func toInfo(l Layout, j *jobRecord, pending int) protocol.JobInfo {
	info := protocol.JobInfo{
		ID: j.ID, ReplicaID: j.ReplicaID, State: effectiveState(j), Command: j.Command, Cwd: j.Cwd,
		Started: j.Started, Ended: j.Ended, ExitCode: j.ExitCode, Signal: j.Signal,
		Tmux: j.Tmux, TmuxPath: j.TmuxPath, Session: j.Session, PID: j.PID, Pending: pending, Message: j.Message,
	}
	if j.Tmux {
		info.Socket = tmuxSocket
	}
	return info
}

func listJobs(l Layout, replica string) []protocol.JobInfo {
	ents, _ := os.ReadDir(l.Jobs(replica))
	pend := map[string]int{}
	outs, _ := pendingOutputs(l, replica)
	for _, o := range outs {
		pend[o.RunID]++
	}
	var out []protocol.JobInfo
	for _, e := range ents {
		if !e.IsDir() || !ValidJobID(e.Name()) {
			continue
		}
		j, err := loadJob(l, replica, e.Name())
		if err != nil {
			out = append(out, protocol.JobInfo{ID: e.Name(), ReplicaID: replica, State: "corrupt", Message: err.Error()})
			continue
		}
		out = append(out, toInfo(l, j, pend[j.ID]))
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Started.Before(out[k].Started) })
	return out
}

// resolveJob accepts a full job id or a unique prefix.
func resolveJob(l Layout, replica, id string) (string, error) {
	id = strings.TrimSpace(id)
	if len(id) < 3 || strings.ContainsAny(id, "/.") {
		return "", perr(protocol.ErrBadRequest, "invalid job id %q", id)
	}
	ents, _ := os.ReadDir(l.Jobs(replica))
	var matches []string
	for _, e := range ents {
		if e.Name() == id {
			return id, nil
		}
		if strings.HasPrefix(e.Name(), id) {
			matches = append(matches, e.Name())
		}
	}
	switch len(matches) {
	case 0:
		return "", perr(protocol.ErrNotFound, "no job %q in this project on this backend (see `recom jobs`)", id)
	case 1:
		return matches[0], nil
	}
	return "", perr(protocol.ErrBadRequest, "job id %q is ambiguous (%s)", id, strings.Join(matches, ", "))
}

func (s *server) jobInfo(id string) (*protocol.JobInfo, error) {
	full, err := resolveJob(s.layout, s.replica, id)
	if err != nil {
		return nil, err
	}
	j, err := loadJob(s.layout, s.replica, full)
	if err != nil {
		return nil, err
	}
	n := 0
	outs, _ := pendingOutputs(s.layout, s.replica)
	for _, o := range outs {
		if o.RunID == full {
			n++
		}
	}
	info := toInfo(s.layout, j, n)
	if info.Tmux && info.State == protocol.JobRunning && j.TmuxPath != "" {
		// The tmux server reads the user's ~/.tmux.conf; report the real prefix.
		out, err := exec.Command(j.TmuxPath, "-L", tmuxSocket, "show-options", "-gv", "prefix").Output()
		if err == nil {
			info.Prefix = strings.TrimSpace(string(out))
		}
	}
	return &info, nil
}

// launchJob starts a detached job. The session's lease is handed to the job,
// which holds it until its supervisor has recorded the final manifest.
func (s *server) launchJob(r *protocol.ExecRequest) (*protocol.Response, error) {
	if !s.haveLease() {
		return nil, perr(protocol.ErrBusy, "detached jobs need the replica lease")
	}
	if r.Cwd == "~" {
		return nil, perr(protocol.ErrNoReplica, "detached jobs need a project replica; run once without --no-sync first")
	}
	p, err := s.prepare(r)
	if err != nil {
		return nil, err
	}
	if p.warn != "" {
		s.event("warning", p.warn)
	}
	useTmux := false
	tmuxPath := ""
	if r.Tmux != "never" {
		if tp, err := lookPath("tmux", p.env["PATH"], p.dir); err == nil {
			useTmux, tmuxPath = true, tp
		} else if r.Tmux == "always" {
			return nil, &protocol.Error{Code: protocol.ErrExec, ExitCode: 127, Message: "tmux is not installed on the backend (needed for --detach with tmux=always)"}
		}
	}
	id := newRunID()
	p.env["RECOM_RUN_ID"] = id
	p.env["RECOM_JOB_ID"] = id
	jobDir := s.layout.Job(s.replica, id)
	if err := os.MkdirAll(jobDir, 0o700); err != nil {
		return nil, err
	}
	pre, _, m, err := s.preRun(id, "job", r, p)
	if err != nil {
		return nil, err
	}
	_ = pre
	j := &jobRecord{
		ID: id, ReplicaID: s.replica, Command: r.Display, Cwd: r.Cwd, State: protocol.JobStarting,
		Started: m.Started, Tmux: useTmux, TmuxPath: tmuxPath, Collect: p.collect,
	}
	if useTmux {
		j.Session = "recom-" + id
	}
	spec := jobSpec{Path: p.path, Argv: p.argv, Env: p.env.list(), Dir: p.dir, Root: s.layout.Root, Replica: s.replica, Policy: r.Policy, Rows: r.Rows, Cols: r.Cols}
	if err := writeJSONAtomic(filepath.Join(jobDir, "spec.json"), spec); err != nil {
		return nil, err
	}
	if err := saveJob(s.layout, j); err != nil {
		return nil, err
	}
	if !p.collect {
		_ = withMutex(s.layout.State(s.replica), func() error { return saveManifest(s.layout, m) })
	}
	// Hand the lease to the job; until the supervisor claims it, this helper's
	// PID keeps it alive.
	stateDir := s.layout.State(s.replica)
	jl := selfLease("job", id, s.owner, r.Display)
	if err := transferLease(stateDir, s.leaseID, jl); err != nil {
		return nil, err
	}
	s.leaseID = id

	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*protocol.Response, error) {
		j.State = protocol.JobFailed
		j.Message = err.Error()
		j.Ended = time.Now().UTC()
		_ = saveJob(s.layout, j)
		m.State = protocol.JobFailed
		m.Note = err.Error()
		_, _ = finishRun(s.layout, s.replica, s.replicaPath, m, nil, nil, r.Policy, nil)
		os.Remove(filepath.Join(jobDir, "spec.json"))
		return nil, &protocol.Error{Code: protocol.ErrExec, ExitCode: 125, Message: "could not launch job: " + err.Error()}
	}
	if useTmux {
		cols, rows := r.Cols, r.Rows
		if cols <= 0 || rows <= 0 {
			cols, rows = 200, 50
		}
		script := shellQuote(self) + " __agent supervise " + shellQuote(s.layout.Root) + " " + s.replica + " " + id
		cmd := exec.Command(tmuxPath, "-L", tmuxSocket, "new-session", "-d", "-s", j.Session,
			"-x", fmt.Sprint(cols), "-y", fmt.Sprint(rows), script)
		cmd.Env = p.env.list()
		cmd.Dir = p.dir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fail(fmt.Errorf("tmux: %v: %s", err, bytes.TrimSpace(out)))
		}
	} else {
		logf, err := os.OpenFile(filepath.Join(jobDir, "supervisor.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return fail(err)
		}
		devnull, _ := os.Open(os.DevNull)
		cmd := exec.Command(self, "__agent", "supervise", s.layout.Root, s.replica, id)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, logf, logf
		cmd.Dir = p.dir
		cmd.Env = p.env.list()
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		err = cmd.Start()
		logf.Close()
		if devnull != nil {
			devnull.Close()
		}
		if err != nil {
			return fail(err)
		}
		_ = cmd.Process.Release()
	}
	// Wait for the supervisor to take over.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		cur, err := loadJob(s.layout, s.replica, id)
		if err == nil && cur.State != protocol.JobStarting {
			if cur.State == protocol.JobFailed {
				s.leaseID = ""
				return nil, &protocol.Error{Code: protocol.ErrExec, ExitCode: 125, Message: "job failed to start: " + cur.Message}
			}
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.leaseID = "" // the job owns the lease now
	return &protocol.Response{Exit: &protocol.ExitResult{RunID: id, JobID: id, Tmux: useTmux}}, nil
}

// supervise runs a detached job to completion and records its outcome. It is
// started by tmux or as a setsid'd process, and never needs the client.
func supervise(root, replica, id string) int {
	signal.Ignore(syscall.SIGHUP) // survive tmux/ssh hangups; the job has its own session
	if ValidateRoot(root) != nil || !ValidReplicaID(replica) || !ValidJobID(id) {
		fmt.Fprintln(os.Stderr, "supervise: invalid arguments")
		return 2
	}
	l := Layout{Root: root}
	stateDir := l.State(replica)
	jobDir := l.Job(replica, id)
	j, err := loadJob(l, replica, id)
	if err != nil {
		fmt.Fprintln(os.Stderr, "supervise:", err)
		return 1
	}
	var spec jobSpec
	specPath := filepath.Join(jobDir, "spec.json")
	if err := readJSON(specPath, &spec); err != nil {
		j.State, j.Message = protocol.JobFailed, err.Error()
		_ = saveJob(l, j)
		return 1
	}
	os.Remove(specPath)

	me := selfLease("job", id, "", j.Command)
	if err := transferLease(stateDir, id, me); err != nil {
		j.State, j.Message = protocol.JobFailed, "lease: "+err.Error()
		j.Ended = time.Now().UTC()
		_ = saveJob(l, j)
		return 1
	}
	defer releaseLease(stateDir, id) //nolint:errcheck

	m, err := loadManifest(l, replica, id)
	if err != nil {
		m = &Manifest{RunID: id, Kind: "job", ReplicaID: replica, Command: j.Command, Cwd: j.Cwd, Started: j.Started}
	}

	cmd := &exec.Cmd{Path: spec.Path, Args: spec.Argv, Dir: spec.Dir, Env: spec.Env}
	var copyDone chan struct{}
	if j.Tmux {
		logf, err := os.OpenFile(filepath.Join(jobDir, "output.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return failJob(l, j, m, err)
		}
		defer logf.Close()
		ws := &pty.Winsize{Rows: uint16(max(spec.Rows, 24)), Cols: uint16(max(spec.Cols, 80))}
		if sz, err := pty.GetsizeFull(os.Stdin); err == nil && sz.Rows > 0 {
			ws = sz
		}
		ptmx, err := pty.StartWithSize(cmd, ws)
		if err != nil {
			return failJob(l, j, m, err)
		}
		defer ptmx.Close()
		if term.IsTerminal(int(os.Stdin.Fd())) {
			if old, err := term.MakeRaw(int(os.Stdin.Fd())); err == nil {
				defer term.Restore(int(os.Stdin.Fd()), old) //nolint:errcheck
			}
		}
		winch := make(chan os.Signal, 1)
		signal.Notify(winch, syscall.SIGWINCH)
		go func() {
			for range winch {
				if sz, err := pty.GetsizeFull(os.Stdin); err == nil {
					_ = pty.Setsize(ptmx, sz)
				}
			}
		}()
		go func() { _, _ = io.Copy(ptmx, os.Stdin) }()
		copyDone = make(chan struct{})
		go func() {
			defer close(copyDone)
			// The log file is authoritative; the tmux pane is best effort, so a
			// killed tmux session never stops logging (or the job).
			_, _ = io.Copy(&teeWriter{log: logf, pane: os.Stdout}, ptmx)
		}()
	} else {
		outf, err1 := os.OpenFile(filepath.Join(jobDir, "stdout.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		errf, err2 := os.OpenFile(filepath.Join(jobDir, "stderr.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err := errors.Join(err1, err2); err != nil {
			return failJob(l, j, m, err)
		}
		defer outf.Close()
		defer errf.Close()
		devnull, _ := os.Open(os.DevNull)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, outf, errf
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			return failJob(l, j, m, err)
		}
	}
	j.State = protocol.JobRunning
	j.SupervisorPID, j.SupervisorStart = me.PID, me.Start
	j.PID = cmd.Process.Pid
	j.PIDStart, _ = procStart(j.PID)
	_ = saveJob(l, j)

	_ = cmd.Wait()
	if copyDone != nil {
		select {
		case <-copyDone:
		case <-time.After(2 * time.Second):
		}
	}
	_ = syscall.Kill(-j.PID, syscall.SIGHUP) // leftovers in the job's process group

	code, sig := exitStatus(cmd.ProcessState)
	j.ExitCode, j.Signal = &code, sig
	j.Ended = time.Now().UTC()
	j.State = protocol.JobExited
	if _, err := os.Stat(filepath.Join(jobDir, "stop-requested")); err == nil {
		j.State = protocol.JobStopped
	}
	m.ExitCode, m.Signal, m.State, m.Ended = &code, sig, j.State, j.Ended
	var pre *Snapshot
	var f *filters
	if j.Collect {
		pre, err = loadSnapshot(filepath.Join(l.Runs(replica), id+".pre"))
		if err != nil {
			m.Note = err.Error()
		}
		f, _ = newFilters(spec.Policy)
	}
	res, err := finishRun(l, replica, l.Replica(replica), m, pre, f, spec.Policy, nil)
	if err != nil {
		j.Message = "could not record outputs: " + err.Error()
	} else if len(res.Outputs) > 0 {
		j.Message = fmt.Sprintf("%d output(s) recorded", len(res.Outputs))
	}
	_ = saveJob(l, j)
	if j.Tmux {
		status := fmt.Sprintf("exit status %d", code)
		if sig != "" {
			status = "killed by " + sig
		}
		fmt.Fprintf(os.Stdout, "\r\n[recom] job %s finished (%s)\r\n", id, status)
	}
	return 0
}

type teeWriter struct {
	log      io.Writer
	pane     io.Writer
	paneDead bool
}

func (w *teeWriter) Write(p []byte) (int, error) {
	if !w.paneDead {
		if _, err := w.pane.Write(p); err != nil {
			w.paneDead = true
		}
	}
	return w.log.Write(p)
}

func failJob(l Layout, j *jobRecord, m *Manifest, err error) int {
	j.State, j.Message = protocol.JobFailed, err.Error()
	j.Ended = time.Now().UTC()
	_ = saveJob(l, j)
	m.State, m.Note, m.Ended = protocol.JobFailed, err.Error(), j.Ended
	_, _ = finishRun(l, j.ReplicaID, l.Replica(j.ReplicaID), m, nil, nil, protocol.OutputPolicy{}, nil)
	fmt.Fprintln(os.Stderr, "recom: job failed to start:", err)
	return 1
}

// recoverLostRuns finalizes runs/jobs left "running" by a helper or
// supervisor that died (crash, kill -9, reboot). Called with the lease held,
// so no other run can have touched the replica since: comparing the persisted
// pre-run snapshot with the current tree attributes outputs safely.
func recoverLostRuns(l Layout, replica string) []string {
	ms, _ := loadManifests(l, replica)
	var warns []string
	for _, m := range ms {
		if m.State != protocol.JobRunning && m.State != protocol.JobStarting {
			continue
		}
		var j *jobRecord
		if m.Kind == "job" {
			j, _ = loadJob(l, replica, m.RunID)
			if j != nil && effectiveState(j) == protocol.JobRunning {
				continue
			}
		}
		pre, err := loadSnapshot(filepath.Join(l.Runs(replica), m.RunID+".pre"))
		var f *filters
		var pol protocol.OutputPolicy
		if err == nil {
			pol = pre.Policy
			if f, err = newFilters(pol); err != nil {
				pre = nil
			}
		} else {
			pre = nil
		}
		m.State = protocol.JobLost
		m.Note = "the process supervising this run disappeared (crash or reboot); outputs were reconstructed afterwards"
		if m.Ended.IsZero() {
			m.Ended = time.Now().UTC()
		}
		res, _ := finishRun(l, replica, l.Replica(replica), m, pre, f, pol, nil)
		if j != nil {
			j.State, j.Ended, j.Message = protocol.JobLost, m.Ended, "supervisor disappeared; outputs reconstructed"
			_ = saveJob(l, j)
		}
		warns = append(warns, fmt.Sprintf("recovered lost %s %s (%s): %d output(s)", m.Kind, m.RunID, m.Command, len(res.Outputs)))
	}
	return warns
}

// logs streams job logs to the client.
func (s *server) logs(r *protocol.JobRequest) (*protocol.Response, error) {
	id, err := resolveJob(s.layout, s.replica, r.JobID)
	if err != nil {
		return nil, err
	}
	dir := s.layout.Job(s.replica, id)
	type src struct {
		path string
		kind byte
		off  int64
	}
	var srcs []*src
	j, err := loadJob(s.layout, s.replica, id)
	if err != nil {
		return nil, err
	}
	if j.Tmux {
		srcs = []*src{{path: filepath.Join(dir, "output.log"), kind: protocol.FStdout}}
	} else {
		srcs = []*src{{path: filepath.Join(dir, "stdout.log"), kind: protocol.FStdout}, {path: filepath.Join(dir, "stderr.log"), kind: protocol.FStderr}}
	}
	if r.Tail > 0 {
		for _, sc := range srcs {
			sc.off = tailOffset(sc.path, r.Tail)
		}
	}
	pump := func() (bool, error) {
		moved := false
		buf := make([]byte, 64<<10)
		for _, sc := range srcs {
			f, err := os.Open(sc.path)
			if err != nil {
				continue
			}
			for {
				n, err := f.ReadAt(buf, sc.off)
				if n > 0 {
					moved = true
					sc.off += int64(n)
					if werr := s.out.Write(sc.kind, buf[:n]); werr != nil {
						f.Close()
						return moved, werr
					}
				}
				if err != nil || n == 0 {
					break
				}
			}
			f.Close()
		}
		return moved, nil
	}
	for {
		if _, err := pump(); err != nil {
			return nil, err
		}
		if !r.Follow {
			break
		}
		cur, err := loadJob(s.layout, s.replica, id)
		if err != nil {
			return nil, err
		}
		st := effectiveState(cur)
		if st != protocol.JobRunning && st != protocol.JobStarting {
			if _, err := pump(); err != nil {
				return nil, err
			}
			break
		}
		select {
		case <-s.ctx.Done():
			return nil, s.ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	info, err := s.jobInfo(id)
	if err != nil {
		return nil, err
	}
	return &protocol.Response{Job: info}, nil
}

// tailOffset returns the offset of the last n lines of a file.
func tailOffset(p string, n int) int64 {
	f, err := os.Open(p)
	if err != nil {
		return 0
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 0
	}
	size := fi.Size()
	const chunk = 64 << 10
	lines := 0
	pos := size
	buf := make([]byte, chunk)
	for pos > 0 {
		step := int64(chunk)
		if pos < step {
			step = pos
		}
		pos -= step
		if _, err := f.ReadAt(buf[:step], pos); err != nil && err != io.EOF {
			return 0
		}
		for i := step - 1; i >= 0; i-- {
			if buf[i] == '\n' {
				if pos+i == size-1 {
					continue // trailing newline
				}
				lines++
				if lines >= n {
					return pos + i + 1
				}
			}
		}
	}
	return 0
}

// stop terminates a job's process group (after verifying its identity),
// escalating from SIGTERM to SIGKILL after the grace period.
func (s *server) stop(r *protocol.JobRequest) (*protocol.Response, error) {
	id, err := resolveJob(s.layout, s.replica, r.JobID)
	if err != nil {
		return nil, err
	}
	j, err := loadJob(s.layout, s.replica, id)
	if err != nil {
		return nil, err
	}
	st := effectiveState(j)
	if st != protocol.JobRunning && st != protocol.JobStarting {
		info, _ := s.jobInfo(id)
		if info != nil {
			info.Message = "job is not running (" + st + ")"
		}
		return &protocol.Response{Job: info}, nil
	}
	_ = os.WriteFile(filepath.Join(s.layout.Job(s.replica, id), "stop-requested"), []byte(time.Now().UTC().Format(time.RFC3339)), 0o600)
	grace := r.Grace
	if grace <= 0 {
		grace = 10 * time.Second
	}
	if alivePID(j.PID, j.PIDStart) {
		_ = syscall.Kill(-j.PID, syscall.SIGTERM)
		s.event("info", fmt.Sprintf("sent SIGTERM to job %s (process group %d)", id, j.PID))
		deadline := time.Now().Add(grace)
		for alivePID(j.PID, j.PIDStart) && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
		if alivePID(j.PID, j.PIDStart) {
			_ = syscall.Kill(-j.PID, syscall.SIGKILL)
			s.event("info", fmt.Sprintf("job %s ignored SIGTERM for %s; sent SIGKILL", id, grace))
		}
	}
	// Let the supervisor record the final status and outputs.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		cur, err := loadJob(s.layout, s.replica, id)
		if err == nil {
			if es := effectiveState(cur); es != protocol.JobRunning && es != protocol.JobStarting {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	info, err := s.jobInfo(id)
	if err != nil {
		return nil, err
	}
	return &protocol.Response{Job: info}, nil
}
