package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/chaseungjoon/recom/internal/config"
	"github.com/chaseungjoon/recom/internal/protocol"
	"github.com/chaseungjoon/recom/internal/remote"
	"github.com/chaseungjoon/recom/internal/rsync"
	"github.com/chaseungjoon/recom/internal/sshx"
	"github.com/chaseungjoon/recom/internal/toolchain"
)

// plans resolves the project's toolchains from its local dependency files.
func (a *app) plans(ws *config.Workspace) []toolchain.Plan {
	res := toolchain.Resolve(ws.Root, ws.Project.Setup.Toolchain())
	for _, h := range res.Hints {
		a.debug("%s", h)
	}
	return res.Plans
}

// runPath is [run] path plus the toolchains' directories (e.g. .venv/bin).
func runPath(ws *config.Workspace, plans []toolchain.Plan) []string {
	out := append([]string{}, ws.Project.Run.Path...)
	for _, d := range toolchain.Paths(plans) {
		if !contains(out, d) {
			out = append(out, d)
		}
	}
	return out
}

// setupPath is the PATH for toolchain setup steps: [run] path without the
// toolchain directories, so that e.g. a half-built .venv/bin/python3 never
// shadows the interpreter that creates the virtualenv.
func setupPath(ws *config.Workspace, plans []toolchain.Plan) []string {
	own := toolchain.Paths(plans)
	var out []string
	for _, d := range ws.Project.Run.Path {
		if !contains(own, d) {
			out = append(out, d)
		}
	}
	return out
}

// runEnv is the toolchains' environment (e.g. RUBYOPT) overridden by [run] env.
func runEnv(ws *config.Workspace, plans []toolchain.Plan) map[string]string {
	if len(ws.Project.Run.Env) == 0 && !anyEnv(plans) {
		return nil
	}
	m := map[string]string{}
	for _, p := range plans {
		for k, v := range p.Env {
			m[k] = v
		}
	}
	for k, v := range ws.Project.Run.Env {
		m[k] = v
	}
	return m
}

func anyEnv(plans []toolchain.Plan) bool {
	for _, p := range plans {
		if len(p.Env) > 0 {
			return true
		}
	}
	return false
}

func relEnv(plans []toolchain.Plan) map[string]string {
	var m map[string]string
	for _, p := range plans {
		for k, v := range p.RelEnv {
			if m == nil {
				m = map[string]string{}
			}
			m[k] = v
		}
	}
	return m
}

// pathFallback is where the backend's version managers keep tools.
func pathFallback(plans []toolchain.Plan) []string { return toolchain.Fallbacks(plans) }

func syncExcludes(plans []toolchain.Plan) []string {
	var out []string
	for _, p := range plans {
		out = append(out, p.SyncExclude...)
	}
	return out
}

