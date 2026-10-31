package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/chaseungjoon/recom/internal/config"
	"github.com/chaseungjoon/recom/internal/protocol"
	"github.com/chaseungjoon/recom/internal/remote"
	"github.com/chaseungjoon/recom/internal/sshx"
)

type app struct {
	cmd    *cobra.Command
	gf     *globalFlags
	out    io.Writer
	err    io.Writer
	color  bool
	ctx    context.Context
	global *config.Global
}

func newApp(cmd *cobra.Command, gf *globalFlags) *app {
	a := &app{cmd: cmd, gf: gf, out: cmd.OutOrStdout(), err: cmd.ErrOrStderr(), ctx: context.Background()}
	if f, ok := a.err.(*os.File); ok && term.IsTerminal(int(f.Fd())) && os.Getenv("NO_COLOR") == "" {
		a.color = true
	}
	return a
}

func (a *app) paint(code, s string) string {
	if !a.color {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (a *app) prefix() string { return a.paint("2", "recom:") }

// info prints a progress message to stderr unless --quiet.
func (a *app) info(format string, args ...any) {
	if a.gf.quiet {
		return
	}
	fmt.Fprintf(a.err, "%s %s\n", a.prefix(), fmt.Sprintf(format, args...))
}

// warn always prints.
func (a *app) warn(format string, args ...any) {
	fmt.Fprintf(a.err, "%s %s %s\n", a.prefix(), a.paint("33", "warning:"), fmt.Sprintf(format, args...))
}

func (a *app) debug(format string, args ...any) {
	if a.gf.verbose {
		fmt.Fprintf(a.err, "%s %s\n", a.paint("2", "recom[debug]:"), fmt.Sprintf(format, args...))
	}
}

func (a *app) loadGlobal() (*config.Global, error) {
	if a.global != nil {
		return a.global, nil
	}
	g, err := config.LoadGlobal()
	if err != nil {
		return nil, err
	}
	a.global = g
	return g, nil
}

// workspace discovers the project containing the current directory.
func (a *app) workspace() (*config.Workspace, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	ws, err := config.Discover(cwd)
	if err != nil {
		return nil, err
	}
	if ws.Notice != "" {
		a.info("%s", ws.Notice)
	}
	return ws, nil
}

// hostName resolves the backend: --host > RECOM_HOST > project host > default.
func (a *app) hostName(ws *config.Workspace) (string, error) {
	g, err := a.loadGlobal()
	if err != nil {
		return "", err
	}
	name := a.gf.host
	if name == "" {
		name = os.Getenv("RECOM_HOST")
	}
	if name == "" && ws != nil {
		name = ws.Project.Host
	}
	if name == "" {
		name = g.DefaultHost
	}
	if name == "" {
		switch len(g.Hosts) {
		case 0:
			return "", errors.New("no backend registered; run `recom ssh <alias>` (an SSH alias or user@host)")
		case 1:
			return g.HostNames()[0], nil
		default:
			return "", fmt.Errorf("several backends are registered (%s) but none is the default; run `recom default <name>` or pass --host", strings.Join(g.HostNames(), ", "))
		}
	}
	if _, ok := g.Hosts[name]; !ok {
		known := "none registered yet"
		if len(g.Hosts) > 0 {
			known = "known: " + strings.Join(g.HostNames(), ", ")
		}
		return "", fmt.Errorf("unknown backend %q (%s); register it with `recom ssh %s`", name, known, name)
	}
	return name, nil
}

func (a *app) target(ws *config.Workspace) (sshx.Target, error) {
	name, err := a.hostName(ws)
	if err != nil {
		return sshx.Target{}, err
	}
	return sshx.Target{Name: name, Host: a.global.Hosts[name]}, nil
}

func (a *app) dial(t sshx.Target) (*remote.Conn, error) {
	ctx, cancel := context.WithTimeout(a.ctx, 60*time.Second)
	defer cancel()
	return remote.Dial(ctx, t, remote.Options{
		Verbose: a.gf.verbose,
		Log:     a.debug,
		OnEvent: a.onEvent,
	})
}

func (a *app) onEvent(ev protocol.Event) {
	switch ev.Kind {
	case "warning":
		a.warn("%s", ev.Message)
	case "waiting":
		a.info("%s (Ctrl-C to give up)", ev.Message)
	default:
		a.info("%s", ev.Message)
	}
}

// connErr classifies a dial/call failure into an exit code.
func connErr(err error) error {
	var te *remote.TransportError
	if errors.As(err, &te) {
		return exitWith(ExitTransport, err)
	}
	return exitWith(ExitPreRun, err)
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return t.Local().Format("2006-01-02")
}

func dur(a, b time.Time) string {
	if a.IsZero() {
		return "-"
	}
	if b.IsZero() {
		b = time.Now()
	}
	d := b.Sub(a).Round(time.Second)
	return d.String()
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
