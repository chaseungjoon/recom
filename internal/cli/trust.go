package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/chaseungjoon/recom/internal/config"
)

// ensureTrusted makes sure the user approved the backend commands that the
// project's .recom.toml sets (setup.command, run.wrapper, run.env,
// run.path). A cloned repository could otherwise run code on the backend
// whatever command the user types.
func (a *app) ensureTrusted(ws *config.Workspace) error {
	if ws == nil || ws.Trusted() {
		return nil
	}
	if a.gf.trust {
		return a.trust(ws)
	}
	in, inOK := a.cmd.InOrStdin().(*os.File)
	if !inOK || !term.IsTerminal(int(in.Fd())) || !a.isTerminal() {
		return exitWith(ExitPreRun, fmt.Errorf("%s sets commands that run on the backend and they are not approved for this working copy:\n  %s\nreview them, then run `recom trust` (or pass --trust)",
			config.ProjectFile, strings.Join(ws.TrustSummary(), "\n  ")))
	}
	a.printTrustSummary(ws)
	fmt.Fprintf(a.err, "%s trust these settings for %s? [y/N] ", a.prefix(), ws.Root)
	line, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return a.trust(ws)
	}
	return exitWith(ExitPreRun, fmt.Errorf("not approved; nothing was run (review %s, then run `recom trust`)", config.ProjectFile))
}

func (a *app) isTerminal() bool {
	f, ok := a.err.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func (a *app) printTrustSummary(ws *config.Workspace) {
	fmt.Fprintf(a.err, "%s %s sets commands that run on the backend:\n", a.prefix(), config.ProjectFile)
	for _, l := range ws.TrustSummary() {
		fmt.Fprintf(a.err, "    %s\n", l)
	}
}

func (a *app) trust(ws *config.Workspace) error {
	if err := ws.Trust(); err != nil {
		return exitWith(ExitPreRun, fmt.Errorf("record approval: %w", err))
	}
	a.info("approved %s for %s", config.ProjectFile, ws.Root)
	return nil
}

func trustCmd(gf *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "trust",
		Short: "Approve the backend commands set by this project's .recom.toml",
		Long: `Approve the settings in .recom.toml that run code on the backend for every
command: setup.command, run.wrapper, run.env and run.path. A repository you
clone can set these, so recom asks before using them in a new working copy
and again whenever they change. Approvals are stored per working copy in
your recom config directory, never in the project.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a := newApp(cmd, gf)
			ws, err := a.workspace()
			if err != nil {
				return exitWith(ExitPreRun, err)
			}
			if !ws.NeedsTrust() {
				a.info("%s sets no backend commands; nothing to approve", config.ProjectFile)
				return nil
			}
			a.printTrustSummary(ws)
			return a.trust(ws)
		},
	}
}
