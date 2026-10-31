package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"github.com/chaseungjoon/recom/internal/config"
	"github.com/chaseungjoon/recom/internal/protocol"
	"github.com/chaseungjoon/recom/internal/remote"
	"github.com/chaseungjoon/recom/internal/rsync"
)

// runRemote implements `recom <command> [args...]`.
func runRemote(a *app, args []string) error {
	gf := a.gf
	if gf.shell != "" && len(args) > 0 {
		return exitWith(ExitUsage, errors.New("--shell takes the whole remote command as one string; do not pass extra arguments"))
	}
	if gf.detach && gf.tty {
		return exitWith(ExitUsage, errors.New("--detach jobs already get a terminal under tmux; use `recom attach <id>` instead of --tty"))
	}
	if gf.tty && !(term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))) {
		return exitWith(ExitUsage, errors.New("--tty needs an interactive terminal on stdin and stdout"))
	}
	argv := args
	if gf.shell != "" {
		argv = []string{gf.shell}
	}

	ws, err := a.workspace()
	if errors.Is(err, config.ErrNoProject) {
		if !gf.noSync || gf.detach {
			return exitWith(ExitPreRun, err)
		}
		ws = nil // backend-global command from the remote home directory
	} else if err != nil {
		return exitWith(ExitPreRun, err)
	}
	t, err := a.target(ws)
	if err != nil {
		return exitWith(ExitPreRun, err)
	}
	var rs *rsync.Binary
	if ws != nil {
		if rs, err = rsync.Detect(); err != nil {
			return exitWith(ExitPreRun, err)
		}
		a.debug("using %s (%s)", rs.Path, rs.Version)
		warnLocalPaths(a, ws, argv)
	}

	conn, err := a.dial(t)
	if err != nil {
		return connErr(err)
	}
	defer conn.Close()

	if ws == nil {
		return a.execGlobal(conn, t.Name, argv)
	}

	replicaID := ws.ReplicaID()
	st := ws.LoadHostState(t.Name)
	open := &protocol.OpenRequest{
		ReplicaID: replicaID, ProjectID: ws.Project.ID,
		Create: !gf.noSync, Lock: !gf.noSync || gf.detach, Wait: true,
		TryLock: gf.noSync && !gf.detach,
		Owner:   ownerName(ws),
	}
	resp, err := a.callInterruptible(conn, &protocol.Request{Op: protocol.OpOpen, Open: open})
	if err != nil {
		var pe *protocol.Error
		if errors.As(err, &pe) && pe.Code == protocol.ErrBusy {
			return exitWith(ExitPreRun, busyMessage(pe))
		}
		return connErr(err)
	}
	or := resp.Open
	for _, w := range or.Warnings {
		a.warn("%s", w)
	}
	st.ReplicaPath = or.ReplicaPath
	cwd := ws.Rel
	collectOK := or.Locked && !gf.noCollect
	if gf.noSync && !or.ReplicaExists {
		a.info("no replica of this project on %s yet; running from the remote home directory (outputs are not collected)", t.Name)
		cwd = "~"
		collectOK = false
	} else if gf.noSync && !or.Locked {
		a.warn("project is busy on %s (%s); running without output tracking", t.Name, holderText(or.Holder))
	}

	// 1. Reconcile outputs left pending by earlier runs before uploading, so
	//    nothing generated remotely can be lost or overwritten.
	var protected []string
	if or.Locked {
		var auto []protocol.Output
		for _, o := range or.Pending {
			if o.Status == protocol.StatusPending && !gf.noCollect {
				auto = append(auto, o)
			}
		}
		if len(auto) > 0 {
			a.info("collecting %d pending output(s) from earlier runs", len(auto))
			if _, err := a.collectOutputs(conn, ws, t, rs, or.ReplicaPath, auto, "pending", false); err != nil {
				a.warn("%v", err)
			}
			if r2, err := conn.Call(&protocol.Request{Op: protocol.OpPending, Pending: &protocol.PendingRequest{ReplicaID: replicaID}}, nil, nil); err == nil {
				or.Pending = r2.Pending.Outputs
			}
		}
		for _, o := range or.Pending {
			if o.Pending() {
				protected = append(protected, o.Path)
			}
		}
	}

	// 2. Upload. The command never runs against a partial upload.
	if !gf.noSync {
		start := time.Now()
		stats, err := rs.Upload(a.ctx, t, ws.Root, or.ReplicaPath, uploadRules(ws.Project, protected), a.rsyncStderr(), false)
		if err != nil {
			return exitWith(ExitPreRun, fmt.Errorf("%w; the command was not run", err))
		}
		st.LastSync, st.LastUpload = time.Now().UTC(), stats.Files
		switch {
		case stats.Files == 0:
			a.debug("project already in sync with %s (%s)", t.Name, time.Since(start).Round(time.Millisecond))
		default:
			a.info("synced %d changed file(s) (%s) to %s", stats.Files, humanBytes(stats.Bytes), t.Name)
		}
		if a.gf.verbose {
			for _, n := range stats.Names {
				a.debug("  ↑ %s", n)
			}
		}
		if len(protected) > 0 {
			a.info("left %d path(s) with uncollected remote outputs untouched (see `recom status`)", len(protected))
		}
	}

	req := &protocol.ExecRequest{
		Argv: argv, Display: displayCommand(displayArgv(argv, gf.shell)), Cwd: cwd,
		Env: ws.Project.Run.Env, Path: ws.Project.Run.Path, Wrapper: ws.Project.Run.Wrapper, EnvMode: ws.Project.Run.EnvMode,
		Shell: gf.shell != "", TTY: gf.tty, Term: os.Getenv("TERM"), Detach: gf.detach, Tmux: ws.Project.Jobs.Tmux,
		Policy: outputPolicy(ws.Project), Client: ownerName(ws), Local: ws.Root,
	}
	req.Policy.NoCollection = !collectOK
	if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		req.Cols, req.Rows = w, h
	}

	if gf.detach {
		return a.launchDetached(conn, ws, t.Name, st, req)
	}

	exit, runErr := a.execStreaming(conn, req)
	if runErr != nil {
		var pe *protocol.Error
		if errors.As(runErr, &pe) && pe.Code == protocol.ErrExec {
			a.saveOutcome(ws, st, "", req.Display, pe.ExitCode, "not started")
			msg := pe.Message + " (" + t.Name + ")"
			if hint := subcommandHint(a.cmd, argv); hint != "" {
				msg += "; " + hint
			}
			return exitWith(pe.ExitCode, errors.New(msg))
		}
		var te *remote.TransportError
		if errors.As(runErr, &te) {
			return exitWith(ExitTransport, fmt.Errorf("%v; the remote command was terminated and any outputs will be collected on your next recom run", runErr))
		}
		return exitWith(ExitPreRun, runErr)
	}
	for _, w := range exit.Warnings {
		if collectOK || !strings.Contains(w, "not tracked") {
			a.warn("%s", w)
		}
	}

	// 3. Bring outputs home, even after a failed command.
	var pending, held []protocol.Output
	for _, o := range exit.Outputs {
		switch o.Status {
		case protocol.StatusPending:
			pending = append(pending, o)
		case protocol.StatusHeld:
			held = append(held, o)
		case protocol.StatusSkipped:
			a.warn("not collecting %s: %s", a.localName(ws, o.Path), o.Reason)
		}
	}
	sum, err := a.collectOutputs(conn, ws, t, rs, or.ReplicaPath, pending, exit.RunID, false)
	if err != nil {
		a.warn("output collection failed: %v", err)
		sum.failed += len(pending)
	}
	a.reportHeld(ws, held)
	if sum.collected > 0 && sum.bytes > 0 && len(pending) > 15 {
		a.info("collected %d output(s) (%s)", sum.collected, humanBytes(sum.bytes))
	}

	outcome := "exit " + fmt.Sprint(exit.ExitCode)
	if exit.Signal != "" {
		outcome = "killed by " + exit.Signal
		a.info("remote command was killed by %s", exit.Signal)
	}
	if sum.failed > 0 {
		outcome += ", outputs pending"
	}
	a.saveOutcome(ws, st, exit.RunID, req.Display, exit.ExitCode, outcome)
	if exit.ExitCode == 0 && sum.failed > 0 {
		return exitWith(ExitOutputs, fmt.Errorf("the command succeeded but %d output(s) could not be collected; they stay pending on %s", sum.failed, t.Name))
	}
	if exit.ExitCode != 0 {
		return exitSilently(exit.ExitCode)
	}
	return nil
}

