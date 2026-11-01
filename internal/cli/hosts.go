package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/chaseungjoon/recom/internal/agent"
	"github.com/chaseungjoon/recom/internal/config"
	"github.com/chaseungjoon/recom/internal/protocol"
	"github.com/chaseungjoon/recom/internal/remote"
	"github.com/chaseungjoon/recom/internal/sshx"
	"github.com/chaseungjoon/recom/internal/toolchain"
)

func sshCmd(gf *globalFlags) *cobra.Command {
	var name, identity, root string
	var port int
	var opts []string
	cmd := &cobra.Command{
		Use:   "ssh <alias | user@host>",
		Short: "Register an SSH backend (reuses ~/.ssh/config; never modifies it)",
		Long: `Register an existing SSH target as a recom backend. The target is passed
to your ssh binary as-is, so ~/.ssh/config aliases, agents, keys, ProxyJump,
known_hosts and Tailscale all work. recom connects once to discover the remote
home directory and platform, installs its small helper under
~/.local/share/recom on the backend, and checks for rsync and tmux.

The first registered backend becomes the default.`,
		Example: "  recom ssh aiworkstation\n  recom ssh me@10.0.0.5 --port 2222 --identity ~/.ssh/id_work --name gpu",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := newApp(cmd, gf)
			target := args[0]
			if err := sshx.ValidTarget(target); err != nil {
				return exitWith(ExitUsage, err)
			}
			if name == "" {
				name = target
				if i := strings.LastIndexByte(name, '@'); i >= 0 {
					name = name[i+1:]
				}
			}
			if !config.ValidHostName(name) {
				return exitWith(ExitUsage, fmt.Errorf("cannot derive a backend name from %q; pass --name", target))
			}
			for _, o := range opts {
				if err := sshx.ValidOption(o); err != nil {
					return exitWith(ExitUsage, err)
				}
			}
			if identity != "" {
				abs, err := filepath.Abs(expandHome(identity))
				if err != nil {
					return err
				}
				if _, err := os.Stat(abs); err != nil {
					return exitWith(ExitUsage, fmt.Errorf("identity file: %w", err))
				}
				identity = abs
			}
			g, err := a.loadGlobal()
			if err != nil {
				return err
			}
			h := &config.Host{Target: target, Port: port, Identity: identity, SSHOptions: opts, Added: time.Now().UTC().Truncate(time.Second)}
			t := sshx.Target{Name: name, Host: h}
			a.info("connecting to %s …", target)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			info, err := remote.Probe(ctx, t)
			if err != nil {
				return connErr(err)
			}
			if strings.TrimSpace(info.Noise) != "" {
				a.warn("the remote shell prints text on non-interactive logins (%q); recom copes with it, but rsync may not — consider guarding it in your shell rc file", firstN(strings.TrimSpace(info.Noise), 60))
			}
			if info.OS != "linux" {
				return exitWith(ExitPreRun, fmt.Errorf("%s runs %s; recom backends must run Linux", target, info.OS))
			}
			if info.Arch != "amd64" && info.Arch != "arm64" {
				return exitWith(ExitPreRun, fmt.Errorf("%s has unsupported architecture %s (supported: amd64, arm64)", target, info.Arch))
			}
			h.OS, h.Arch, h.RemoteHome = info.OS, info.Arch, info.Home
			if root == "" {
				root = strings.TrimRight(info.Home, "/") + "/.local/share/recom"
			}
			if err := agent.ValidateRoot(root); err != nil {
				return exitWith(ExitUsage, fmt.Errorf("%v: the remote home %q contains unusual characters; pass --remote-root with a plain absolute path", err, info.Home))
			}
			h.RemoteRoot = root

			conn, err := remote.Dial(ctx, t, remote.Options{Verbose: gf.verbose, Log: a.debug})
			if err != nil {
				return connErr(err)
			}
			resp, err := conn.Call(&protocol.Request{Op: protocol.OpEnv, Env: &protocol.EnvRequest{EnvMode: agent.EnvSSH}}, nil, nil)
			conn.Close()
			if err != nil {
				return connErr(err)
			}
			env := resp.Env
			if !env.RsyncOK {
				return exitWith(ExitPreRun, fmt.Errorf("rsync is not installed on %s; install it there (e.g. `sudo apt install rsync`) and run `recom ssh %s` again", target, target))
			}
			if !env.Writable {
				return exitWith(ExitPreRun, fmt.Errorf("cannot write to %s on %s", root, target))
			}
			_, existed := g.Hosts[name]
			g.Hosts[name] = h
			becameDefault := false
			if g.DefaultHost == "" {
				g.DefaultHost = name
				becameDefault = true
			}
			if err := g.Save(); err != nil {
				return err
			}
			verb := "registered"
			if existed {
				verb = "updated"
			}
			a.info("%s backend %q → %s (%s, %s/%s)", verb, name, target, env.OS, info.OS, info.Arch)
			a.info("remote data lives in %s", root)
			if len(env.GPUs) > 0 {
				a.info("GPU: %s", strings.Join(env.GPUs, "; "))
			}
			if !env.TmuxOK {
				a.warn("tmux is not installed on %s: detached jobs will work, but `recom attach` will only show logs", name)
			}
			if becameDefault {
				a.info("%q is now the default backend", name)
			} else if g.DefaultHost != name {
				a.info("make it the default with: recom default %s", name)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "name for the backend (default: the alias or host part of the target)")
	cmd.Flags().IntVarP(&port, "port", "p", 0, "SSH port (default: from ~/.ssh/config or 22)")
	cmd.Flags().StringVarP(&identity, "identity", "i", "", "SSH identity file (default: from ~/.ssh/config / agent)")
	cmd.Flags().StringVar(&root, "remote-root", "", "absolute directory on the backend for replicas and state (default ~/.local/share/recom)")
	cmd.Flags().StringArrayVarP(&opts, "option", "o", nil, "extra ssh -o option (repeatable), e.g. -o ProxyJump=bastion")
	return cmd
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return p
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

type hostJSON struct {
	Name       string `json:"name"`
	Default    bool   `json:"default"`
	Target     string `json:"ssh_target"`
	Port       int    `json:"port,omitempty"`
	Identity   string `json:"identity,omitempty"`
	Platform   string `json:"platform"`
	RemoteRoot string `json:"remote_root"`
}

func hostsCmd(gf *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hosts",
		Short: "List registered backends",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a := newApp(cmd, gf)
			g, err := a.loadGlobal()
			if err != nil {
				return err
			}
			var list []hostJSON
			for _, n := range g.HostNames() {
				h := g.Hosts[n]
				list = append(list, hostJSON{n, n == g.DefaultHost, h.Target, h.Port, h.Identity, h.OS + "/" + h.Arch, h.RemoteRoot})
			}
			if gf.json {
				if list == nil {
					list = []hostJSON{}
				}
				return json.NewEncoder(a.out).Encode(list)
			}
			if len(list) == 0 {
				fmt.Fprintln(a.out, "No backends registered. Add one with: recom ssh <alias>")
				return nil
			}
			tw := tabwriter.NewWriter(a.out, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tSSH TARGET\tPLATFORM\tREMOTE ROOT")
			for _, h := range list {
				n := h.Name
				if h.Default {
					n += " *"
				}
				tgt := h.Target
				if h.Port > 0 {
					tgt += fmt.Sprintf(" (port %d)", h.Port)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", n, tgt, h.Platform, h.RemoteRoot)
			}
			tw.Flush()
			fmt.Fprintln(a.out, "\n* default backend")
			return nil
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "remove <name>",
		Short: "Forget a backend (remote files are left untouched)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := newApp(cmd, gf)
			g, err := a.loadGlobal()
			if err != nil {
				return err
			}
			h, ok := g.Hosts[args[0]]
			if !ok {
				return fmt.Errorf("unknown backend %q", args[0])
			}
			delete(g.Hosts, args[0])
			if g.DefaultHost == args[0] {
				g.DefaultHost = ""
			}
			if err := g.Save(); err != nil {
				return err
			}
			a.info("removed backend %q; its replicas remain on the backend in %s", args[0], h.RemoteRoot)
			return nil
		},
	})
	return cmd
}

func defaultCmd(gf *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "default <name>",
		Short: "Set the default backend",
		Args:  cobra.ExactArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			g, err := config.LoadGlobal()
			if err != nil || len(args) > 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			return g.HostNames(), cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			a := newApp(cmd, gf)
			g, err := a.loadGlobal()
			if err != nil {
				return err
			}
			if _, ok := g.Hosts[args[0]]; !ok {
				return errors.New("unknown backend " + fmt.Sprintf("%q", args[0]) + "; register it first with `recom ssh " + args[0] + "`")
			}
			g.DefaultHost = args[0]
			if err := g.Save(); err != nil {
				return err
			}
			a.info("default backend is now %q", args[0])
			return nil
		},
	}
}

