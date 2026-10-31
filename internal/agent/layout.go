package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/chaseungjoon/recom/internal/protocol"
)

var (
	replicaIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{7,99}$`)
	jobIDRe     = regexp.MustCompile(`^[a-z0-9]{6,32}$`)
	safeRootRe  = regexp.MustCompile(`^/[A-Za-z0-9._/@+=,-]+$`)
)

// Layout is the remote directory structure under the application root.
//
//	<root>/bin/                     helper binaries (content-addressed)
//	<root>/projects/<replica>/      persistent project replicas
//	<root>/state/<replica>/         lease, manifests, snapshots, jobs (0700)
type Layout struct {
	Root string
}

// ValidateRoot checks that the application root is a safe absolute path.
func ValidateRoot(root string) error {
	if !safeRootRe.MatchString(root) || filepath.Clean(root) != root || root == "/" {
		return fmt.Errorf("unsafe remote root %q", root)
	}
	for _, part := range strings.Split(root, "/") {
		if part == ".." || part == "." {
			return fmt.Errorf("unsafe remote root %q", root)
		}
	}
	return nil
}

// ValidReplicaID reports whether id is a well-formed replica id.
func ValidReplicaID(id string) bool { return replicaIDRe.MatchString(id) }

// ValidJobID reports whether id is a well-formed job id.
func ValidJobID(id string) bool { return jobIDRe.MatchString(id) }

func (l Layout) Replica(id string) string  { return filepath.Join(l.Root, "projects", id) }
func (l Layout) State(id string) string    { return filepath.Join(l.Root, "state", id) }
func (l Layout) Runs(id string) string     { return filepath.Join(l.State(id), "runs") }
func (l Layout) Jobs(id string) string     { return filepath.Join(l.State(id), "jobs") }
func (l Layout) Job(id, job string) string { return filepath.Join(l.Jobs(id), job) }

// EnsureState creates the private state directories for a replica.
func (l Layout) EnsureState(id string) error {
	for _, d := range []string{l.Root, filepath.Join(l.Root, "state"), l.State(id), l.Runs(id), l.Jobs(id)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// CleanRel validates a slash-separated relative path received from the
// client and returns it in canonical form. It refuses absolute paths, ".."
// components, NUL bytes and empty paths.
func CleanRel(rel string) (string, error) {
	if rel == "" || strings.ContainsRune(rel, 0) {
		return "", fmt.Errorf("invalid path %q", rel)
	}
	if strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("absolute path %q not allowed", rel)
	}
	c := path.Clean(rel)
	if c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("path %q escapes the project", rel)
	}
	for _, part := range strings.Split(rel, "/") {
		if part == ".." {
			return "", fmt.Errorf("path %q escapes the project", rel)
		}
	}
	return c, nil
}

// insideNoSymlink resolves rel under base, refusing any symlinked component
// along the way (the final component may be a symlink only if allowFinal).
func insideNoSymlink(base, rel string, allowFinal bool) (string, error) {
	if rel == "." {
		return base, nil
	}
	parts := strings.Split(rel, "/")
	cur := base
	for i, p := range parts {
		cur = filepath.Join(cur, p)
		fi, err := os.Lstat(cur)
		if err != nil {
			return "", err
		}
		if fi.Mode()&os.ModeSymlink != 0 && !(allowFinal && i == len(parts)-1) {
			return "", fmt.Errorf("path %q traverses a symlink", rel)
		}
	}
	return cur, nil
}

// writeJSONAtomic writes v to path with mode 0600 via a temp file + rename.
func writeJSONAtomic(p string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(p, b)
}

func writeFileAtomic(p string, b []byte) error {
	dir := filepath.Dir(p)
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, p)
}

// readJSON reads and strictly decodes JSON; malformed files fail closed.
func readJSON(p string, v any) error {
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("corrupt state file %s: %w", p, err)
	}
	return nil
}

func perr(code, format string, args ...any) *protocol.Error {
	return &protocol.Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

func asPErr(err error) *protocol.Error {
	var pe *protocol.Error
	if errors.As(err, &pe) {
		return pe
	}
	return &protocol.Error{Code: protocol.ErrInternal, Message: err.Error()}
}