func displayArgv(argv []string, shell string) []string {
	if shell != "" {
		return []string{"--shell", shell}
	}
	return argv
}

func (a *app) saveOutcome(ws *config.Workspace, st *config.HostState, runID, display string, code int, outcome string) {
	st.LastRunID, st.LastCommand, st.LastOutcome = runID, display, outcome
	st.LastExit = &code
	st.LastRunAt = time.Now().UTC()
	if err := ws.SaveHostState(st); err != nil {
		a.debug("save state: %v", err)
	}
}

func ownerName(ws *config.Workspace) string {
	h, _ := os.Hostname()
	u := os.Getenv("USER")
	if u == "" {
		u = "user"
	}
	return u + "@" + h
}

func holderText(h *protocol.LeaseInfo) string {
	if h == nil {
		return "busy"
	}
	if h.Kind == "job" {
		return fmt.Sprintf("detached job %s is running `%s`", h.ID, h.Command)
	}
	return fmt.Sprintf("another run from %s", h.Owner)
}

func busyMessage(pe *protocol.Error) error {
	if pe.Holder != nil && pe.Holder.Kind == "job" {
		return fmt.Errorf("the project is busy on this backend: detached job %s (`%s`) is running and owns the replica.\n"+
			"  recom logs %s -f      follow it\n"+
			"  recom stop %s         stop it\n"+
			"  recom --no-sync <cmd> run something without syncing (e.g. nvidia-smi)", pe.Holder.ID, pe.Holder.Command, pe.Holder.ID, pe.Holder.ID)
	}
	return errors.New(pe.Message)
}