func outputExcludes(plans []toolchain.Plan) []string {
	var out []string
	for _, p := range plans {
		out = append(out, p.OutputExclude...)
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// installable returns the plans that have something to install.
func installable(plans []toolchain.Plan) []toolchain.Plan {
	var out []toolchain.Plan
	for _, p := range plans {
		if p.Script != "" {
			out = append(out, p)
		}
	}
	return out
}

// runSetup runs every setup step whose fingerprint differs from the stamp
// the backend reported (all of them with force). It needs the lease.
func (a *app) runSetup(conn *remote.Conn, ws *config.Workspace, t sshx.Target, plans []toolchain.Plan, stamps map[string]string, force bool) error {
	for _, p := range installable(plans) {
		if !force && stamps[p.Name] == p.Fingerprint {
			a.debug("%s environment is up to date on %s", p.Name, t.Name)
			continue
		}
		custom := p.Name == toolchain.Custom
		what := p.Name + " environment"
		from := ""
		if len(p.Sources) > 0 {
			from = " from " + strings.Join(p.Sources, ", ")
		}
		switch {
		case custom:
			what = "[setup] command"
			a.info("running the [setup] command on %s: %s", t.Name, p.Summary)
		case stamps[p.Name] != "":
			a.info("updating the %s on %s%s: %s", what, t.Name, from, p.Summary)
		default:
			a.info("setting up the %s on %s%s: %s", what, t.Name, from, p.Summary)
		}
		path := setupPath(ws, plans)
		if custom {
			path = runPath(ws, plans)
		}
		req := &protocol.ExecRequest{
			Argv: []string{"/bin/sh", "-c", p.FullScript()}, Display: "recom setup " + p.Name, Cwd: ".",
			Env: ws.Project.Run.Env, Path: path, PathFallback: toolchain.Fallbacks(plans), EnvMode: ws.Project.Run.EnvMode,
			Policy: protocol.OutputPolicy{NoCollection: true}, Client: ownerName(ws), Local: ws.Root,
		}
		start := time.Now()
		exit, err := a.execStreaming(conn, req, os.Stderr)
		if err != nil {
			var pe *protocol.Error
			if errors.As(err, &pe) && pe.Code == protocol.ErrExec {
				return exitWith(ExitPreRun, fmt.Errorf("could not start the %s setup on %s: %s; the command was not run", p.Name, t.Name, pe.Message))
			}
			return connErr(err)
		}
		for _, w := range exit.Warnings {
			if !strings.Contains(w, "not tracked") {
				a.warn("%s", w)
			}
		}
		if exit.Signal == "SIGINT" || exit.Signal == "SIGKILL" || exit.ExitCode == 130 {
			return exitWith(130, fmt.Errorf("%s setup interrupted; the command was not run", what))
		}
		if exit.ExitCode != 0 {
			fix, failed := "fix the dependency files and run again", what+" setup failed"
			if custom {
				fix, failed = "fix [setup] command in .recom.toml and run again", what+" failed"
			}
			return exitWith(ExitPreRun, fmt.Errorf("%s on %s (exit %d); the command was not run.\n"+
				"  %s (`recom setup` retries just the setup),\n"+
				"  prepare it by hand with `recom --shell '...'`, or skip it once with `recom --no-setup <command>`",
				failed, t.Name, exit.ExitCode, fix))
		}
		done := "ready"
		if custom {
			done = "done"
		}
		a.info("%s %s (%s)", what, done, time.Since(start).Round(100*time.Millisecond))
	}
	return nil
}

// prepareBackend uploads the project and runs its setup steps now.
func (a *app) prepareBackend(ws *config.Workspace, plans []toolchain.Plan, force, clean bool) error {
	t, err := a.target(ws)
	if err != nil {
		return exitWith(ExitPreRun, err)
	}
	rs, err := rsync.Detect()
	if err != nil {
		return exitWith(ExitPreRun, err)
	}
	conn, err := a.dial(t)
	if err != nil {
		return connErr(err)
	}
	defer conn.Close()
	open := &protocol.OpenRequest{ReplicaID: ws.ReplicaID(), ProjectID: ws.Project.ID, Create: true, Lock: true, Wait: true, Owner: ownerName(ws)}
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
	st := ws.LoadHostState(t.Name)
	st.ReplicaPath = or.ReplicaPath
	protected := a.collectPending(conn, ws, t, rs, or)
	if err := a.upload(ws, t, rs, or, st, protected, plans); err != nil {
		return err
	}
	if err := ws.SaveHostState(st); err != nil {
		a.debug("save state: %v", err)
	}
	if clean {
		if err := a.cleanEnvironments(conn, ws, t, plans); err != nil {
			return err
		}
		force = true
	}
	return a.runSetup(conn, ws, t, plans, or.Setup, force)
}

// cleanEnvironments deletes the toolchains' environment directories in the
// replica so that the next setup builds them from scratch.
func (a *app) cleanEnvironments(conn *remote.Conn, ws *config.Workspace, t sshx.Target, plans []toolchain.Plan) error {
	var dirs []string
	for _, p := range plans {
		dirs = append(dirs, p.EnvDirs...)
	}
	if len(dirs) == 0 {
		return nil
	}
	a.info("removing %s on %s", strings.Join(dirs, ", "), t.Name)
	q := make([]string, len(dirs))
	for i, d := range dirs {
		q[i] = toolchain.Quote(d)
	}
	req := &protocol.ExecRequest{
		Argv: []string{"/bin/sh", "-c", "rm -rf -- " + strings.Join(q, " ") + " " + toolchain.StampDir}, Display: "recom setup --clean", Cwd: ".",
		EnvMode: "ssh", Policy: protocol.OutputPolicy{NoCollection: true}, Client: ownerName(ws), Local: ws.Root,
	}
	exit, err := a.execStreaming(conn, req, os.Stderr)
	if err != nil {
		return connErr(err)
	}
	if exit.ExitCode != 0 {
		return exitWith(ExitPreRun, fmt.Errorf("could not remove the old environments on %s (exit %d)", t.Name, exit.ExitCode))
	}
	return nil
}

func setupCmd(gf *globalFlags) *cobra.Command {
	var force, clean bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Upload the project and prepare its environment on the backend now",
		Long: `Upload the project and install its dependencies on the backend, the same
step every run performs automatically before the command when the
dependency files have changed: a Python .venv from requirements.txt,
pyproject.toml, uv.lock, poetry.lock or Pipfile; node_modules from
package.json; cargo fetch, go mod download, bundle install, and so on (see
docs/environments.md). Toolchains come from [setup] in .recom.toml.

  --force  run every step even if nothing changed
  --clean  delete the environments (.venv, node_modules, ...) first and
           rebuild them from scratch`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a := newApp(cmd, gf)
			ws, err := a.workspace()
			if err != nil {
				return exitWith(ExitPreRun, err)
			}
			res := toolchain.Resolve(ws.Root, ws.Project.Setup.Toolchain())
			for _, h := range res.Hints {
				a.info("%s", h)
			}
			if len(installable(res.Plans)) == 0 {
				a.info("nothing to set up: no dependency files found at the project root (requirements.txt, pyproject.toml, environment.yml, package.json, Cargo.toml, go.mod, Gemfile, ...) and no [setup] command")
				return nil
			}
			return a.prepareBackend(ws, res.Plans, force, clean)
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "run every setup step even if the dependency files did not change")
	cmd.Flags().BoolVar(&clean, "clean", false, "delete the backend environments (.venv, node_modules, ...) and rebuild them")
	return cmd
}

// describePlans prints what recom detected, for init and status.
func (a *app) describePlans(plans []toolchain.Plan) {
	for _, p := range plans {
		if p.Name == toolchain.Custom {
			continue
		}
		a.info("detected %s (%s): %s", p.Name, strings.Join(p.Sources, ", "), p.Summary)
	}
}

// initSetup prepares the backend right after `recom init` when there is
// something to install and a backend is configured.
func (a *app) initSetup(ws *config.Workspace, plans []toolchain.Plan, skip bool) error {
	g, _ := a.loadGlobal()
	switch {
	case g == nil || len(g.Hosts) == 0:
		a.info("next: register a backend with `recom ssh <alias>`, then run `recom <command>` (the first run sets up the environment)")
		return nil
	case len(installable(plans)) == 0:
		a.info("next: `recom <command>` — the first run uploads the project")
		return nil
	case skip:
		a.info("next: `recom <command>` — the first run uploads the project and sets up its environment")
		return nil
	}
	if _, err := a.hostName(ws); err != nil {
		a.info("%v", err)
		a.info("the environment is set up on your first `recom <command>`")
		return nil
	}
	a.info("preparing the backend now (skip with `recom init --no-setup`)")
	err := a.prepareBackend(ws, plans, false, false)
	var ee *exitError
	if errors.As(err, &ee) && ee.code == ExitTransport {
		a.warn("could not reach the backend (%v); the environment will be set up on your first `recom <command>`", ee.err)
		return nil
	}
	if err == nil {
		a.info("next: `recom <command>`")
	}
	return err
}
