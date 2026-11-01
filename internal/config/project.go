package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/chaseungjoon/recom/internal/pathmatch"
	"github.com/chaseungjoon/recom/internal/policy"
	"github.com/chaseungjoon/recom/internal/toolchain"
)

// ProjectFile is the project config file name, placed at the project root.
const ProjectFile = ".recom.toml"

// LocalDir is the untracked per-working-copy directory at the project root.
const LocalDir = ".recom"

// Project is .recom.toml.
type Project struct {
	ID      string        `toml:"id"`
	Host    string        `toml:"host,omitempty"`
	Sync    SyncConfig    `toml:"sync"`
	Outputs OutputsConfig `toml:"outputs"`
	Setup   SetupConfig   `toml:"setup"`
	Run     RunConfig     `toml:"run"`
	Jobs    JobsConfig    `toml:"jobs"`
}

// SetupConfig controls automatic environment setup on the backend.
type SetupConfig struct {
	// Toolchains whose dependencies recom installs before running. Unset
	// (projects from recom 0.1) or containing "auto" detects them on every
	// run; an empty list turns automatic setup off.
	Toolchains []string `toml:"toolchains"`
	// Command is an extra backend command run after the toolchain setups.
	Command string `toml:"command,omitempty"`
	// Watch lists files (globs, relative to the project root) whose change
	// re-runs Command.
	Watch []string `toml:"watch"`
}

// Toolchain converts the section for the toolchain package.
func (s SetupConfig) Toolchain() toolchain.Config {
	return toolchain.Config{Toolchains: s.Toolchains, Command: s.Command, Watch: s.Watch}
}

// SyncConfig controls the pre-run upload.
type SyncConfig struct {
	Exclude         []string `toml:"exclude"`
	Include         []string `toml:"include"`
	DefaultExcludes *bool    `toml:"default_excludes,omitempty"`
}

// UseDefaultExcludes reports whether built-in excludes apply.
func (s SyncConfig) UseDefaultExcludes() bool { return s.DefaultExcludes == nil || *s.DefaultExcludes }

// OutputsConfig controls post-run output collection.
type OutputsConfig struct {
	Exclude     []string `toml:"exclude"`
	Include     []string `toml:"include"`
	MaxFileSize string   `toml:"max_file_size,omitempty"`
	MaxRunSize  string   `toml:"max_run_size,omitempty"`
}

// RunConfig controls the remote execution environment.
type RunConfig struct {
	Env     map[string]string `toml:"env"`
	Path    []string          `toml:"path"`
	Wrapper []string          `toml:"wrapper"`
	// EnvMode is "login" (default) or "ssh".
	EnvMode string `toml:"env_mode,omitempty"`
}

// JobsConfig controls detached jobs.
type JobsConfig struct {
	// Tmux is "auto" (default), "always" or "never".
	Tmux string `toml:"tmux,omitempty"`
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// NewUUID returns a random RFC 4122 version 4 UUID.
func NewUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// Validate checks the project config.
func (p *Project) Validate() error {
	if !uuidRe.MatchString(p.ID) {
		return fmt.Errorf("%s: id %q is not a valid UUID (do not edit the id by hand)", ProjectFile, p.ID)
	}
	if p.Host != "" && !ValidHostName(p.Host) {
		return fmt.Errorf("%s: invalid host %q", ProjectFile, p.Host)
	}
	for _, list := range [][]string{p.Sync.Exclude, p.Sync.Include, p.Outputs.Exclude, p.Outputs.Include} {
		if _, err := pathmatch.CompileSet(list); err != nil {
			return fmt.Errorf("%s: %w", ProjectFile, err)
		}
		for _, pat := range list {
			if strings.ContainsAny(pat, "\n\r\x00") {
				return fmt.Errorf("%s: pattern %q contains control characters", ProjectFile, pat)
			}
		}
	}
	if _, err := p.MaxFileSize(); err != nil {
		return err
	}
	if _, err := p.MaxRunSize(); err != nil {
		return err
	}
	switch p.Run.EnvMode {
	case "", "login", "ssh":
	default:
		return fmt.Errorf("%s: run.env_mode must be \"login\" or \"ssh\"", ProjectFile)
	}
	for _, n := range p.Setup.Toolchains {
		if !toolchain.Known(n) {
			return fmt.Errorf("%s: setup.toolchains: unknown toolchain %q (known: auto, %s)", ProjectFile, n, strings.Join(toolchain.Names(), ", "))
		}
	}
	for _, w := range p.Setup.Watch {
		if w == "" || strings.HasPrefix(w, "/") || strings.Contains(w, "..") {
			return fmt.Errorf("%s: setup.watch: %q must be a path relative to the project root", ProjectFile, w)
		}
	}
	switch p.Jobs.Tmux {
	case "", "auto", "always", "never":
	default:
		return fmt.Errorf("%s: jobs.tmux must be \"auto\", \"always\" or \"never\"", ProjectFile)
	}
	for k := range p.Run.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") {
			return fmt.Errorf("%s: invalid environment variable name %q", ProjectFile, k)
		}
	}
	return nil
}

