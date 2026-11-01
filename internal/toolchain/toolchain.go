// Package toolchain detects a project's language toolchains from the
// dependency files on the local machine and describes how to prepare the
// matching environment on the backend: a Python .venv from requirements.txt
// or pyproject.toml, node_modules from package.json, a Cargo fetch, and so on.
//
// Everything here is computed locally and is deterministic. A Plan's
// Fingerprint changes exactly when its dependency files (or recom's setup
// script for them) change, which is when the backend has to be set up again.
package toolchain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Auto is the [setup] toolchains entry that means "detect on every run".
const Auto = "auto"

// Custom is the plan name of the user's [setup] command.
const Custom = "custom"

// StampDir is where setup scripts record what they installed, relative to
// the replica root. It lives inside .recom/, which is never uploaded,
// downloaded or treated as an output.
const StampDir = ".recom/setup"

// Plan describes one toolchain's backend environment.
type Plan struct {
	Name    string   // config name, e.g. "python"
	Summary string   // human description, e.g. "uv sync"
	Sources []string // dependency files that triggered detection (for messages)

	// Script is a POSIX sh fragment run in the replica root that installs the
	// dependencies. Empty means there is nothing to install (the toolchain
	// still contributes PATH and excludes).
	Script string
	// Requires is a replica-relative path that must exist for a previous
	// setup to count (e.g. ".venv/bin/python"); deleting it on the backend
	// forces a new setup.
	Requires string

	Path []string // PATH entries for runs (replica-relative)
	// PathFallback are backend directories appended to PATH (setup and runs)
	// when they exist: where version managers install tools that only
	// interactive shells put on PATH (nvm, volta, rustup, ...).
	PathFallback []string
	Env          map[string]string // literal environment variables for runs
	RelEnv       map[string]string // run environment variables whose values are replica-relative paths

	// EnvDirs are the replica-relative environment directories that
	// `recom setup --clean` deletes before rebuilding.
	EnvDirs []string

	SyncExclude   []string // upload excludes (local build/env directories)
	OutputExclude []string // never treated as run outputs (backend build/env directories)

	// Watch lists the files whose content feeds the fingerprint.
	Watch []string
	// Fingerprint identifies Script plus the content of Watch.
	Fingerprint string
}

// Stamp is the content a successful setup leaves in StampDir/<Name>.
func (p Plan) Stamp() string { return p.Fingerprint + "\n" + p.Requires + "\n" }

// FullScript is the complete program run on the backend with /bin/sh -c:
// helpers, the setup body, and the stamp that records success.
func (p Plan) FullScript() string {
	var b strings.Builder
	b.WriteString(prelude)
	b.WriteString(p.Script)
	if !strings.HasSuffix(p.Script, "\n") {
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "mkdir -p %s\nprintf '%%s\\n%%s\\n' %s %s > %s/%s.tmp\nmv -f %s/%s.tmp %s/%s\n",
		StampDir, Quote(p.Fingerprint), Quote(p.Requires), StampDir, p.Name, StampDir, p.Name, StampDir, p.Name)
	return b.String()
}

const prelude = `set -e
have() { command -v "$1" >/dev/null 2>&1; }
note() { printf 'recom: %s\n' "$*" >&2; }
need() { have "$1" || { note "$1 is not installed on the backend (or not on its PATH): $2"; exit 127; }; }
`

type detector struct {
	name   string
	detect func(root string) *Plan
}

// detectors in the order setups run. Conda comes before Python so that a
// project with environment.yml gets the Conda environment, not a .venv.
var detectors = []detector{
	{"conda", detectConda},
	{"python", detectPython},
	{"node", detectNode},
	{"rust", detectRust},
	{"go", detectGo},
	{"maven", detectMaven},
	{"gradle", detectGradle},
	{"ruby", detectRuby},
	{"php", detectPHP},
	{"julia", detectJulia},
	{"r", detectR},
	{"dotnet", detectDotnet},
	{"elixir", detectElixir},
}

// Names lists every supported toolchain name.
func Names() []string {
	out := make([]string, len(detectors))
	for i, d := range detectors {
		out[i] = d.name
	}
	return out
}

// Known reports whether name is a valid [setup] toolchains entry.
func Known(name string) bool {
	if name == Auto {
		return true
	}
	for _, d := range detectors {
		if d.name == name {
			return true
		}
	}
	return false
}

