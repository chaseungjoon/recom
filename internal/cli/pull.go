package cli

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/chaseungjoon/recom/internal/config"
	"github.com/chaseungjoon/recom/internal/protocol"
	"github.com/chaseungjoon/recom/internal/rsync"
)

func pullCmd(gf *globalFlags) *cobra.Command {
	var force, all, dismiss bool
	cmd := &cobra.Command{
		Use:   "pull [path ...]",
		Short: "Fetch remote files or pending outputs into the project",
		Long: `Without arguments, download the pending outputs of earlier runs (what a
normal run collects automatically); --all also fetches "held" outputs that
exceeded the size limits or include patterns.

With paths (files or directories, relative to the current directory), fetch
exactly those from the remote replica, regardless of size limits.

Local files are never overwritten if they differ from what the run started
with: the remote version goes to .recom/conflicts/ instead. --force
overwrites them, saving the previous local version under .recom/backups/.

--dismiss stops tracking the given pending outputs without downloading them
(the remote files are left in place).`,
		Example: "  recom pull\n  recom pull --all\n  recom pull checkpoints/model.pt\n  recom pull outputs/ --force",
		RunE: func(cmd *cobra.Command, args []string) error {
			a := newApp(cmd, gf)
			if dismiss && len(args) == 0 {
				return exitWith(ExitUsage, errors.New("--dismiss needs the paths to stop tracking"))
			}
			rs, err := rsync.Detect()
			if err != nil {
				return exitWith(ExitPreRun, err)
			}
			ws, t, conn, err := a.projectConn()
			if err != nil {
				return err
			}
			defer conn.Close()
			rels := make([]string, 0, len(args))
			for _, arg := range args {
				r, err := projectRel(ws, arg)
				if err != nil {
					return exitWith(ExitUsage, err)
				}
				rels = append(rels, r)
			}
			resp, err := conn.Call(&protocol.Request{Op: protocol.OpOpen, Open: &protocol.OpenRequest{ReplicaID: ws.ReplicaID(), ProjectID: ws.Project.ID, TryLock: true, Owner: ownerName(ws) + " (pull)"}}, nil, nil)
			if err != nil {
				return connErr(err)
			}
			or := resp.Open
			for _, w := range or.Warnings {
				a.warn("%s", w)
			}
			if !or.ReplicaExists {
				return exitWith(1, fmt.Errorf("this project has no replica on %s yet", t.Name))
			}
			if !or.Locked {
				a.warn("%s; files may still be changing", holderText(or.Holder))
			}

			if dismiss {
				var marks []protocol.Mark
				for _, o := range or.Pending {
					for _, r := range rels {
						if o.Path == r || r == "." || strings.HasPrefix(o.Path, r+"/") {
							marks = append(marks, protocol.Mark{RunID: o.RunID, Path: o.Path, Status: protocol.StatusDismissed})
						}
					}
				}
				if len(marks) == 0 {
					a.info("nothing pending under the given paths")
					return nil
				}
				if _, err := conn.Call(&protocol.Request{Op: protocol.OpMark, Mark: &protocol.MarkRequest{ReplicaID: ws.ReplicaID(), Items: marks}}, nil, nil); err != nil {
					return connErr(err)
				}
				a.info("stopped tracking %d pending output(s); the files remain on %s", len(marks), t.Name)
				return nil
			}

			if len(rels) == 0 {
				for _, o := range or.Pending {
					if o.Status == protocol.StatusPending || all {
						rels = append(rels, o.Path)
					}
				}
				if len(rels) == 0 {
					held := 0
					for _, o := range or.Pending {
						if o.Status == protocol.StatusHeld {
							held++
						}
					}
					if held > 0 {
						a.info("no pending outputs; %d held output(s) — fetch them with `recom pull --all` or by path", held)
					} else {
						a.info("no pending outputs on %s", t.Name)
					}
					return nil
				}
			}
			sr, err := conn.Call(&protocol.Request{Op: protocol.OpStat, Stat: &protocol.StatRequest{ReplicaID: ws.ReplicaID(), Paths: rels, SkipSecrets: true}}, nil, nil)
			if err != nil {
				return connErr(err)
			}
			for _, w := range sr.Stat.Warnings {
				a.warn("%s", w)
			}
			for _, m := range sr.Stat.Missing {
				a.warn("%s: not found on %s", m, t.Name)
			}
			var total int64
			for _, f := range sr.Stat.Files {
				total += f.Size
			}
			if len(sr.Stat.Files) == 0 {
				if len(sr.Stat.Missing) > 0 {
					return exitSilently(1)
				}
				a.info("nothing to fetch")
				return nil
			}
			a.info("fetching %d file(s) (%s) from %s", len(sr.Stat.Files), humanBytes(total), t.Name)
			batch := "pull-" + time.Now().UTC().Format("20060102-150405")
			sum, err := a.collectOutputs(conn, ws, t, rs, or.ReplicaPath, sr.Stat.Files, batch, force)
			if err != nil {
				return exitWith(1, err)
			}
			a.info("done: %d collected, %d conflict(s), %d failed", sum.collected, sum.conflicts, sum.failed)
			if sum.failed > 0 || len(sr.Stat.Missing) > 0 {
				return exitSilently(1)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite differing local files (previous versions saved under .recom/backups/)")
	cmd.Flags().BoolVar(&all, "all", false, "also fetch held outputs (over the size limits / not included)")
	cmd.Flags().BoolVar(&dismiss, "dismiss", false, "stop tracking the given pending outputs without downloading them")
	return cmd
}

// projectRel converts a user-supplied path (relative to the cwd, or absolute
// inside the project) into a project-relative slash path.
func projectRel(ws *config.Workspace, p string) (string, error) {
	var abs string
	if filepath.IsAbs(p) {
		abs = filepath.Clean(p)
		if r, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
			abs = filepath.Join(r, filepath.Base(abs))
		}
	} else {
		abs = filepath.Join(ws.Root, filepath.FromSlash(ws.Rel), p)
	}
	rel, err := filepath.Rel(ws.Root, abs)
	if err != nil {
		return "", err
	}
	rel = path.Clean(filepath.ToSlash(rel))
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("%s is outside the project %s", p, ws.Root)
	}
	if strings.HasPrefix(rel, config.LocalDir+"/") || rel == config.LocalDir {
		return "", fmt.Errorf("%s is recom's local state directory", p)
	}
	return rel, nil
}