// MaxFileSize returns the per-file automatic download limit in bytes.
func (p *Project) MaxFileSize() (int64, error) {
	return sizeOr(p.Outputs.MaxFileSize, policy.DefaultMaxFileSize, "outputs.max_file_size")
}

// MaxRunSize returns the per-run automatic download limit in bytes.
func (p *Project) MaxRunSize() (int64, error) {
	return sizeOr(p.Outputs.MaxRunSize, policy.DefaultMaxRunSize, "outputs.max_run_size")
}

func sizeOr(s string, def int64, key string) (int64, error) {
	if s == "" {
		return def, nil
	}
	n, err := ParseSize(s)
	if err != nil {
		return 0, fmt.Errorf("%s: %s: %w", ProjectFile, key, err)
	}
	return n, nil
}

// ParseSize parses sizes like "100MB", "1.5GiB", "0" (unlimited), "500k".
func ParseSize(s string) (int64, error) {
	t := strings.TrimSpace(strings.ToUpper(s))
	if t == "" {
		return 0, errors.New("empty size")
	}
	mult := int64(1)
	for _, u := range []struct {
		suf string
		m   int64
	}{{"TIB", 1 << 40}, {"GIB", 1 << 30}, {"MIB", 1 << 20}, {"KIB", 1 << 10}, {"TB", 1 << 40}, {"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10}, {"T", 1 << 40}, {"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10}, {"B", 1}} {
		if strings.HasSuffix(t, u.suf) {
			mult = u.m
			t = strings.TrimSpace(strings.TrimSuffix(t, u.suf))
			break
		}
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("invalid size %q (use e.g. 100MB, 2GB, 0 for unlimited)", s)
	}
	return int64(f * float64(mult)), nil
}

// ProjectTemplate is written by `recom init`. toolchains and path are what
// init detected in the project (toolchains nil means none was found).
func ProjectTemplate(id string, toolchains, path []string) string {
	tc := `["auto"]`
	if len(toolchains) > 0 {
		tc = tomlList(toolchains)
	}
	return `# recom project configuration. Safe to commit: it contains no secrets and
# no machine-specific paths. Docs: https://github.com/chaseungjoon/recom/blob/main/docs/configuration.md
id = "` + id + `"

# Preferred backend for this project (overrides the global default).
# host = "aiworkstation"

[setup]
# Before running your command, recom installs the project's dependencies on
# the backend from your dependency files (requirements.txt / pyproject.toml →
# .venv, package.json → node_modules, Cargo.toml, go.mod, Gemfile, ...), and
# again whenever those files change. "auto" detects toolchains on every run;
# [] turns automatic setup off. See docs/environments.md.
toolchains = ` + tc + `
# Extra backend command run after the toolchain setups, e.g. "make deps";
# it runs again whenever a file matching ` + "`watch`" + ` changes.
# command = ""
watch = []

[sync]
# Extra rsync-style patterns that are never uploaded.
exclude = []
# Patterns uploaded even if a built-in exclude (including secrets) matches.
include = []

[outputs]
# Changed remote files matching these are never brought back automatically.
exclude = []
# If non-empty, only matching outputs are downloaded automatically.
include = []
# Larger outputs stay on the backend as "held"; fetch them with ` + "`recom pull`" + `.
max_file_size = "100MB"
max_run_size = "1GB"

[run]
# "login" runs commands with the environment of your remote login shell;
# "ssh" uses the plain non-interactive SSH environment.
env_mode = "login"
# Extra environment variables for remote commands.
env = {}
# Directories prepended to PATH (relative entries are inside the remote project).
# The [setup] toolchains' directories (e.g. .venv/bin) are always included.
path = ` + tomlList(path) + `
# Command prefix, e.g. ["conda", "run", "--no-capture-output", "-n", "myenv"].
wrapper = []

[jobs]
# Detached jobs run under tmux when available: "auto", "always" or "never".
tmux = "auto"
`
}

