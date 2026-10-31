// Package config handles recom's global configuration (registered SSH
// backends), per-project configuration (.recom.toml) and local working-copy
// state (.recom/).
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/BurntSushi/toml"
)

// Host is a registered SSH backend.
type Host struct {
	// Target is what is passed to ssh: an alias from ~/.ssh/config or user@host.
	Target   string `toml:"ssh_target"`
	Port     int    `toml:"port,omitempty"`
	Identity string `toml:"identity,omitempty"`
	// SSHOptions are extra "-o Key=Value" options (validated on registration).
	SSHOptions []string `toml:"ssh_options,omitempty"`
	// RemoteHome / RemoteRoot are absolute paths discovered at registration.
	RemoteHome string    `toml:"remote_home"`
	RemoteRoot string    `toml:"remote_root"`
	OS         string    `toml:"os"`
	Arch       string    `toml:"arch"`
	Added      time.Time `toml:"added"`
	// Multiplex enables OpenSSH connection sharing (ControlMaster). Default true.
	Multiplex *bool `toml:"multiplex,omitempty"`
}

// UseMultiplex reports whether connection sharing is enabled.
func (h *Host) UseMultiplex() bool { return h.Multiplex == nil || *h.Multiplex }

// Global is ~/.config/recom/config.toml.
type Global struct {
	DefaultHost string           `toml:"default_host,omitempty"`
	Hosts       map[string]*Host `toml:"hosts"`
}

var hostNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// ValidHostName reports whether n is usable as a backend name.
func ValidHostName(n string) bool { return hostNameRe.MatchString(n) }

// Dir returns the recom config directory.
func Dir() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" && filepath.IsAbs(d) {
		return filepath.Join(d, "recom")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "recom")
}

// GlobalPath returns the global config file path (RECOM_CONFIG overrides).
func GlobalPath() string {
	if p := os.Getenv("RECOM_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(Dir(), "config.toml")
}

// CacheDir returns recom's cache directory (SSH control sockets).
func CacheDir() string {
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" && filepath.IsAbs(d) {
		return filepath.Join(d, "recom")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "recom")
}

// LoadGlobal reads the global config; a missing file yields an empty config.
func LoadGlobal() (*Global, error) {
	g := &Global{Hosts: map[string]*Host{}}
	p := GlobalPath()
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return g, nil
	}
	if err != nil {
		return nil, err
	}
	md, err := toml.Decode(string(b), g)
	if err != nil {
		return nil, fmt.Errorf("%s is not valid TOML: %w", p, err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		return nil, fmt.Errorf("%s: unknown setting %q", p, und[0].String())
	}
	if g.Hosts == nil {
		g.Hosts = map[string]*Host{}
	}
	for name, h := range g.Hosts {
		if !ValidHostName(name) || h == nil || h.Target == "" {
			return nil, fmt.Errorf("%s: host %q is incomplete; re-register it with `recom ssh`", p, name)
		}
	}
	return g, nil
}

// Save writes the global config atomically with private permissions.
func (g *Global) Save() error {
	p := GlobalPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.WriteString("# recom global configuration — registered SSH backends.\n# Managed by `recom ssh` / `recom default`; safe to edit by hand.\n\n")
	if err := toml.NewEncoder(&buf).Encode(g); err != nil {
		return err
	}
	return WriteFileAtomic(p, buf.Bytes(), 0o600)
}

// HostNames returns sorted host names.
func (g *Global) HostNames() []string {
	names := make([]string, 0, len(g.Hosts))
	for n := range g.Hosts {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// WriteFileAtomic writes data via a temp file in the same directory + rename.
func WriteFileAtomic(p string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(p), ".tmp-"+filepath.Base(p)+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		return err
	}
	ok = true
	return nil
}
