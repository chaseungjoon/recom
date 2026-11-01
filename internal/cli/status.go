package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/chaseungjoon/recom/internal/buildinfo"
	"github.com/chaseungjoon/recom/internal/config"
	"github.com/chaseungjoon/recom/internal/helperbin"
	"github.com/chaseungjoon/recom/internal/protocol"
	"github.com/chaseungjoon/recom/internal/rsync"
	"github.com/chaseungjoon/recom/internal/sshx"
)

func versionCmd(gf *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version and build information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a := newApp(cmd, gf)
			info := map[string]any{
				"version": buildinfo.Version, "commit": buildinfo.Commit, "date": buildinfo.Date,
				"go": runtime.Version(), "platform": buildinfo.Platform(), "protocol": protocol.Version,
				"embedded_helpers": helperbin.Embedded(),
			}
			if gf.json {
				return json.NewEncoder(a.out).Encode(info)
			}
			fmt.Fprintf(a.out, "recom %s\n", buildinfo.Version)
			if buildinfo.Commit != "" {
				fmt.Fprintf(a.out, "  commit:   %s\n", buildinfo.Commit)
			}
			if buildinfo.Date != "" {
				fmt.Fprintf(a.out, "  built:    %s\n", buildinfo.Date)
			}
			fmt.Fprintf(a.out, "  platform: %s (%s)\n", buildinfo.Platform(), runtime.Version())
			fmt.Fprintf(a.out, "  protocol: v%d\n", protocol.Version)
			h := helperbin.Embedded()
			if len(h) == 0 {
				h = []string{"none (development build; can only drive backends of its own platform)"}
			}
			fmt.Fprintf(a.out, "  helpers:  %s\n", strings.Join(h, ", "))
			return nil
		},
	}
}

type statusJSON struct {
	Project     string                 `json:"project_id"`
	Root        string                 `json:"root"`
	Cwd         string                 `json:"cwd"`
	CopyID      string                 `json:"copy_id"`
	Replica     string                 `json:"replica_id"`
	Host        string                 `json:"host"`
	Target      string                 `json:"ssh_target"`
	Local       *config.HostState      `json:"local_state"`
	Connected   bool                   `json:"connected"`
	ConnError   string                 `json:"connection_error,omitempty"`
	Remote      *protocol.StatusResult `json:"remote,omitempty"`
	ConnectTime string                 `json:"connect_time,omitempty"`
}