// callInterruptible performs a call that may block (waiting for the replica
// lease) and lets Ctrl-C abort it cleanly.
func (a *app) callInterruptible(conn *remote.Conn, req *protocol.Request) (*protocol.Response, error) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	type res struct {
		r   *protocol.Response
		err error
	}
	ch := make(chan res, 1)
	go func() {
		r, err := conn.Call(req, nil, nil)
		ch <- res{r, err}
	}()
	select {
	case r := <-ch:
		return r.r, r.err
	case <-sig:
		conn.Abort()
		return nil, exitSilently(130)
	}
}

// execGlobal runs a backend-global command (--no-sync outside a project).
func (a *app) execGlobal(conn *remote.Conn, host string, argv []string) error {
	open := &protocol.OpenRequest{ReplicaID: "global-0000000000", Owner: "global"}
	if _, err := conn.Call(&protocol.Request{Op: protocol.OpOpen, Open: open}, nil, nil); err != nil {
		return connErr(err)
	}
	req := &protocol.ExecRequest{
		Argv: argv, Display: displayCommand(displayArgv(argv, a.gf.shell)), Cwd: "~",
		Shell: a.gf.shell != "", TTY: a.gf.tty, Term: os.Getenv("TERM"),
		Policy: protocol.OutputPolicy{NoCollection: true}, Client: "global",
	}
	if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		req.Cols, req.Rows = w, h
	}
	exit, err := a.execStreaming(conn, req)
	if err != nil {
		var pe *protocol.Error
		if errors.As(err, &pe) && pe.Code == protocol.ErrExec {
			return exitWith(pe.ExitCode, fmt.Errorf("%s (%s)", pe.Message, host))
		}
		var te *remote.TransportError
		if errors.As(err, &te) {
			return exitWith(ExitTransport, err)
		}
		return exitWith(ExitPreRun, err)
	}
	if exit.ExitCode != 0 {
		return exitSilently(exit.ExitCode)
	}
	return nil
}

