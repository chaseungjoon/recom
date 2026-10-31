// Package collect installs remote outputs into the local project safely:
// download into a staging area inside the project, verify SHA-256, apply the
// conflict rules, then rename atomically into place. All filesystem access
// goes through os.Root, so no path can escape the project root.
package collect

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/chaseungjoon/recom/internal/config"
	"github.com/chaseungjoon/recom/internal/protocol"
	"github.com/chaseungjoon/recom/internal/rsync"
	"github.com/chaseungjoon/recom/internal/sshx"
)

// Result statuses.
const (
	Collected = "collected" // installed (or already identical)
	Conflict  = "conflict"  // local copy changed; remote version staged
	Failed    = "failed"    // left pending on the backend
	Skipped   = "skipped"   // refused (unsafe path, etc.)
)

// Result is the outcome for one output.
type Result struct {
	Output   protocol.Output
	Status   string
	Note     string
	Staged   string // conflict copy, relative to the project root
	Backup   string // previous local version saved by --force, relative
	Identity bool   // local file already had the remote content
}

// Collector installs outputs into one working copy.
type Collector struct {
	Root      string
	Target    sshx.Target
	Rsync     *rsync.Binary
	RemoteDir string
	Force     bool
	Stderr    io.Writer
}

// SafeRel validates a remote-supplied relative path for local installation.
func SafeRel(p string) (string, error) {
	if p == "" || strings.ContainsRune(p, 0) || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") && filepath.Separator == '\\' {
		return "", fmt.Errorf("unsafe path %q", p)
	}
	c := path.Clean(p)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") || c != p {
		return "", fmt.Errorf("unsafe path %q", p)
	}
	first := strings.SplitN(c, "/", 2)[0]
	if first == config.LocalDir || c == config.ProjectFile {
		return "", fmt.Errorf("refusing to overwrite recom's own file %q", p)
	}
	return c, nil
}