func statusCmd(gf *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show project identity, backend, last sync/run and pending outputs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a := newApp(cmd, gf)
			ws, err := a.workspace()
			if err != nil {
				return err
			}
			t, err := a.target(ws)
			if err != nil {
				return err
			}
			st := ws.LoadHostState(t.Name)
			out := statusJSON{Project: ws.Project.ID, Root: ws.Root, Cwd: ws.Rel, CopyID: ws.Local.CopyID, Replica: ws.ReplicaID(), Host: t.Name, Target: t.Host.Target, Local: st}
			start := time.Now()
			conn, err := a.dial(t)
			if err != nil {
				out.ConnError = err.Error()
			} else {
				out.ConnectTime = time.Since(start).Round(time.Millisecond).String()
				resp, err := conn.Call(&protocol.Request{Op: protocol.OpStatus, Pending: &protocol.PendingRequest{ReplicaID: ws.ReplicaID()}}, nil, nil)
				conn.Close()
				if err != nil {
					out.ConnError = err.Error()
				} else {
					out.Connected = true
					out.Remote = resp.Status
				}
			}
			if gf.json {
				return json.NewEncoder(a.out).Encode(out)
			}
			w := a.out
			fmt.Fprintf(w, "Project   %s\n", ws.Root)
			fmt.Fprintf(w, "  id        %s (working copy %s)\n", ws.Project.ID, ws.Local.CopyID)
			if ws.Rel != "." {
				fmt.Fprintf(w, "  cwd       %s\n", ws.Rel)
			}
			fmt.Fprintf(w, "Backend   %s (%s, %s/%s)\n", t.Name, t.Host.Target, t.Host.OS, t.Host.Arch)
			if !out.Connected {
				fmt.Fprintf(w, "  status    unreachable: %s\n", out.ConnError)
			} else {
				fmt.Fprintf(w, "  status    connected in %s\n", out.ConnectTime)
				r := out.Remote
				if r.ReplicaExists {
					fmt.Fprintf(w, "  replica   %s\n", r.ReplicaPath)
				} else {
					fmt.Fprintf(w, "  replica   not created yet (the first `recom <command>` uploads the project)\n")
				}
				if r.Holder != nil {
					fmt.Fprintf(w, "  busy      %s\n", holderText(r.Holder))
				}
			}
			fmt.Fprintf(w, "Last sync %s", ago(st.LastSync))
			if !st.LastSync.IsZero() {
				fmt.Fprintf(w, " (%d file(s) uploaded)", st.LastUpload)
			}
			fmt.Fprintln(w)
			if st.LastCommand != "" {
				fmt.Fprintf(w, "Last run  %s — %s (%s)\n", st.LastCommand, st.LastOutcome, ago(st.LastRunAt))
			}
			for _, p := range installable(a.plans(ws)) {
				state := "unknown (backend unreachable)"
				if out.Connected {
					switch out.Remote.Setup[p.Name] {
					case p.Fingerprint:
						state = "ready"
					case "":
						state = "not set up yet (runs before the next command, or `recom setup`)"
					default:
						state = "dependencies changed (updated before the next command, or `recom setup`)"
					}
				}
				name := p.Name
				if len(p.Sources) > 0 {
					name += " (" + strings.Join(p.Sources, ", ") + ")"
				}
				fmt.Fprintf(w, "Setup     %s: %s\n", name, state)
			}
			if out.Connected {
				r := out.Remote
				for _, j := range r.Running {
					fmt.Fprintf(w, "Job       %s running for %s: %s\n", j.ID, dur(j.Started, time.Time{}), j.Command)
				}
				if len(r.Pending) == 0 {
					fmt.Fprintln(w, "Outputs   nothing pending")
				} else {
					fmt.Fprintf(w, "Outputs   %d pending on the backend:\n", len(r.Pending))
					tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
					for i, o := range r.Pending {
						if i == 20 {
							fmt.Fprintf(tw, "  …\t%d more\t\n", len(r.Pending)-20)
							break
						}
						why := "will be collected by the next run (or `recom pull`)"
						if o.Status == protocol.StatusHeld {
							why = "held: " + o.Reason + " — `recom pull " + quoteArg(o.Path) + "`"
						}
						fmt.Fprintf(tw, "  %s\t%s\t%s\n", o.Path, humanBytes(o.Size), why)
					}
					tw.Flush()
				}
				for _, warn := range r.Warnings {
					a.warn("%s", warn)
				}
			}
			if !out.Connected {
				return exitSilently(ExitTransport)
			}
			return nil
		},
	}
}

