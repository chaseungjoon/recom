package agent

import (
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chaseungjoon/recom/internal/pathmatch"
	"github.com/chaseungjoon/recom/internal/policy"
	"github.com/chaseungjoon/recom/internal/protocol"
)

// Entry is the recorded state of one path in a snapshot.
type Entry struct {
	Type    byte // 'f' regular file, 'l' symlink
	Size    int64
	MtimeNs int64
	CtimeNs int64
	Ino     uint64
	Mode    uint32
	Hash    string
	Link    string
}

// Snapshot is the state of a replica at one instant.
type Snapshot struct {
	Taken   time.Time
	Entries map[string]*Entry
	Policy  protocol.OutputPolicy // policy in force, for deferred/recovered diffs
}

// racyWindow: files modified this close to the pre-run snapshot are always
// content-compared, because coarse filesystem timestamps could hide a change.
const racyWindow = 2 * time.Second

// filters compiled from an OutputPolicy.
type filters struct {
	exclude     pathmatch.Set
	include     pathmatch.Set
	secrets     pathmatch.Set
	secretAllow pathmatch.Set
	skipSecrets bool
}

func newFilters(p protocol.OutputPolicy) (*filters, error) {
	ex, err := pathmatch.CompileSet(append(append([]string{}, policy.OutputExcludes...), p.Exclude...))
	if err != nil {
		return nil, fmt.Errorf("outputs.exclude: %w", err)
	}
	in, err := pathmatch.CompileSet(p.Include)
	if err != nil {
		return nil, fmt.Errorf("outputs.include: %w", err)
	}
	return &filters{
		exclude:     ex,
		include:     in,
		secrets:     pathmatch.MustCompileSet(policy.Secrets),
		secretAllow: pathmatch.MustCompileSet(policy.SecretAllow),
		skipSecrets: p.SkipSecrets,
	}, nil
}

func (f *filters) isSecret(rel string, isDir bool) bool {
	return f.secrets.MatchTree(rel, isDir) && !f.secretAllow.Match(rel, isDir)
}

// skip reports whether rel is outside output tracking entirely.
func (f *filters) skip(rel string, isDir bool) bool {
	if f.exclude.Match(rel, isDir) {
		return true
	}
	return f.skipSecrets && f.isSecret(rel, isDir)
}

// hashCache avoids re-hashing unchanged files across runs.
type hashCache map[string]cacheEntry

type cacheEntry struct {
	Size, MtimeNs, CtimeNs int64
	Ino                    uint64
	Hash                   string
}

func loadHashCache(stateDir string) hashCache {
	f, err := os.Open(filepath.Join(stateDir, "hashcache.gob"))
	if err != nil {
		return hashCache{}
	}
	defer f.Close()
	c := hashCache{}
	if err := gob.NewDecoder(f).Decode(&c); err != nil {
		return hashCache{} // a corrupt cache only costs re-hashing
	}
	return c
}

func (c hashCache) save(stateDir string, snap *Snapshot) error {
	out := hashCache{}
	for p, e := range snap.Entries {
		if e.Type == 'f' && e.Hash != "" {
			out[p] = cacheEntry{e.Size, e.MtimeNs, e.CtimeNs, e.Ino, e.Hash}
		}
	}
	tmp, err := os.CreateTemp(stateDir, ".tmp-hash-*")
	if err != nil {
		return err
	}
	if err := gob.NewEncoder(tmp).Encode(out); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(stateDir, "hashcache.gob"))
}

func (c hashCache) lookup(rel string, e *Entry) string {
	ce, ok := c[rel]
	if ok && ce.Size == e.Size && ce.MtimeNs == e.MtimeNs && ce.CtimeNs == e.CtimeNs && ce.Ino == e.Ino {
		return ce.Hash
	}
	return ""
}

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// takeSnapshot walks root (without following symlinks), recording regular
// files and symlinks that are subject to output tracking. When hashLimit > 0,
// regular files up to that size are hashed (using the cache when possible).
func takeSnapshot(root string, f *filters, cache hashCache, hashLimit int64) (*Snapshot, error) {
	snap := &Snapshot{Taken: time.Now(), Entries: map[string]*Entry{}}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			if errors.Is(err, fs.ErrPermission) || errors.Is(err, fs.ErrNotExist) {
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			return err
		}
		if p == root {
			return nil
		}
		rel := filepath.ToSlash(p[len(root)+1:])
		if d.IsDir() {
			if f.skip(rel, true) {
				return fs.SkipDir
			}
			return nil
		}
		t := d.Type()
		if t&fs.ModeSymlink == 0 && !t.IsRegular() {
			return nil // sockets, devices, FIFOs are never outputs
		}
		if f.skip(rel, false) {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil // vanished mid-walk
		}
		e := &Entry{Size: fi.Size(), MtimeNs: fi.ModTime().UnixNano(), Mode: uint32(fi.Mode().Perm())}
		e.CtimeNs, e.Ino = statExtra(fi)
		if t&fs.ModeSymlink != 0 {
			e.Type = 'l'
			e.Link, _ = os.Readlink(p)
		} else {
			e.Type = 'f'
			if hashLimit > 0 && e.Size <= hashLimit {
				if h := cache.lookup(rel, e); h != "" {
					e.Hash = h
				} else if h, err := hashFile(p); err == nil {
					e.Hash = h
				}
			}
		}
		snap.Entries[rel] = e
		return nil
	})
	return snap, err
}

