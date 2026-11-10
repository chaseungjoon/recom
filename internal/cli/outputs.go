package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/chaseungjoon/recom/internal/collect"
	"github.com/chaseungjoon/recom/internal/config"
	"github.com/chaseungjoon/recom/internal/pathmatch"
	"github.com/chaseungjoon/recom/internal/policy"
	"github.com/chaseungjoon/recom/internal/protocol"
	"github.com/chaseungjoon/recom/internal/remote"
	"github.com/chaseungjoon/recom/internal/rsync"
	"github.com/chaseungjoon/recom/internal/sshx"
)

// announceBytes is the download size from which collection is announced and,
// on a terminal, shows a live progress line.
const announceBytes = 16 << 20

// progressTTY reports whether a live progress line can be drawn on stderr.
func (a *app) progressTTY() bool {
	if a.gf.quiet || os.Getenv("TERM") == "dumb" {
		return false
	}
	f, ok := a.err.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

type collectSummary struct {
	collected, conflicts, failed int
	bytes                        int64
}

// collectOutputs downloads and installs items, reports each result, and
// records the outcome on the backend.
func (a *app) collectOutputs(conn *remote.Conn, ws *config.Workspace, t sshx.Target, rs *rsync.Binary, replicaPath string, items []protocol.Output, batch string, force bool) (collectSummary, error) {
	var sum collectSummary
	if len(items) == 0 {
		return sum, nil
	}
	lock, err := ws.LockLocal(t.Name, 30e9)
	if err != nil {
		return sum, err
	}
	defer lock.Unlock()

	c := &collect.Collector{Root: ws.Root, Target: t, Rsync: rs, RemoteDir: replicaPath, Force: force, Stderr: a.rsyncStderr()}
	var total int64
	for _, o := range items {
		total += o.Size
	}
	clearProgress := func() {}
	if total >= announceBytes {
		// Without this, a large collection after the command exits looks
		// like recom hanging.
		a.info("downloading %d output(s) (%s) from %s; Ctrl-C leaves them pending for `recom pull`, and [outputs] exclude in %s stops collecting them",
			len(items), humanBytes(total), t.Name, config.ProjectFile)
		if a.progressTTY() {
			drawn := false
			c.Progress = func(done, total int64) {
				drawn = true
				fmt.Fprintf(a.err, "\r\x1b[K%s %s %s / %s (%d%%)", a.prefix(), a.paint("32", "↓"), humanBytes(done), humanBytes(total), done*100/total)
			}
			clearProgress = func() {
				if drawn {
					fmt.Fprint(a.err, "\r\x1b[K")
				}
			}
		}
	}
	results, dlErr := c.Collect(a.ctx, items, batch)
	clearProgress()
	if dlErr != nil {
		a.debug("download: %v", dlErr)
	}
	var marks []protocol.Mark
	shown := 0
	for _, r := range results {
		o := r.Output
		switch r.Status {
		case collect.Collected:
			sum.collected++
			if !r.Identity {
				sum.bytes += o.Size
			}
			if shown < 15 || a.gf.verbose {
				extra := ""
				if r.Identity {
					extra = ", already up to date"
				}
				if r.Backup != "" {
					extra += ", previous local copy saved to " + r.Backup
				}
				a.info("%s %s (%s%s)", a.paint("32", "←"), a.localName(ws, o.Path), humanBytes(o.Size), extra)
			}
			shown++
			if o.RunID != "" {
				marks = append(marks, protocol.Mark{RunID: o.RunID, Path: o.Path, Status: protocol.StatusCollected})
			}
		case collect.Conflict:
			sum.conflicts++
			a.warn("conflict: %s was changed locally while the run was in progress; kept your version, remote version saved to %s", a.localName(ws, o.Path), r.Staged)
			if o.RunID != "" {
				marks = append(marks, protocol.Mark{RunID: o.RunID, Path: o.Path, Status: protocol.StatusConflict, Note: r.Staged})
			}
		case collect.Skipped:
			a.warn("skipped %s: %s", o.Path, r.Note)
		default:
			sum.failed++
			a.warn("could not collect %s: %s (still pending; it will be retried on the next run or `recom pull`)", a.localName(ws, o.Path), r.Note)
		}
	}
	if shown > 15 && !a.gf.verbose {
		a.info("… and %d more", shown-15)
	}
	if len(marks) > 0 {
		if _, err := conn.Call(&protocol.Request{Op: protocol.OpMark, Mark: &protocol.MarkRequest{ReplicaID: ws.ReplicaID(), Items: marks}}, nil, nil); err != nil {
			a.warn("could not record collected outputs on the backend: %v", err)
		}
	}
	return sum, nil
}

// pendingPolicyMarks re-applies the current [outputs] rules to outputs that
// earlier runs left pending under the rules in force back then: outputs now
// excluded stop being tracked, and pending ones the include list no longer
// matches are held.
func pendingPolicyMarks(pending []protocol.Output, pol protocol.OutputPolicy) []protocol.Mark {
	ex, err := pathmatch.CompileSet(append(append([]string{}, policy.OutputExcludes...), pol.Exclude...))
	if err != nil {
		return nil
	}
	in, err := pathmatch.CompileSet(pol.Include)
	if err != nil {
		return nil
	}
	var marks []protocol.Mark
	for _, o := range pending {
		switch {
		case !o.Pending():
		case ex.MatchTree(o.Path, false):
			marks = append(marks, protocol.Mark{RunID: o.RunID, Path: o.Path, Status: protocol.StatusDismissed, Note: "matched by [outputs] exclude"})
		case o.Status == protocol.StatusPending && len(in) > 0 && !in.MatchTree(o.Path, false):
			marks = append(marks, protocol.Mark{RunID: o.RunID, Path: o.Path, Status: protocol.StatusHeld, Note: "not matched by [outputs] include"})
		}
	}
	return marks
}

// applyOutputPolicy updates or.Pending (and the backend's records) with
// pendingPolicyMarks. Remote files are never touched.
func (a *app) applyOutputPolicy(conn *remote.Conn, ws *config.Workspace, t sshx.Target, or *protocol.OpenResult, pol protocol.OutputPolicy) {
	marks := pendingPolicyMarks(or.Pending, pol)
	if len(marks) == 0 {
		return
	}
	if _, err := conn.Call(&protocol.Request{Op: protocol.OpMark, Mark: &protocol.MarkRequest{ReplicaID: ws.ReplicaID(), Items: marks}}, nil, nil); err != nil {
		a.warn("could not apply the current [outputs] rules to pending outputs: %v", err)
		return
	}
	type key struct{ run, path string }
	byKey := make(map[key]protocol.Mark, len(marks))
	for _, m := range marks {
		byKey[key{m.RunID, m.Path}] = m
	}
	kept := or.Pending[:0]
	dismissed := 0
	for _, o := range or.Pending {
		if m, ok := byKey[key{o.RunID, o.Path}]; ok {
			if m.Status == protocol.StatusDismissed {
				dismissed++
				continue
			}
			o.Status, o.Reason = m.Status, m.Note
		}
		kept = append(kept, o)
	}
	or.Pending = kept
	if dismissed > 0 {
		a.info("stopped tracking %d earlier output(s) now matched by [outputs] exclude; the files remain on %s", dismissed, t.Name)
	}
}

// localName shows a project path relative to the user's current directory.
func (a *app) localName(ws *config.Workspace, p string) string {
	if ws.Rel == "." || ws.Rel == "" {
		return p
	}
	if r, err := filepath.Rel(filepath.FromSlash(ws.Rel), filepath.FromSlash(p)); err == nil {
		return filepath.ToSlash(r)
	}
	return p
}

func (a *app) reportHeld(ws *config.Workspace, held []protocol.Output) {
	for i, o := range held {
		if i == 10 && !a.gf.verbose {
			a.info("… and %d more held outputs (see `recom status`)", len(held)-10)
			break
		}
		a.info("%s %s (%s) stays on the backend: %s — fetch with: recom pull %s", a.paint("33", "held"), a.localName(ws, o.Path), humanBytes(o.Size), o.Reason, quoteArg(a.localName(ws, o.Path)))
	}
}

func (a *app) rsyncStderr() *prefixWriter { return &prefixWriter{a: a} }

// prefixWriter forwards rsync diagnostics only in verbose mode, but always
// shows lines that look like real errors.
type prefixWriter struct {
	a   *app
	buf []byte
}

func (w *prefixWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := strings.IndexByte(string(w.buf), '\n')
		if i < 0 {
			break
		}
		line := strings.TrimSpace(string(w.buf[:i]))
		w.buf = w.buf[i+1:]
		if line == "" {
			continue
		}
		if w.a.gf.verbose {
			w.a.debug("rsync: %s", line)
		} else if strings.Contains(line, "error") || strings.Contains(line, "failed") || strings.Contains(line, "denied") {
			w.a.warn("rsync: %s", line)
		}
	}
	return len(p), nil
}