func envCmd(gf *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "env",
		Short: "Show the backend's OS, toolchains, GPUs and the environment commands get",
		Long: `Show what remote commands will see: OS, architecture, shell, PATH (after
the project's [run] settings), Python/compilers/CUDA tools and GPUs. Nothing
here comes from your local machine. To run the remote "env" program use
` + "`recom -- env`" + `.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a := newApp(cmd, gf)
			ws, err := a.workspace()
			if err != nil && !errors.Is(err, config.ErrNoProject) {
				return err
			}
			if errors.Is(err, config.ErrNoProject) {
				ws = nil
			}
			t, err := a.target(ws)
			if err != nil {
				return err
			}
			conn, err := a.dial(t)
			if err != nil {
				return connErr(err)
			}
			defer conn.Close()
			req := &protocol.EnvRequest{}
			if ws != nil {
				req.ReplicaID = ws.ReplicaID()
				req.EnvMode = ws.Project.Run.EnvMode
				plans := a.plans(ws)
				req.Env = runEnv(ws, plans)
				req.Path = runPath(ws, plans)
				req.PathFallback = pathFallback(plans)
			}
			resp, err := conn.Call(&protocol.Request{Op: protocol.OpEnv, Env: req}, nil, nil)
			if err != nil {
				return connErr(err)
			}
			e := resp.Env
			if gf.json {
				return json.NewEncoder(a.out).Encode(e)
			}
			w := a.out
			fmt.Fprintf(w, "Backend   %s (%s)\n", t.Name, e.Hostname)
			fmt.Fprintf(w, "OS        %s, kernel %s, %s\n", e.OS, e.Kernel, e.Arch)
			fmt.Fprintf(w, "Shell     %s (environment mode: %s)\n", e.Shell, e.EnvMode)
			fmt.Fprintf(w, "PATH      %s\n", e.PATH)
			if ws != nil && len(ws.Project.Run.Wrapper) > 0 {
				fmt.Fprintf(w, "Wrapper   %s\n", strings.Join(ws.Project.Run.Wrapper, " "))
			}
			if e.EnvWarning != "" {
				a.warn("%s", e.EnvWarning)
			}
			fmt.Fprintln(w, "Tools")
			tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
			for _, tool := range e.Tools {
				if tool.Path == "" {
					fmt.Fprintf(tw, "  %s\t-\tnot found\n", tool.Name)
					continue
				}
				fmt.Fprintf(tw, "  %s\t%s\t%s\n", tool.Name, tool.Path, tool.Version)
			}
			tw.Flush()
			if len(e.GPUs) > 0 {
				fmt.Fprintln(w, "GPUs")
				for _, g := range e.GPUs {
					fmt.Fprintf(w, "  %s\n", g)
				}
			} else {
				fmt.Fprintf(w, "GPUs      none detected (%s)\n", e.GPUNote)
			}
			fmt.Fprintf(w, "Storage   %s free of %s in %s\n", humanBytes(int64(e.DiskFree)), humanBytes(int64(e.DiskTotal)), e.Root)
			return nil
		},
	}
}

type check struct {
	name, detail string
	level        int // 0 ok, 1 warn, 2 fail
}

func doctorCmd(gf *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose local tools, SSH connectivity, the remote helper and backend tools",
		Long: `Run diagnostics: local ssh/rsync, configuration, project discovery, SSH
connectivity (and noisy shell startup files), the remote helper (installed or
upgraded if needed), backend rsync/tmux/disk space, and the replica lease.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a := newApp(cmd, gf)
			var checks []check
			add := func(level int, name, format string, args ...any) {
				checks = append(checks, check{name, fmt.Sprintf(format, args...), level})
			}
			add(0, "recom", "%s on %s", versionString(), buildinfo.Platform())
			if out, err := exec.Command(sshx.Binary(), "-V").CombinedOutput(); err != nil {
				add(2, "ssh", "%s not usable: %v", sshx.Binary(), err)
			} else {
				add(0, "ssh", "%s", strings.TrimSpace(string(out)))
			}
			if rs, err := rsync.Detect(); err != nil {
				add(2, "rsync", "%v", err)
			} else {
				add(0, "rsync", "%s (%s)", rs.Path, rs.Version)
			}
			if fi, err := os.Stat(config.GlobalPath()); err == nil {
				if fi.Mode().Perm()&0o077 != 0 {
					add(1, "config", "%s is readable by other users (chmod 600 recommended)", config.GlobalPath())
				} else {
					add(0, "config", "%s", config.GlobalPath())
				}
			} else {
				add(1, "config", "%s does not exist yet (run `recom ssh <alias>`)", config.GlobalPath())
			}
			g, err := a.loadGlobal()
			if err != nil {
				add(2, "config", "%v", err)
			}
			ws, werr := a.workspace()
			if werr != nil {
				add(1, "project", "%v", werr)
				ws = nil
			} else {
				add(0, "project", "%s (replica %s)", ws.Root, ws.ReplicaID())
			}
			if g != nil {
				t, err := a.target(ws)
				if err != nil {
					add(2, "backend", "%v", err)
				} else {
					a.doctorRemote(t, ws, add)
				}
			}
			fails := 0
			for _, c := range checks {
				mark := a.paint("32", "✓")
				switch c.level {
				case 1:
					mark = a.paint("33", "!")
				case 2:
					mark = a.paint("31", "✗")
					fails++
				}
				fmt.Fprintf(a.out, "%s %-9s %s\n", mark, c.name, c.detail)
			}
			if fails > 0 {
				return exitSilently(1)
			}
			return nil
		},
	}
}