func sameMeta(a, b *Entry) bool {
	return a.Type == b.Type && a.Size == b.Size && a.MtimeNs == b.MtimeNs && a.CtimeNs == b.CtimeNs && a.Ino == b.Ino && a.Link == b.Link
}

// safeLink reports whether a symlink at rel pointing to target stays inside
// the project (relative target that does not climb above the root).
func safeLink(rel, target string) bool {
	if target == "" || strings.HasPrefix(target, "/") {
		return false
	}
	j := path.Clean(path.Join(path.Dir(rel), target))
	return j != ".." && !strings.HasPrefix(j, "../") && j != "."
}

// diffResult is the outcome of comparing pre/post snapshots.
type diffResult struct {
	Outputs []protocol.Output
	Deleted int
}

// diffSnapshots determines the outputs of a run. root is the replica path,
// used to hash candidates. The post snapshot is updated with any new hashes.
func diffSnapshots(root, runID string, pre, post *Snapshot, f *filters, pol protocol.OutputPolicy, cache hashCache) diffResult {
	var res diffResult
	paths := make([]string, 0, len(post.Entries))
	for p := range post.Entries {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	racyBefore := pre.Taken.Add(-racyWindow).UnixNano()

	var total int64
	for _, rel := range paths {
		e := post.Entries[rel]
		pe, existed := pre.Entries[rel]
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if existed && sameMeta(pe, e) {
			racy := pe.MtimeNs >= racyBefore
			if !racy || pe.Hash == "" || e.Type != 'f' {
				if e.Hash == "" {
					e.Hash = pe.Hash
				}
				continue
			}
			if e.Hash == "" {
				e.Hash, _ = hashFile(abs)
			}
			if e.Hash == pe.Hash {
				continue
			}
		}
		if existed && pe.Type == 'f' && e.Type == 'f' && pe.Hash != "" {
			if e.Hash == "" {
				if h := cache.lookup(rel, e); h != "" {
					e.Hash = h
				} else {
					e.Hash, _ = hashFile(abs)
				}
			}
			if e.Hash == pe.Hash {
				continue // touched only; contents unchanged
			}
		}
		o := protocol.Output{
			RunID: runID, Path: rel, Size: e.Size, Mode: e.Mode, MtimeNs: e.MtimeNs,
			PreExisted: existed, Status: protocol.StatusPending,
		}
		if existed {
			o.PreSHA256 = pe.Hash
		}
		if e.Type == 'l' {
			o.Type = "symlink"
			o.Link = e.Link
			if !safeLink(rel, e.Link) {
				o.Status = protocol.StatusSkipped
				o.Reason = "symlink points outside the project"
			}
			res.Outputs = append(res.Outputs, o)
			continue
		}
		o.Type = "file"
		switch {
		case len(f.include) > 0 && !f.include.MatchTree(rel, false):
			o.Status = protocol.StatusHeld
			o.Reason = "not matched by [outputs] include"
		case pol.MaxFileSize > 0 && e.Size > pol.MaxFileSize:
			o.Status = protocol.StatusHeld
			o.Reason = fmt.Sprintf("larger than max_file_size (%s)", humanBytes(pol.MaxFileSize))
		case pol.MaxRunSize > 0 && total+e.Size > pol.MaxRunSize:
			o.Status = protocol.StatusHeld
			o.Reason = fmt.Sprintf("run outputs exceed max_run_size (%s)", humanBytes(pol.MaxRunSize))
		default:
			total += e.Size
		}
		if o.Status == protocol.StatusPending && e.Hash == "" {
			e.Hash, _ = hashFile(abs)
		}
		o.SHA256 = e.Hash
		res.Outputs = append(res.Outputs, o)
	}
	for p, pe := range pre.Entries {
		if _, ok := post.Entries[p]; !ok && pe.Type == 'f' {
			res.Deleted++
		}
	}
	return res
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

// saveSnapshot / loadSnapshot persist the pre-run snapshot of a detached job.
func saveSnapshot(p string, s *Snapshot) error {
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-snap-*")
	if err != nil {
		return err
	}
	if err := gob.NewEncoder(tmp).Encode(s); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), p)
}

func loadSnapshot(p string) (*Snapshot, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var s Snapshot
	if err := gob.NewDecoder(f).Decode(&s); err != nil {
		return nil, fmt.Errorf("corrupt snapshot %s: %w", p, err)
	}
	if s.Entries == nil {
		s.Entries = map[string]*Entry{}
	}
	return &s, nil
}