func initCmd(gf *globalFlags) *cobra.Command {
	var noSetup bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Make the current directory a recom project and prepare its backend environment",
		Long: `Create .recom.toml (a random project id plus documented settings, safe to
commit) and .recom/ (untracked working-copy state) in the current directory.

init detects the project's toolchains from its dependency files
(requirements.txt, pyproject.toml, environment.yml, package.json,
Cargo.toml, go.mod, Gemfile, ...) and records them under [setup], with
their directories (e.g. .venv/bin) in [run] path. If there is something to
install and a backend is registered, init then uploads the project and
sets up the environment on the backend right away, so the first
` + "`recom <command>`" + ` starts immediately; --no-setup leaves that to the first run.

Refuses to reinitialize an existing project. With --host, the backend is
recorded as this project's preferred backend.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a := newApp(cmd, gf)
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			plans := toolchain.Detect(cwd)
			var names []string
			for _, p := range plans {
				names = append(names, p.Name)
			}
			p, err := config.Init(cwd, gf.host, names, toolchain.Paths(plans))
			if err != nil {
				return err
			}
			if gf.host != "" {
				a.info("recorded preferred backend %q in %s", gf.host, config.ProjectFile)
			}
			a.info("initialized recom project %s", p.ID)
			a.info("created %s (commit it) and %s/ (local state, ignored by git)", config.ProjectFile, config.LocalDir)
			a.describePlans(plans)
			ws, err := a.workspace()
			if err != nil {
				return err
			}
			return a.initSetup(ws, a.plans(ws), noSetup)
		},
	}
	cmd.Flags().BoolVar(&noSetup, "no-setup", false, "only write the configuration; set up the backend on the first run")
	return cmd
}