func tomlList(items []string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = strconv.Quote(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// LoadProject parses a project file.
func LoadProject(path string) (*Project, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Project
	md, err := toml.Decode(string(b), &p)
	if err != nil {
		return nil, fmt.Errorf("%s is not valid TOML: %w", path, err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		return nil, fmt.Errorf("%s: unknown setting %q", path, und[0].String())
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// Workspace is a discovered project plus the caller's position inside it.
type Workspace struct {
	Root    string // canonical absolute project root
	Rel     string // slash-separated cwd relative to Root ("." at root)
	Project *Project
	Local   *LocalState
	Notice  string // e.g. working-copy identity changes
}

// ErrNoProject is returned when no .recom.toml is found.
var ErrNoProject = errors.New("not inside a recom project (no .recom.toml found in this directory or any parent); run `recom init` at your project root")

// Discover walks up from cwd to the nearest project file.
func Discover(cwd string) (*Workspace, error) {
	real, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return nil, err
	}
	real, err = filepath.Abs(real)
	if err != nil {
		return nil, err
	}
	dir := real
	for {
		fi, err := os.Lstat(filepath.Join(dir, ProjectFile))
		if err == nil && fi.Mode().IsRegular() {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, ErrNoProject
		}
		dir = parent
	}
	proj, err := LoadProject(filepath.Join(dir, ProjectFile))
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(dir, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("current directory is outside the project root %s", dir)
	}
	ws := &Workspace{Root: dir, Rel: filepath.ToSlash(rel), Project: proj}
	if err := ws.loadLocal(); err != nil {
		return nil, err
	}
	return ws, nil
}

// LocalPath returns a path inside the working copy's .recom directory.
func (w *Workspace) LocalPath(parts ...string) string {
	return filepath.Join(append([]string{w.Root, LocalDir}, parts...)...)
}

// ReplicaID is the stable remote replica name for this working copy.
func (w *Workspace) ReplicaID() string { return w.Project.ID + "-" + w.Local.CopyID }

// LocalState is .recom/local.toml: the identity of this working copy.
type LocalState struct {
	CopyID  string    `toml:"copy_id"`
	Root    string    `toml:"root"`
	Created time.Time `toml:"created"`
}

var copyIDRe = regexp.MustCompile(`^[0-9a-f]{12}$`)

// EnsureLocalDir creates .recom/ with a self-ignoring .gitignore.
func EnsureLocalDir(root string) error {
	d := filepath.Join(root, LocalDir)
	if fi, err := os.Lstat(d); err == nil && !fi.IsDir() {
		return fmt.Errorf("%s exists and is not a directory", d)
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	gi := filepath.Join(d, ".gitignore")
	if _, err := os.Lstat(gi); errors.Is(err, os.ErrNotExist) {
		return os.WriteFile(gi, []byte("# recom working-copy state; never commit.\n*\n"), 0o600)
	}
	return nil
}

// loadLocal loads or creates the working-copy identity. Two local copies of
// the same project (git clones or cp -r) must not share one remote replica:
//
//   - A git clone has no .recom/ (it ignores itself), so it gets a fresh id.
//   - A moved/renamed directory keeps its id (old location no longer has it).
//   - A copied directory carries the same id while the original still exists
//     at the recorded root with that id; the copy is given a fresh id.
func (w *Workspace) loadLocal() error {
	if err := EnsureLocalDir(w.Root); err != nil {
		return err
	}
	p := w.LocalPath("local.toml")
	var ls LocalState
	_, err := toml.DecodeFile(p, &ls)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return w.newLocal("")
	case err != nil || !copyIDRe.MatchString(ls.CopyID):
		return w.newLocal(fmt.Sprintf("working-copy state %s was unreadable; created a new identity (a fresh remote replica will be used)", p))
	}
	if ls.Root != w.Root {
		var orig LocalState
		if _, err := toml.DecodeFile(filepath.Join(ls.Root, LocalDir, "local.toml"), &orig); err == nil && orig.CopyID == ls.CopyID && sameProject(ls.Root, w.Project.ID) {
			return w.newLocal(fmt.Sprintf("this directory is a copy of %s; gave it its own remote replica", ls.Root))
		}
		ls.Root = w.Root
		if err := writeLocal(p, &ls); err != nil {
			return err
		}
		w.Notice = "project moved; keeping its remote replica"
	}
	w.Local = &ls
	return nil
}

func sameProject(root, id string) bool {
	p, err := LoadProject(filepath.Join(root, ProjectFile))
	return err == nil && p.ID == id
}

func (w *Workspace) newLocal(notice string) error {
	ls := &LocalState{CopyID: randHex(6), Root: w.Root, Created: time.Now().UTC().Truncate(time.Second)}
	if err := writeLocal(w.LocalPath("local.toml"), ls); err != nil {
		return err
	}
	// Per-host state belongs to the old identity.
	os.RemoveAll(w.LocalPath("state"))
	w.Local = ls
	w.Notice = notice
	return nil
}

func writeLocal(p string, ls *LocalState) error {
	var sb strings.Builder
	sb.WriteString("# recom working-copy identity. Do not copy between checkouts.\n")
	if err := toml.NewEncoder(&sb).Encode(ls); err != nil {
		return err
	}
	return WriteFileAtomic(p, []byte(sb.String()), 0o600)
}

// Init creates a new project at dir, recording the detected toolchains and
// their PATH entries.
func Init(dir, host string, toolchains, path []string) (*Project, error) {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	home, _ := os.UserHomeDir()
	if hr, err := filepath.EvalSymlinks(home); err == nil && hr == real {
		return nil, errors.New("refusing to make your home directory a recom project (it would upload everything); run `recom init` inside a project directory")
	}
	if filepath.Dir(real) == real {
		return nil, errors.New("refusing to make the filesystem root a recom project")
	}
	if _, err := os.Lstat(filepath.Join(real, ProjectFile)); err == nil {
		p, perr := LoadProject(filepath.Join(real, ProjectFile))
		if perr != nil {
			return nil, fmt.Errorf("%s already exists but is invalid (%v); fix or remove it", ProjectFile, perr)
		}
		return nil, fmt.Errorf("already a recom project (id %s); nothing to do — run `recom <command>`. To start over with a new identity, delete %s yourself", p.ID, ProjectFile)
	}
	if ws, err := Discover(filepath.Dir(real)); err == nil {
		return nil, fmt.Errorf("this directory is inside the recom project at %s; run recom from there (nested projects are not supported)", ws.Root)
	}
	if host != "" && !ValidHostName(host) {
		return nil, fmt.Errorf("invalid host name %q", host)
	}
	id := NewUUID()
	content := ProjectTemplate(id, toolchains, path)
	if host != "" {
		content = strings.Replace(content, `# host = "aiworkstation"`, `host = "`+host+`"`, 1)
	}
	f, err := os.OpenFile(filepath.Join(real, ProjectFile), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	if err := EnsureLocalDir(real); err != nil {
		return nil, err
	}
	return LoadProject(filepath.Join(real, ProjectFile))
}