func (a *app) doctorRemote(t sshx.Target, ws *config.Workspace, add func(int, string, string, ...any)) {
	add(0, "backend", "%s → %s (%s/%s, root %s)", t.Name, t.Host.Target, t.Host.OS, t.Host.Arch, t.Host.RemoteRoot)
	h, err := helperbin.For(t.Host.OS, t.Host.Arch)
	if err != nil {
		add(2, "helper", "%v", err)
		return
	}
	start := time.Now()
	conn, err := a.dial(t)
	if err != nil {
		add(2, "connect", "%v", err)
		add(0, "hint", "check `ssh %s true`; for Tailscale make sure both machines are online (`tailscale status`)", t.Host.Target)
		return
	}
	defer conn.Close()
	add(0, "connect", "SSH session + helper handshake in %s", time.Since(start).Round(time.Millisecond))
	if n := len(conn.Noise); n > 0 {
		add(1, "shell", "remote shell printed %d bytes on a non-interactive login (%q); recom ignores it, but rsync can break — move it into an interactive-only block", n, firstN(strings.TrimSpace(string(conn.Noise)), 50))
	} else {
		add(0, "shell", "non-interactive login is quiet")
	}
	add(0, "helper", "%s %s (%s, sha256 %s…, verified)", h.Name(), conn.Ack.HelperVersion, h.Source, conn.Ack.HelperSHA256[:12])
	if conn.Ack.HelperVersion != buildinfo.Version {
		add(1, "helper", "helper reports version %s, client is %s", conn.Ack.HelperVersion, buildinfo.Version)
	}
	req := &protocol.EnvRequest{}
	if ws != nil {
		req.ReplicaID = ws.ReplicaID()
		req.EnvMode = ws.Project.Run.EnvMode
		plans := a.plans(ws)
		req.Env = runEnv(ws, plans)
		req.Path = runPath(ws, plans)
		req.PathFallback = pathFallback(plans)
	}
	resp, err := conn.Call(&protocol.Request{Op: protocol.OpEnv, Env: req}, nil, nil)
	if err != nil {
		add(2, "env", "%v", err)
		return
	}
	e := resp.Env
	if e.EnvWarning != "" {
		add(1, "env", "%s", e.EnvWarning)
	}
	tool := func(name string) protocol.Tool {
		for _, t := range e.Tools {
			if t.Name == name {
				return t
			}
		}
		return protocol.Tool{Name: name}
	}
	if r := tool("rsync"); r.Path != "" {
		add(0, "rsync", "backend: %s", r.Version)
	} else {
		add(2, "rsync", "not installed on the backend (sudo apt install rsync)")
	}
	if r := tool("tmux"); r.Path != "" {
		add(0, "tmux", "backend: %s", r.Version)
	} else {
		add(1, "tmux", "not installed on the backend: detached jobs work, attach shows logs only")
	}
	if r := tool("python3"); r.Path != "" {
		add(0, "python3", "%s (%s)", r.Path, r.Version)
	}
	if len(e.GPUs) > 0 {
		add(0, "gpu", "%s", strings.Join(e.GPUs, "; "))
	} else {
		add(0, "gpu", "%s", e.GPUNote)
	}
	if !e.Writable {
		add(2, "storage", "%s is not writable", e.Root)
	} else if e.DiskFree < 1<<30 {
		add(1, "storage", "only %s free in %s", humanBytes(int64(e.DiskFree)), e.Root)
	} else {
		add(0, "storage", "%s free in %s", humanBytes(int64(e.DiskFree)), e.Root)
	}
	if ws != nil {
		resp, err := conn.Call(&protocol.Request{Op: protocol.OpStatus, Pending: &protocol.PendingRequest{ReplicaID: ws.ReplicaID()}}, nil, nil)
		if err == nil {
			s := resp.Status
			if s.Holder != nil {
				add(1, "lease", "replica busy: %s", holderText(s.Holder))
			} else {
				add(0, "lease", "replica idle")
			}
			for _, w := range s.Warnings {
				add(1, "manifest", "%s", w)
			}
		}
	}
}