func hashReader(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashIn(root *os.Root, name string) (string, error) {
	f, err := root.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return hashReader(f)
}

func sameStat(a, b fs.FileInfo) bool {
	return a.Size() == b.Size() && a.ModTime().Equal(b.ModTime()) && a.Mode() == b.Mode()
}

// Collect downloads and installs items. batch names the staging/conflict
// subdirectory (normally the run id). It never returns partially installed
// files: anything not verified stays pending (status Failed).
func (c *Collector) Collect(ctx context.Context, items []protocol.Output, batch string) ([]Result, error) {
	if len(items) == 0 {
		return nil, nil
	}
	root, err := os.OpenRoot(c.Root)
	if err != nil {
		return nil, err
	}
	defer root.Close()

	stageRel := filepath.Join(config.LocalDir, "staging", batch)
	if err := root.MkdirAll(stageRel, 0o700); err != nil {
		return nil, fmt.Errorf("create staging area: %w", err)
	}
	results := make([]Result, len(items))
	var download []string
	for i, it := range items {
		results[i] = Result{Output: it}
		rel, err := SafeRel(it.Path)
		if err != nil {
			results[i].Status, results[i].Note = Skipped, err.Error()
			continue
		}
		items[i].Path = rel
		if it.Type == "symlink" {
			continue
		}
		if it.Type != "file" {
			results[i].Status, results[i].Note = Skipped, "unsupported file type "+it.Type
			continue
		}
		// Skip the transfer when the local copy already has the content.
		if it.SHA256 != "" {
			if fi, err := root.Lstat(filepath.FromSlash(rel)); err == nil && fi.Mode().IsRegular() && fi.Size() == it.Size {
				if h, err := hashIn(root, filepath.FromSlash(rel)); err == nil && h == it.SHA256 {
					results[i].Status, results[i].Identity = Collected, true
					continue
				}
			}
		}
		download = append(download, rel)
	}
	var dlErr error
	if len(download) > 0 {
		dlErr = c.Rsync.Download(ctx, c.Target, c.RemoteDir, filepath.Join(c.Root, stageRel), download, c.Stderr)
	}
	for i := range items {
		if results[i].Status != "" {
			continue
		}
		c.installOne(root, stageRel, batch, items[i], &results[i])
	}
	allDone := true
	for _, r := range results {
		if r.Status == Failed {
			allDone = false
		}
	}
	if allDone {
		_ = os.RemoveAll(filepath.Join(c.Root, stageRel))
	}
	return results, dlErr
}

func (c *Collector) installOne(root *os.Root, stageRel, batch string, it protocol.Output, res *Result) {
	dest := filepath.FromSlash(it.Path)
	staged := filepath.Join(stageRel, dest)

	if it.Type == "symlink" {
		if err := root.MkdirAll(filepath.Dir(staged), 0o700); err != nil {
			res.Status, res.Note = Failed, err.Error()
			return
		}
		_ = root.Remove(staged)
		if err := root.Symlink(it.Link, staged); err != nil {
			res.Status, res.Note = Failed, err.Error()
			return
		}
	} else {
		fi, err := root.Lstat(staged)
		if err != nil || !fi.Mode().IsRegular() {
			res.Status, res.Note = Failed, "not downloaded"
			return
		}
		if it.SHA256 != "" {
			h, err := hashIn(root, staged)
			if err != nil || h != it.SHA256 {
				res.Status, res.Note = Failed, "checksum mismatch (remote file changed after the run, or transfer incomplete); kept on the backend"
				return
			}
		}
	}

	if err := root.MkdirAll(filepath.Dir(dest), 0o755); err != nil && filepath.Dir(dest) != "." {
		res.Status, res.Note = Failed, "cannot create directory: "+err.Error()
		return
	}
	local, lerr := root.Lstat(dest)
	install := false
	switch {
	case errors.Is(lerr, fs.ErrNotExist):
		install = true
	case lerr != nil:
		res.Status, res.Note = Failed, lerr.Error()
		return
	case it.Type == "symlink":
		if local.Mode()&fs.ModeSymlink != 0 {
			if t, err := root.Readlink(dest); err == nil && t == it.Link {
				_ = root.Remove(staged)
				res.Status, res.Identity = Collected, true
				return
			}
		}
		install = c.Force && local.Mode()&fs.ModeSymlink != 0
	case local.Mode().IsRegular():
		h, err := hashIn(root, dest)
		if err != nil {
			res.Status, res.Note = Failed, err.Error()
			return
		}
		switch {
		case h == it.SHA256 && it.SHA256 != "":
			_ = root.Remove(staged)
			res.Status, res.Identity = Collected, true
			return
		case c.Force:
			install = true
		case it.PreExisted && it.PreSHA256 != "" && h == it.PreSHA256:
			install = true // local copy is exactly what the run started from
		}
		if install {
			// Detect a local edit racing with installation.
			if now, err := root.Lstat(dest); err != nil || !sameStat(now, local) {
				install = false
			}
		}
	default:
		install = false // directory or special file in the way
	}

	if !install {
		conf := filepath.Join(config.LocalDir, "conflicts", batch, dest)
		if err := root.MkdirAll(filepath.Dir(conf), 0o700); err == nil {
			_ = root.Remove(conf)
			if err := root.Rename(staged, conf); err == nil {
				res.Status, res.Staged = Conflict, filepath.ToSlash(conf)
				res.Note = "local file changed since the run started; remote version saved to " + res.Staged
				return
			}
		}
		res.Status, res.Note = Failed, "conflict, and the remote version could not be staged"
		return
	}
	if c.Force && lerr == nil {
		bk := filepath.Join(config.LocalDir, "backups", time.Now().UTC().Format("20060102-150405"), dest)
		if err := root.MkdirAll(filepath.Dir(bk), 0o700); err == nil {
			if err := root.Rename(dest, bk); err == nil {
				res.Backup = filepath.ToSlash(bk)
			}
		}
	}
	if err := root.Rename(staged, dest); err != nil {
		res.Status, res.Note = Failed, "install: "+err.Error()
		return
	}
	res.Status = Collected
}
