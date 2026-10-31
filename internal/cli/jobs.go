package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/chaseungjoon/recom/internal/config"
	"github.com/chaseungjoon/recom/internal/protocol"
	"github.com/chaseungjoon/recom/internal/remote"
	"github.com/chaseungjoon/recom/internal/sshx"
)

// projectConn opens a helper session for the current project.
func (a *app) projectConn() (*config.Workspace, sshx.Target, *remote.Conn, error) {
	ws, err := a.workspace()
	if err != nil {
		return nil, sshx.Target{}, nil, exitWith(ExitPreRun, err)
	}
	t, err := a.target(ws)
	if err != nil {
		return nil, sshx.Target{}, nil, exitWith(ExitPreRun, err)
	}
	conn, err := a.dial(t)
	if err != nil {
		return nil, sshx.Target{}, nil, connErr(err)
	}
	return ws, t, conn, nil
}

func jobState(j protocol.JobInfo) string {
	switch j.State {
	case protocol.JobExited, protocol.JobStopped:
		if j.ExitCode != nil {
			if j.Signal != "" {
				return fmt.Sprintf("%s (%s)", j.State, j.Signal)
			}
			return fmt.Sprintf("%s (%d)", j.State, *j.ExitCode)
		}
	}
	return j.State
}

func jobsCmd(gf *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "jobs",
		Short: "List detached jobs of this project on the backend",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a := newApp(cmd, gf)
			ws, t, conn, err := a.projectConn()
			if err != nil {
				return err
			}
			defer conn.Close()
			resp, err := conn.Call(&protocol.Request{Op: protocol.OpJobs, Job: &protocol.JobRequest{ReplicaID: ws.ReplicaID()}}, nil, nil)
			if err != nil {
				return connErr(err)
			}
			if gf.json {
				if resp.Jobs == nil {
					resp.Jobs = []protocol.JobInfo{}
				}
				return json.NewEncoder(a.out).Encode(resp.Jobs)
			}
			if len(resp.Jobs) == 0 {
				fmt.Fprintf(a.out, "No detached jobs for this project on %s. Start one with: recom --detach <command>\n", t.Name)
				return nil
			}
			tw := tabwriter.NewWriter(a.out, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tSTATE\tSTARTED\tDURATION\tOUTPUTS\tCOMMAND")
			pendingJobs := 0
			for _, j := range resp.Jobs {
				outs := "-"
				if j.Pending > 0 {
					outs = fmt.Sprintf("%d pending", j.Pending)
					pendingJobs++
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", j.ID, jobState(j), ago(j.Started), dur(j.Started, j.Ended), outs, firstN(j.Command, 60))
			}
			tw.Flush()
			if pendingJobs > 0 {
				fmt.Fprintln(a.out, "\nPending outputs are downloaded by your next `recom <command>`, or now with `recom pull`.")
			}
			return nil
		},
	}
}

