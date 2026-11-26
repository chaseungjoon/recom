package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// trustFields are the project settings that make the backend run code chosen
// by .recom.toml, whatever command the user types. A cloned repository could
// set them, so they need the user's approval per working copy.
type trustFields struct {
	SetupCommand string            `json:"setup_command,omitempty"`
	Wrapper      []string          `json:"wrapper,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	Path         []string          `json:"path,omitempty"`
}

func (w *Workspace) trustFields() trustFields {
	return trustFields{
		SetupCommand: strings.TrimSpace(w.Project.Setup.Command),
		Wrapper:      w.Project.Run.Wrapper,
		Env:          w.Project.Run.Env,
		Path:         w.Project.Run.Path,
	}
}

// NeedsTrust reports whether the project sets any setting that runs
// project-chosen code on the backend.
func (w *Workspace) NeedsTrust() bool {
	f := w.trustFields()
	return f.SetupCommand != "" || len(f.Wrapper) > 0 || len(f.Env) > 0 || len(f.Path) > 0
}

// trustPath is the approval marker for the current settings at this root.
// It lives in the user's config directory, never in the project, so a
// repository cannot ship its own approval.
func (w *Workspace) trustPath() string {
	b, _ := json.Marshal(w.trustFields()) // map keys are sorted
	h := sha256.New()
	h.Write([]byte(w.Root))
	h.Write([]byte{0})
	h.Write(b)
	return filepath.Join(Dir(), "trust", hex.EncodeToString(h.Sum(nil)))
}

// Trusted reports whether the current settings were approved for this
// working copy (or need no approval).
func (w *Workspace) Trusted() bool {
	if !w.NeedsTrust() {
		return true
	}
	fi, err := os.Lstat(w.trustPath())
	return err == nil && fi.Mode().IsRegular()
}

// Trust records approval of the current settings for this working copy.
func (w *Workspace) Trust() error {
	p := w.trustPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(w.Root+"\n"), 0o600)
}

// TrustSummary describes the settings that need approval, one per line.
func (w *Workspace) TrustSummary() []string {
	f := w.trustFields()
	var out []string
	if f.SetupCommand != "" {
		out = append(out, fmt.Sprintf("setup.command = %q", f.SetupCommand))
	}
	if len(f.Wrapper) > 0 {
		out = append(out, fmt.Sprintf("run.wrapper   = %q", f.Wrapper))
	}
	if len(f.Env) > 0 {
		keys := make([]string, 0, len(f.Env))
		for k := range f.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out = append(out, fmt.Sprintf("run.env       %s=%q", k, f.Env[k]))
		}
	}
	if len(f.Path) > 0 {
		out = append(out, fmt.Sprintf("run.path      = %q", f.Path))
	}
	return out
}