// Config is the [setup] section of .recom.toml.
type Config struct {
	// Toolchains: nil or containing "auto" detects on every run; an empty
	// list disables automatic setup; names restrict detection to those.
	Toolchains []string
	Command    string   // extra backend command run after the toolchain setups
	Watch      []string // files (globs) whose change re-runs Command
}

// Result is the resolved setup for a project.
type Result struct {
	Plans []Plan
	// Hints are informational notes, e.g. a dependency file of a toolchain
	// that the project's explicit list leaves out.
	Hints []string
}

// Detect returns the toolchains found in root, ignoring configuration. It
// is what `recom init` records.
func Detect(root string) []Plan {
	return Resolve(root, Config{}).Plans
}

// Resolve computes the plans for a project.
func Resolve(root string, c Config) Result {
	var res Result
	auto := c.Toolchains == nil
	want := map[string]bool{}
	for _, n := range c.Toolchains {
		if n == Auto {
			auto = true
		}
		want[n] = true
	}
	var found []string
	for _, d := range detectors {
		p := d.detect(root)
		if p != nil && d.name == "python" && contains(found, "conda") && !want["python"] {
			continue // environment.yml provides Python
		}
		switch {
		case p != nil && (auto || want[d.name]):
			p.Name = d.name
			p.Fingerprint = fingerprint(root, p)
			res.Plans = append(res.Plans, *p)
			found = append(found, d.name)
		case p == nil && want[d.name]:
			res.Hints = append(res.Hints, fmt.Sprintf("[setup] lists %q but no %s dependency file was found at the project root", d.name, d.name))
		case p != nil && len(c.Toolchains) > 0:
			res.Hints = append(res.Hints, fmt.Sprintf("found %s but %q is not in [setup] toolchains in .recom.toml; add it (or \"auto\") to have recom set it up", strings.Join(p.Sources, ", "), d.name))
		}
	}
	if strings.TrimSpace(c.Command) != "" {
		p := Plan{
			Name:    Custom,
			Summary: c.Command,
			Script:  `"${SHELL:-/bin/sh}" -c ` + Quote(c.Command) + "\n",
			Watch:   globs(root, c.Watch),
		}
		p.Sources = p.Watch
		p.Fingerprint = fingerprint(root, &p)
		res.Plans = append(res.Plans, p)
	}
	return res
}

// Fallbacks returns the union of the plans' PathFallback entries.
func Fallbacks(plans []Plan) []string {
	var out []string
	for _, p := range plans {
		for _, d := range p.PathFallback {
			if !contains(out, d) {
				out = append(out, d)
			}
		}
	}
	return out
}

// Paths returns the union of the plans' PATH entries, in order.
func Paths(plans []Plan) []string {
	var out []string
	for _, p := range plans {
		for _, d := range p.Path {
			if !contains(out, d) {
				out = append(out, d)
			}
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func fingerprint(root string, p *Plan) string {
	h := sha256.New()
	fmt.Fprintf(h, "recom-setup/1\x00%s\x00%s\x00%s\x00", p.Name, p.Script, p.Requires)
	watch := append([]string{}, p.Watch...)
	sort.Strings(watch)
	for _, w := range watch {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(w)))
		sum := sha256.Sum256(b)
		if err != nil {
			sum = [32]byte{}
		}
		fmt.Fprintf(h, "%s\x00%x\x00", w, sum)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// exists reports which of names are regular files at root.
func exists(root string, names ...string) []string {
	var out []string
	for _, n := range names {
		if fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(n))); err == nil && fi.Mode().IsRegular() {
			out = append(out, n)
		}
	}
	return out
}

func has(root, name string) bool { return len(exists(root, name)) == 1 }

// globs expands slash-separated glob patterns relative to root to regular files.
func globs(root string, patterns []string) []string {
	var out []string
	for _, pat := range patterns {
		m, _ := filepath.Glob(filepath.Join(root, filepath.FromSlash(pat)))
		for _, f := range m {
			if fi, err := os.Stat(f); err != nil || !fi.Mode().IsRegular() {
				continue
			}
			rel, err := filepath.Rel(root, f)
			if err == nil && !contains(out, filepath.ToSlash(rel)) {
				out = append(out, filepath.ToSlash(rel))
			}
		}
	}
	sort.Strings(out)
	return out
}

var safeArg = regexp.MustCompile(`^[A-Za-z0-9_./=:@%+,-]+$`)

// Quote single-quotes s for POSIX sh.
func Quote(s string) string {
	if s != "" && safeArg.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func quoteAll(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = Quote(a)
	}
	return strings.Join(q, " ")
}