// execStreaming runs the foreground command with terminal handling.
func (a *app) execStreaming(conn *remote.Conn, req *protocol.ExecRequest) (*protocol.ExitResult, error) {
	var stdin io.Reader
	if req.TTY {
		stdin = os.Stdin
	} else if stdinUsable() {
		stdin = os.Stdin
	}
	restore := func() {}
	if req.TTY {
		fd := int(os.Stdin.Fd())
		old, err := term.MakeRaw(fd)
		if err != nil {
			return nil, fmt.Errorf("cannot switch the terminal to raw mode: %w", err)
		}
		restored := false
		restore = func() {
			if !restored {
				restored = true
				_ = term.Restore(fd, old)
			}
		}
		defer restore()
		winch := make(chan os.Signal, 1)
		signal.Notify(winch, syscall.SIGWINCH)
		defer signal.Stop(winch)
		go func() {
			for range winch {
				if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
					_ = conn.SendJSON(protocol.FResize, protocol.Resize{Rows: h, Cols: w})
				}
			}
		}()
	}

	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(sigs)
	stopSig := make(chan struct{})
	defer close(stopSig)
	go func() {
		interrupts := 0
		for {
			select {
			case <-stopSig:
				return
			case s := <-sigs:
				name := map[os.Signal]string{os.Interrupt: "INT", syscall.SIGTERM: "TERM", syscall.SIGHUP: "HUP", syscall.SIGQUIT: "QUIT"}[s]
				if name != "INT" {
					_ = conn.SendJSON(protocol.FSignal, protocol.Signal{Name: name})
					continue
				}
				interrupts++
				switch interrupts {
				case 1:
					_ = conn.SendJSON(protocol.FSignal, protocol.Signal{Name: "INT"})
					fmt.Fprintf(os.Stderr, "\r\n%s sent SIGINT to the remote command (Ctrl-C again to kill it)\r\n", a.prefix())
				case 2:
					_ = conn.SendJSON(protocol.FSignal, protocol.Signal{Name: "KILL"})
					fmt.Fprintf(os.Stderr, "\r\n%s sent SIGKILL to the remote process group (Ctrl-C again to disconnect)\r\n", a.prefix())
				default:
					restore()
					fmt.Fprintf(os.Stderr, "\r\n%s disconnecting; the backend terminates the command and keeps its outputs pending\r\n", a.prefix())
					conn.Abort()
					os.Exit(130)
				}
			}
		}
	}()
	exit, err := conn.Exec(req, remote.ExecIO{Stdin: stdin, Stdout: os.Stdout, Stderr: os.Stderr})
	restore()
	return exit, err
}

// stdinUsable reports whether forwarding stdin is safe: always for pipes and
// files, and for a terminal only when recom is in the foreground process
// group (reading the tty from a background job would stop recom).
func stdinUsable() bool {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return true
	}
	pgrp, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
	return err == nil && pgrp == syscall.Getpgrp()
}

func (a *app) launchDetached(conn *remote.Conn, ws *config.Workspace, host string, st *config.HostState, req *protocol.ExecRequest) error {
	exit, err := conn.Exec(req, remote.ExecIO{Stdout: os.Stdout, Stderr: os.Stderr})
	if err != nil {
		var pe *protocol.Error
		if errors.As(err, &pe) && pe.Code == protocol.ErrExec {
			return exitWith(pe.ExitCode, fmt.Errorf("%s (%s)", pe.Message, host))
		}
		return connErr(err)
	}
	a.saveOutcome(ws, st, exit.JobID, req.Display, 0, "detached job started")
	fmt.Fprintln(a.out, exit.JobID)
	mode := "logs only (tmux not installed on the backend)"
	if exit.Tmux {
		mode = "tmux session, attachable"
	}
	a.info("started detached job %s on %s (%s)", exit.JobID, host, mode)
	a.info("  recom logs %s -f   ·   recom attach %s   ·   recom stop %s", exit.JobID, exit.JobID, exit.JobID)
	a.info("outputs are collected automatically by your next recom command after it finishes")
	return nil
}

// warnLocalPaths flags arguments that are absolute local paths: the backend
// has different paths, so they almost certainly will not work.
func warnLocalPaths(a *app, ws *config.Workspace, argv []string) {
	for _, arg := range argv {
		if strings.HasPrefix(arg, ws.Root+"/") || arg == ws.Root {
			a.warn("argument %q is a local absolute path; the backend uses a different directory — pass paths relative to the current directory", arg)
			return
		}
	}
}

// subcommandHint suggests a recom subcommand when a remote command was not
// found and looks like a typo of one.
func subcommandHint(root *cobra.Command, argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	if s := root.Root().SuggestionsFor(argv[0]); len(s) > 0 {
		return fmt.Sprintf("did you mean `recom %s`?", s[0])
	}
	return ""
}