func logsCmd(gf *globalFlags) *cobra.Command {
	var follow bool
	var tail int
	cmd := &cobra.Command{
		Use:   "logs <job-id>",
		Short: "Print a detached job's output (stdout and stderr)",
		Long:  "Print the durable log of a detached job. With -f, keep streaming until the job ends.\nA unique prefix of the job id is enough.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := newApp(cmd, gf)
			ws, _, conn, err := a.projectConn()
			if err != nil {
				return err
			}
			defer conn.Close()
			return a.streamLogs(conn, ws, args[0], follow, tail)
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep streaming until the job finishes")
	cmd.Flags().IntVarP(&tail, "tail", "n", 0, "only show the last N lines")
	return cmd
}

func (a *app) streamLogs(conn *remote.Conn, ws *config.Workspace, id string, follow bool, tail int) error {
	resp, err := conn.Call(&protocol.Request{Op: protocol.OpLogs, Job: &protocol.JobRequest{ReplicaID: ws.ReplicaID(), JobID: id, Follow: follow, Tail: tail}}, os.Stdout, os.Stderr)
	if err != nil {
		return connErr(err)
	}
	if j := resp.Job; j != nil && j.State != protocol.JobRunning && j.State != protocol.JobStarting {
		a.info("job %s %s", j.ID, describeEnd(*j))
	}
	return nil
}

func describeEnd(j protocol.JobInfo) string {
	s := jobState(j)
	if j.Pending > 0 {
		s += fmt.Sprintf("; %d output(s) pending — collected by your next recom command or `recom pull`", j.Pending)
	}
	return s
}

func attachCmd(gf *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "attach <job-id>",
		Short: "Attach to a running detached job (tmux); otherwise follow its logs",
		Long: `Attach your terminal to a running detached job's tmux session. Detach again
with the backend's tmux prefix then d (Ctrl-b d by default; recom prints the
actual key); the job keeps running. Ctrl-C is delivered to the job.

Jobs started without tmux (not installed on the backend) cannot take input:
attach then follows their logs. For a finished job, attach prints its log.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := newApp(cmd, gf)
			ws, t, conn, err := a.projectConn()
			if err != nil {
				return err
			}
			resp, err := conn.Call(&protocol.Request{Op: protocol.OpJob, Job: &protocol.JobRequest{ReplicaID: ws.ReplicaID(), JobID: args[0]}}, nil, nil)
			if err != nil {
				conn.Close()
				return connErr(err)
			}
			j := resp.Job
			switch {
			case j.State == protocol.JobRunning && j.Tmux:
				conn.Close()
				prefix := j.Prefix
				if prefix == "" {
					prefix = "C-b"
				}
				a.info("attaching to job %s on %s — detach with %s then d (the job keeps running)", j.ID, t.Name, prefix)
				remoteCmd := sshx.Quote(j.TmuxPath) + " -L " + sshx.Quote(j.Socket) + " attach-session -t " + sshx.Quote("="+j.Session)
				c := t.Command(a.ctx, true, remoteCmd)
				c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
				if tv := os.Getenv("TERM"); tv == "" || tv == "dumb" {
					// tmux refuses to attach to a terminal it cannot drive.
					c.Env = append(os.Environ(), "TERM=xterm-256color")
				}
				if err := c.Run(); err != nil {
					var ee *exec.ExitError
					if errors.As(err, &ee) {
						return exitWith(ee.ExitCode(), fmt.Errorf("tmux attach ended with status %d", ee.ExitCode()))
					}
					return connErr(err)
				}
				return nil
			case j.State == protocol.JobRunning || j.State == protocol.JobStarting:
				defer conn.Close()
				a.info("job %s has no tmux session (tmux missing on the backend); following its logs instead (Ctrl-C stops following, not the job)", j.ID)
				return a.streamLogs(conn, ws, j.ID, true, 0)
			default:
				defer conn.Close()
				a.info("job %s is no longer running (%s); showing its log", j.ID, jobState(*j))
				return a.streamLogs(conn, ws, j.ID, false, 0)
			}
		},
	}
}

func stopCmd(gf *globalFlags) *cobra.Command {
	var grace time.Duration
	cmd := &cobra.Command{
		Use:   "stop <job-id>",
		Short: "Stop a detached job (SIGTERM, then SIGKILL after a grace period)",
		Long: `Stop a detached job started by recom. recom only signals the process group it
recorded for that job, after verifying the process identity; it never kills
arbitrary processes. Outputs written before the stop are still recorded.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := newApp(cmd, gf)
			ws, _, conn, err := a.projectConn()
			if err != nil {
				return err
			}
			defer conn.Close()
			resp, err := conn.Call(&protocol.Request{Op: protocol.OpStop, Job: &protocol.JobRequest{ReplicaID: ws.ReplicaID(), JobID: args[0], Grace: grace}}, nil, nil)
			if err != nil {
				return connErr(err)
			}
			j := resp.Job
			if strings.HasPrefix(j.Message, "job is not running") {
				a.info("%s: %s; nothing to stop", j.ID, j.Message)
				return nil
			}
			switch j.State {
			case protocol.JobRunning, protocol.JobStarting:
				return exitWith(1, fmt.Errorf("job %s is still running after the stop request", j.ID))
			default:
				a.info("job %s %s", j.ID, describeEnd(*j))
			}
			return nil
		},
	}
	cmd.Flags().DurationVar(&grace, "grace", 10*time.Second, "time to wait after SIGTERM before SIGKILL")
	return cmd
}
