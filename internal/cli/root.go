// Package cli implements the recom command line.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/chaseungjoon/recom/internal/agent"
	"github.com/chaseungjoon/recom/internal/buildinfo"
)

// Exit codes (documented in docs/cli.md). Remote command exit codes 0-255
// pass through unchanged; recom's own failures use these.
const (
	ExitUsage     = 2   // invalid flags/arguments
	ExitOutputs   = 75  // command succeeded but outputs could not all be collected
	ExitPreRun    = 125 // recom failed before the command started (config, sync, lock, helper)
	ExitTransport = 255 // SSH connection failed or was lost
)

// exitError carries an exit code through cobra.
type exitError struct {
	code   int
	err    error
	silent bool
}

func (e *exitError) Error() string {
	if e.err == nil {
		return fmt.Sprintf("exit status %d", e.code)
	}
	return e.err.Error()
}

func (e *exitError) Unwrap() error { return e.err }

func exitWith(code int, err error) error { return &exitError{code: code, err: err} }
func exitSilently(code int) error        { return &exitError{code: code, silent: true} }

type globalFlags struct {
	host    string
	verbose bool
	quiet   bool
	json    bool

	noSync    bool
	noSetup   bool
	noCollect bool
	tty       bool
	detach    bool
	shell     string
}

const longHelp = `recom runs commands on a remote Linux machine over SSH while you keep
editing locally. Every run automatically uploads changed project files first,
installs the project's dependencies on the backend when they changed
(requirements.txt → .venv, package.json → node_modules, ...), executes the
command only on the backend, streams its output, and then downloads the
files that this run created or modified.

  recom ssh aiworkstation        register an SSH backend (uses ~/.ssh/config)
  recom init                     make the current directory a project and
                                 prepare its environment on the backend
  recom python3 src/run.py       sync, run remotely, bring outputs home

Anything after recom that is not a recom subcommand is the remote command,
with its arguments passed through exactly. Use "recom -- <cmd>" when the
command's name collides with a subcommand (e.g. "recom -- env" or
"recom -- ssh other-host"). recom's own flags must come before the command.

Redirection happens where the shell runs:
  recom python3 run.py > log.txt          writes log.txt on THIS machine
  recom --shell 'python3 run.py > log.txt' writes it remotely; it then comes
                                           back as a run output

Exit status is the remote command's status. recom's own failures use:
  125 failed before the command started   255 SSH connection failed or lost
   75 command succeeded but some outputs could not be collected
    2 usage error`

// NewRoot builds the command tree.
func NewRoot(stdout, stderr io.Writer) *cobra.Command {
	gf := &globalFlags{}
	root := &cobra.Command{
		Use:                   "recom [flags] <command> [args...]",
		Short:                 "Write locally. Compute remotely. Bring generated files home.",
		Long:                  longHelp,
		Args:                  cobra.ArbitraryArgs,
		SilenceUsage:          true,
		SilenceErrors:         true,
		DisableFlagsInUseLine: true,
		Version:               versionString(),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && gf.shell == "" {
				return cmd.Help()
			}
			return runRemote(newApp(cmd, gf), args)
		},
	}
	root.SuggestionsMinimumDistance = 2
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetVersionTemplate("recom {{.Version}}\n")
	root.Flags().SetInterspersed(false)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return exitWith(ExitUsage, err) })

	pf := root.PersistentFlags()
	pf.StringVarP(&gf.host, "host", "H", "", "backend to use for this invocation (overrides project and default)")
	pf.BoolVarP(&gf.verbose, "verbose", "v", false, "show what recom is doing (ssh/rsync commands, file lists)")
	pf.BoolVarP(&gf.quiet, "quiet", "q", false, "only print errors and warnings from recom")
	pf.BoolVar(&gf.json, "json", false, "machine-readable output for administrative commands")

	f := root.Flags()
	f.BoolVar(&gf.noSync, "no-sync", false, "skip the pre-run upload (e.g. recom --no-sync nvidia-smi)")
	f.BoolVar(&gf.noSetup, "no-setup", false, "skip the automatic environment setup (venv, node_modules, ...) for this run")
	f.BoolVar(&gf.noCollect, "no-collect", false, "do not download outputs after the run (they stay pending)")
	f.BoolVarP(&gf.tty, "tty", "t", false, "run in a remote pseudo-terminal (interactive programs, colors)")
	f.BoolVarP(&gf.detach, "detach", "d", false, "start a persistent remote job and print its id")
	f.StringVar(&gf.shell, "shell", "", "run a remote shell expression (pipes, redirection, globbing)")

	root.AddGroup(&cobra.Group{ID: "setup", Title: "Setup:"}, &cobra.Group{ID: "jobs", Title: "Jobs:"}, &cobra.Group{ID: "info", Title: "Inspection:"})
	add := func(group string, cmds ...*cobra.Command) {
		for _, c := range cmds {
			c.GroupID = group
			root.AddCommand(c)
		}
	}
	add("setup", sshCmd(gf), hostsCmd(gf), defaultCmd(gf), initCmd(gf), setupCmd(gf), updateCmd(gf))
	add("jobs", jobsCmd(gf), logsCmd(gf), attachCmd(gf), stopCmd(gf), pullCmd(gf))
	add("info", statusCmd(gf), envCmd(gf), doctorCmd(gf), versionCmd(gf))
	root.AddCommand(&cobra.Command{
		Use:                "__agent",
		Hidden:             true,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			os.Exit(agent.Main(args))
			return nil
		},
	})
	root.SetHelpCommandGroupID("info")
	root.SetCompletionCommandGroupID("info")
	return root
}

func versionString() string {
	v := buildinfo.Version
	if c := buildinfo.ShortCommit(); c != "" {
		v += " (" + c + ")"
	}
	return v
}

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	root := NewRoot(os.Stdout, os.Stderr)
	err := root.Execute()
	if err == nil {
		return 0
	}
	var ee *exitError
	if errors.As(err, &ee) {
		if !ee.silent && ee.err != nil {
			fmt.Fprintln(os.Stderr, "recom: "+ee.err.Error())
		}
		return ee.code
	}
	fmt.Fprintln(os.Stderr, "recom: "+err.Error())
	return 1
}
