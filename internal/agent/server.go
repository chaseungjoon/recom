package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/chaseungjoon/recom/internal/buildinfo"
	"github.com/chaseungjoon/recom/internal/protocol"
)

type server struct {
	ctx    context.Context
	cancel context.CancelFunc
	out    *protocol.Writer
	frames chan protocol.Frame
	layout Layout
	home   string

	replica     string
	replicaPath string
	projectID   string
	leaseID     string // set while this session holds the replica lease
	owner       string
}

func serve(stdin io.Reader, stdout io.Writer) int {
	// A dropped SSH connection must not kill the helper before it has
	// terminated the child and written the manifest.
	signal.Notify(make(chan os.Signal, 1), syscall.SIGPIPE, syscall.SIGHUP)

	bw := bufio.NewWriterSize(stdout, 64<<10)
	if _, err := bw.WriteString(protocol.Magic); err != nil {
		return 1
	}
	s := &server{out: protocol.NewWriter(bw), frames: make(chan protocol.Frame, 64)}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	defer s.cancel()
	s.home, _ = os.UserHomeDir()

	in := protocol.NewReader(stdin)
	f, err := in.ReadFrame()
	if err != nil {
		return 1
	}
	var hello protocol.Hello
	if f.Type != protocol.FHello || protocol.Decode(f, &hello) != nil {
		s.fatal(perr(protocol.ErrBadRequest, "expected hello frame"))
		return 1
	}
	if hello.Protocol != protocol.Version {
		s.fatal(perr(protocol.ErrBadRequest, "protocol mismatch: client speaks v%d, helper v%d", hello.Protocol, protocol.Version))
		return 1
	}
	if err := ValidateRoot(hello.Root); err != nil {
		s.fatal(perr(protocol.ErrBadRequest, "%v", err))
		return 1
	}
	s.layout = Layout{Root: hello.Root}
	if err := os.MkdirAll(hello.Root, 0o700); err != nil {
		s.fatal(perr(protocol.ErrInternal, "cannot create %s: %v", hello.Root, err))
		return 1
	}
	sha, _ := selfSHA256()
	if err := s.out.WriteJSON(protocol.FAck, protocol.Ack{
		Protocol: protocol.Version, HelperVersion: buildinfo.Version, HelperSHA256: sha,
		OS: runtime.GOOS, Arch: runtime.GOARCH, Home: s.home, PID: os.Getpid(),
	}); err != nil {
		return 1
	}

	go func() {
		defer close(s.frames)
		defer s.cancel()
		for {
			f, err := in.ReadFrame()
			if err != nil {
				return
			}
			select {
			case s.frames <- f:
			case <-s.ctx.Done():
				return
			}
		}
	}()

	defer s.releaseLease()
	for f := range s.frames {
		if f.Type != protocol.FRequest {
			continue // stray stdin/signal frames between requests
		}
		var req protocol.Request
		if err := protocol.Decode(f, &req); err != nil {
			s.respondErr(perr(protocol.ErrBadRequest, "%v", err))
			continue
		}
		if req.Op == protocol.OpClose {
			s.releaseLease()
			_ = s.out.WriteJSON(protocol.FResponse, protocol.Response{})
			return 0
		}
		resp, err := s.handle(&req)
		if err != nil {
			s.respondErr(asPErr(err))
			continue
		}
		if err := s.out.WriteJSON(protocol.FResponse, resp); err != nil {
			return 1
		}
	}
	return 0 // client went away
}

func (s *server) fatal(e *protocol.Error) {
	_ = s.out.WriteJSON(protocol.FResponse, protocol.Response{Error: e})
}

func (s *server) respondErr(e *protocol.Error) {
	_ = s.out.WriteJSON(protocol.FResponse, protocol.Response{Error: e})
}

func (s *server) event(kind, msg string) {
	_ = s.out.WriteJSON(protocol.FEvent, protocol.Event{Kind: kind, Message: msg})
}

func (s *server) releaseLease() {
	if s.leaseID != "" && s.replica != "" {
		_ = releaseLease(s.layout.State(s.replica), s.leaseID)
		s.leaseID = ""
	}
}

func (s *server) selectReplica(id string) error {
	if !ValidReplicaID(id) {
		return perr(protocol.ErrBadRequest, "invalid replica id %q", id)
	}
	if s.replica != "" && s.replica != id {
		return perr(protocol.ErrBadRequest, "session already bound to replica %s", s.replica)
	}
	s.replica = id
	s.replicaPath = s.layout.Replica(id)
	return s.layout.EnsureState(id)
}

func (s *server) replicaExists() bool {
	fi, err := os.Lstat(s.replicaPath)
	return err == nil && fi.IsDir()
}

