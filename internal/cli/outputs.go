package cli

import (
	"path"
	"strings"

	"github.com/chaseungjoon/recom/internal/collect"
	"github.com/chaseungjoon/recom/internal/config"
	"github.com/chaseungjoon/recom/internal/protocol"
	"github.com/chaseungjoon/recom/internal/remote"
	"github.com/chaseungjoon/recom/internal/rsync"
	"github.com/chaseungjoon/recom/internal/sshx"
)

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
	results, dlErr := c.Collect(a.ctx, items, batch)
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

// localName shows a project path relative to the user's current directory.
func (a *app) localName(ws *config.Workspace, p string) string {
	if ws.Rel == "." || ws.Rel == "" {
		return p
	}
	prefix := ws.Rel + "/"
	if strings.HasPrefix(p, prefix) {
		return p[len(prefix):]
	}
	ups := strings.Repeat("../", strings.Count(ws.Rel, "/")+1)
	return path.Clean(ups + p)
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