func (s *server) handle(req *protocol.Request) (*protocol.Response, error) {
	switch req.Op {
	case protocol.OpOpen:
		if req.Open == nil {
			return nil, perr(protocol.ErrBadRequest, "missing open params")
		}
		return s.open(req.Open)
	case protocol.OpExec:
		if req.Exec == nil {
			return nil, perr(protocol.ErrBadRequest, "missing exec params")
		}
		if req.Exec.Detach {
			return s.launchJob(req.Exec)
		}
		return s.execForeground(req.Exec)
	case protocol.OpMark:
		if req.Mark == nil {
			return nil, perr(protocol.ErrBadRequest, "missing mark params")
		}
		if err := s.selectReplica(req.Mark.ReplicaID); err != nil {
			return nil, err
		}
		for i := range req.Mark.Items {
			p, err := CleanRel(req.Mark.Items[i].Path)
			if err != nil {
				return nil, perr(protocol.ErrBadRequest, "%v", err)
			}
			req.Mark.Items[i].Path = p
		}
		if err := markOutputs(s.layout, s.replica, req.Mark.Items); err != nil {
			return nil, err
		}
		return &protocol.Response{}, nil
	case protocol.OpPending:
		if req.Pending == nil {
			return nil, perr(protocol.ErrBadRequest, "missing pending params")
		}
		if err := s.selectReplica(req.Pending.ReplicaID); err != nil {
			return nil, err
		}
		outs, warns := pendingOutputs(s.layout, s.replica)
		return &protocol.Response{Pending: &protocol.PendingResult{Outputs: outs, Warnings: warns}}, nil
	case protocol.OpStat:
		if req.Stat == nil {
			return nil, perr(protocol.ErrBadRequest, "missing stat params")
		}
		return s.stat(req.Stat)
	case protocol.OpJobs, protocol.OpJob, protocol.OpLogs, protocol.OpStop:
		if req.Job == nil {
			return nil, perr(protocol.ErrBadRequest, "missing job params")
		}
		if err := s.selectReplica(req.Job.ReplicaID); err != nil {
			return nil, err
		}
		switch req.Op {
		case protocol.OpJobs:
			return &protocol.Response{Jobs: listJobs(s.layout, s.replica)}, nil
		case protocol.OpJob:
			j, err := s.jobInfo(req.Job.JobID)
			if err != nil {
				return nil, err
			}
			return &protocol.Response{Job: j}, nil
		case protocol.OpLogs:
			return s.logs(req.Job)
		default:
			return s.stop(req.Job)
		}
	case protocol.OpEnv:
		if req.Env == nil {
			req.Env = &protocol.EnvRequest{}
		}
		return s.envReport(req.Env)
	case protocol.OpStatus:
		if req.Pending == nil {
			return nil, perr(protocol.ErrBadRequest, "missing status params")
		}
		if err := s.selectReplica(req.Pending.ReplicaID); err != nil {
			return nil, err
		}
		st := &protocol.StatusResult{ReplicaPath: s.replicaPath, ReplicaExists: s.replicaExists()}
		st.Holder = currentHolder(s.layout.State(s.replica))
		st.Pending, st.Warnings = pendingOutputs(s.layout, s.replica)
		for _, j := range listJobs(s.layout, s.replica) {
			if j.State == protocol.JobRunning || j.State == protocol.JobStarting {
				st.Running = append(st.Running, j)
			}
		}
		st.LastRun = lastRun(s.layout, s.replica)
		return &protocol.Response{Status: st}, nil
	default:
		return nil, perr(protocol.ErrBadRequest, "unknown operation %q", req.Op)
	}
}

func (s *server) open(o *protocol.OpenRequest) (*protocol.Response, error) {
	if err := s.selectReplica(o.ReplicaID); err != nil {
		return nil, err
	}
	s.projectID = o.ProjectID
	s.owner = o.Owner
	res := &protocol.OpenResult{ReplicaPath: s.replicaPath}
	if o.Create {
		if err := os.MkdirAll(filepath.Join(s.layout.Root, "projects"), 0o700); err != nil {
			return nil, err
		}
		if err := os.Mkdir(s.replicaPath, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	if fi, err := os.Lstat(s.replicaPath); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return nil, perr(protocol.ErrBadRequest, "replica %s is a symlink; refusing to use it", s.replicaPath)
	}
	res.ReplicaExists = s.replicaExists()
	if o.Lock || o.TryLock {
		id := newRunID()
		me := selfLease("run", id, o.Owner, "")
		stateDir := s.layout.State(s.replica)
		_, err := acquireLease(s.ctx, stateDir, me, o.Wait && !o.TryLock, func(h *protocol.LeaseInfo) {
			_ = s.out.WriteJSON(protocol.FEvent, protocol.Event{Kind: "waiting", Message: "waiting for " + describeHolder(h), Holder: h})
		})
		var busy errBusy
		switch {
		case errors.As(err, &busy) && o.TryLock:
			res.Holder = busy.holder
		case errors.As(err, &busy):
			return nil, &protocol.Error{Code: protocol.ErrBusy, Message: busy.Error(), Holder: busy.holder}
		case err != nil:
			return nil, err
		default:
			s.leaseID = id
			res.Locked = true
			res.Warnings = append(res.Warnings, recoverLostRuns(s.layout, s.replica)...)
		}
	}
	var warns []string
	res.Pending, warns = pendingOutputs(s.layout, s.replica)
	res.Warnings = append(res.Warnings, warns...)
	return &protocol.Response{Open: res}, nil
}

// requireLease ensures the session holds the lease (and returns whether it does).
func (s *server) haveLease() bool { return s.leaseID != "" }

func errorf(format string, args ...any) error { return fmt.Errorf(format, args...) }
