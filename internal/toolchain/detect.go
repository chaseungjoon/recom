package toolchain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// ---------------------------------------------------------------- Python

// Python helpers shared by every Python strategy: create .venv if needed
// (uv when available, else the newest matching python3 -m venv), and install
// into it with uv pip or pip.
const pythonLib = `venv() {
  if [ -x .venv/bin/python ]; then return 0; fi
  rm -rf .venv
  if have uv; then uv venv .venv; return; fi
  py=
  for c in $PYS python3 python; do if have "$c"; then py=$c; break; fi; done
  [ -n "$py" ] || { note "no python3 on the backend's PATH; install Python (or uv) there"; exit 127; }
  if ! "$py" -m venv .venv; then
    rm -rf .venv
    note "'$py -m venv' failed; on Debian/Ubuntu install the venv module (sudo apt install python3-venv), or install uv on the backend"
    exit 1
  fi
}
pipi() {
  if have uv; then VIRTUAL_ENV="$PWD/.venv" uv pip install "$@"
  else .venv/bin/python -m pip install --disable-pip-version-check "$@"; fi
}
`

var pyVersionRe = regexp.MustCompile(`^(\d+)\.(\d+)`)

type pyproject struct {
	Project *struct {
		Dependencies []string `toml:"dependencies"`
	} `toml:"project"`
	BuildSystem *map[string]any `toml:"build-system"`
	Tool        struct {
		Poetry *map[string]any `toml:"poetry"`
	} `toml:"tool"`
	DependencyGroups map[string][]any `toml:"dependency-groups"`
}

func detectPython(root string) *Plan {
	var pp pyproject
	hasPyproject := has(root, "pyproject.toml")
	if hasPyproject {
		if _, err := toml.DecodeFile(filepath.Join(root, "pyproject.toml"), &pp); err != nil {
			pp = pyproject{}
		}
	}
	isProject := pp.Project != nil
	isPoetry := pp.Tool.Poetry != nil
	isPackage := pp.BuildSystem != nil || has(root, "setup.py") || has(root, "setup.cfg")

	watch := exists(root, "pyproject.toml", "uv.lock", "poetry.lock", "Pipfile", "Pipfile.lock", "setup.py", "setup.cfg", ".python-version")
	watch = append(watch, globs(root, []string{"requirements*.txt", "requirements/*.txt"})...)

	// The pip fallback for a pyproject.toml: install the project itself if
	// it is a package, else just its dependencies (+ the "dev" group, as uv does).
	pipProject := ""
	if isPackage {
		pipProject = "pipi -e .\n"
	} else if isProject {
		deps := append([]string{}, pp.Project.Dependencies...)
		for _, d := range pp.DependencyGroups["dev"] {
			if s, ok := d.(string); ok {
				deps = append(deps, s)
			}
		}
		if len(deps) > 0 {
			pipProject = "pipi " + quoteAll(deps) + "\n"
		}
	}

	var summary, body string
	var sources []string
	switch {
	case has(root, "uv.lock") || (isProject && !isPoetry && !has(root, "requirements.txt") && !has(root, "Pipfile")):
		sources = exists(root, "uv.lock", "pyproject.toml")
		summary = "uv sync → .venv"
		body = "if have uv; then\n  uv sync\nelse\n  note \"uv is not installed on the backend; creating .venv with pip instead (install uv there for locked, faster installs)\"\n  venv\n" + indent(pipProject) + "fi\n"
	case has(root, "poetry.lock") || isPoetry:
		sources = exists(root, "poetry.lock", "pyproject.toml")
		summary = "poetry install → .venv"
		fallback := "  note \"poetry is not installed on the backend; install it there (pipx install poetry)\"\n  exit 127\n"
		if isPackage {
			fallback = "  note \"poetry is not installed on the backend; installing the project with pip instead\"\n  venv\n  pipi -e .\n"
		}
		body = "if have poetry; then\n  POETRY_VIRTUALENVS_IN_PROJECT=true poetry install --no-interaction\nelse\n" + fallback + "fi\n"
	case has(root, "Pipfile"):
		sources = exists(root, "Pipfile", "Pipfile.lock")
		summary = "pipenv install → .venv"
		fallback := "  note \"pipenv is not installed on the backend; install it there (pipx install pipenv)\"\n  exit 127\n"
		if has(root, "requirements.txt") {
			fallback = "  note \"pipenv is not installed on the backend; installing requirements.txt with pip instead\"\n  venv\n  pipi -r requirements.txt\n"
		}
		body = "if have pipenv; then\n  PIPENV_VENV_IN_PROJECT=1 PIPENV_YES=1 PIPENV_NOSPIN=1 pipenv install\nelse\n" + fallback + "fi\n"
	case has(root, "requirements.txt"):
		sources = []string{"requirements.txt"}
		summary = "venv + pip install -r requirements.txt → .venv"
		body = "venv\npipi -r requirements.txt\n"
	case isPackage:
		sources = exists(root, "pyproject.toml", "setup.py", "setup.cfg")
		summary = "venv + pip install -e . → .venv"
		body = "venv\npipi -e .\n"
	default:
		return nil
	}

	// python3 -m venv fallback: prefer the interpreter .python-version asks for.
	pys := ""
	if b, err := os.ReadFile(filepath.Join(root, ".python-version")); err == nil {
		if m := pyVersionRe.FindStringSubmatch(strings.TrimSpace(string(b))); m != nil {
			pys = "python" + m[1] + "." + m[2]
		}
	}
	return &Plan{
		PathFallback: []string{"~/.local/bin", "~/.cargo/bin"},
		Summary:      summary,
		Sources:      sources,
		Script:       "PYS=" + Quote(pys) + "\n" + pythonLib + body,
		Requires:     ".venv/bin/python",
		EnvDirs:      []string{".venv"},
		Path:         []string{".venv/bin"},
		RelEnv:       map[string]string{"VIRTUAL_ENV": ".venv"},
		Watch:        watch,
	}
}

func indent(s string) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n") + "\n"
}

// ---------------------------------------------------------------- Conda

func detectConda(root string) *Plan {
	files := exists(root, "environment.yml", "environment.yaml")
	if len(files) == 0 {
		return nil
	}
	f := files[0]
	script := `f=` + Quote(f) + `
CONDA=
for c in conda micromamba; do if have "$c"; then CONDA=$(command -v "$c"); break; fi; done
if [ -z "$CONDA" ]; then
  for c in "$HOME/miniforge3/bin/conda" "$HOME/mambaforge/bin/conda" "$HOME/miniconda3/bin/conda" "$HOME/anaconda3/bin/conda" /opt/conda/bin/conda; do
    if [ -x "$c" ]; then CONDA=$c; break; fi
  done
fi
[ -n "$CONDA" ] || { note "conda (or micromamba) is not installed on the backend; install Miniforge there: https://conda-forge.org/download/"; exit 127; }
case "$CONDA" in
  *micromamba) "$CONDA" create -y -p "$PWD/.conda" -f "$f" ;;
  *) if [ -d .conda/conda-meta ]; then "$CONDA" env update -p "$PWD/.conda" -f "$f" --prune
     else "$CONDA" env create -p "$PWD/.conda" -f "$f"; fi ;;
esac
`
	return &Plan{
		Summary:  "conda env from " + f + " → .conda",
		Sources:  files,
		Script:   script,
		Requires: ".conda/conda-meta",
		EnvDirs:  []string{".conda"},
		Path:     []string{".conda/bin"},
		RelEnv:   map[string]string{"CONDA_PREFIX": ".conda"},
		Watch:    files,
	}
}

// ---------------------------------------------------------------- Node.js

// nodeFallback finds Node.js installed by version managers that only hook
// into interactive shells.
var nodeFallback = []string{
	"~/.volta/bin",
	"~/.nvm/current/bin",
	"~/.nvm/versions/node/*/bin",
	"~/.local/share/fnm/aliases/default/bin",
	"~/.fnm/aliases/default/bin",
	"~/.local/share/mise/shims",
	"~/.asdf/shims",
	"~/.bun/bin",
	"~/.local/share/pnpm",
}

func detectNode(root string) *Plan {
	if !has(root, "package.json") {
		return nil
	}
	pm := ""
	var pkg struct {
		PackageManager string `json:"packageManager"`
	}
	if b, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil && json.Unmarshal(b, &pkg) == nil {
		name, _, _ := strings.Cut(pkg.PackageManager, "@")
		switch name {
		case "npm", "pnpm", "yarn", "bun":
			pm = name
		}
	}
	if pm == "" {
		switch {
		case has(root, "pnpm-lock.yaml"):
			pm = "pnpm"
		case has(root, "yarn.lock"):
			pm = "yarn"
		case has(root, "bun.lock") || has(root, "bun.lockb"):
			pm = "bun"
		default:
			pm = "npm"
		}
	}
	install := map[string]string{
		"npm":  "npm install --no-audit --no-fund",
		"pnpm": "run_pm install --config.confirmModulesPurge=false",
		"yarn": "run_pm install",
		"bun":  "bun install",
	}[pm]
	script := ""
	switch pm {
	case "bun":
		script = "need bun \"install Bun there: https://bun.sh\"\n"
	case "npm":
		script = "need node \"install Node.js there (if you use nvm, add its bin directory to [run] path in .recom.toml)\"\nneed npm \"install npm there\"\n"
	default:
		script = "need node \"install Node.js there (if you use nvm, add its bin directory to [run] path in .recom.toml)\"\n" +
			"run_pm() { if have " + pm + "; then " + pm + " \"$@\"; elif have corepack; then corepack " + pm + " \"$@\"; else note \"" + pm + " is not installed on the backend; install it or enable corepack there\"; exit 127; fi; }\n"
	}
	script += install + "\n"
	requires := "node_modules"
	if pm == "yarn" {
		requires = "" // Yarn Plug'n'Play has no node_modules
	}
	return &Plan{
		PathFallback: nodeFallback,
		Summary:      strings.TrimPrefix(install, "run_pm ") + " → node_modules",
		Sources:      []string{"package.json"},
		Script:       script,
		Requires:     requires,
		EnvDirs:      []string{"node_modules"},
		Path:         []string{"node_modules/.bin"},
		Watch:        exists(root, "package.json", "package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "pnpm-workspace.yaml", "yarn.lock", ".yarnrc.yml", "bun.lock", "bun.lockb", ".nvmrc", ".node-version"),
	}
}

// ---------------------------------------------------------------- Rust

func detectRust(root string) *Plan {
	if !has(root, "Cargo.toml") {
		return nil
	}
	return &Plan{
		PathFallback:  []string{"~/.cargo/bin"},
		Summary:       "cargo fetch (target/ stays on the backend)",
		Sources:       []string{"Cargo.toml"},
		Script:        "need cargo \"install Rust there with rustup: https://rustup.rs\"\ncargo fetch\n",
		SyncExclude:   []string{"/target/"},
		OutputExclude: []string{"/target/"},
		Watch:         exists(root, "Cargo.toml", "Cargo.lock", "rust-toolchain", "rust-toolchain.toml"),
	}
}

// ---------------------------------------------------------------- Go

func detectGo(root string) *Plan {
	if !has(root, "go.mod") {
		return nil
	}
	return &Plan{
		PathFallback: []string{"/usr/local/go/bin", "~/go/bin", "~/.local/go/bin"},
		Summary:      "go mod download",
		Sources:      []string{"go.mod"},
		Script:       "need go \"install Go there: https://go.dev/dl/\"\nif [ ! -f vendor/modules.txt ]; then go mod download; fi\n",
		Watch:        exists(root, "go.mod", "go.sum", "go.work", "go.work.sum"),
	}
}

// ---------------------------------------------------------------- JVM

// Maven and Gradle resolve dependencies on the first build, so there is
// nothing to install up front; recom only keeps build directories on the
// backend (local ones are not uploaded, remote ones are not downloaded).
func detectMaven(root string) *Plan {
	if !has(root, "pom.xml") {
		return nil
	}
	return &Plan{
		Summary:       "Maven (dependencies resolve on the first build; target/ stays on the backend)",
		Sources:       []string{"pom.xml"},
		SyncExclude:   []string{"target/"},
		OutputExclude: []string{"target/"},
	}
}

func detectGradle(root string) *Plan {
	files := exists(root, "build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts")
	if len(files) == 0 {
		return nil
	}
	return &Plan{
		Summary:       "Gradle (dependencies resolve on the first build; build/ stays on the backend)",
		Sources:       files[:1],
		SyncExclude:   []string{"build/", ".gradle/", ".kotlin/"},
		OutputExclude: []string{"build/", ".gradle/", ".kotlin/"},
	}
}

// ---------------------------------------------------------------- Ruby

func detectRuby(root string) *Plan {
	if !has(root, "Gemfile") {
		return nil
	}
	return &Plan{
		PathFallback: []string{"~/.rbenv/shims", "~/.rbenv/bin", "~/.asdf/shims", "~/.local/share/mise/shims", "~/.gem/ruby/*/bin"},
		Summary:      "bundle install → vendor/bundle",
		Sources:      []string{"Gemfile"},
		Script:       "need bundle \"install Ruby and Bundler there (gem install bundler)\"\nbundle config set --local path vendor/bundle\nbundle install\n",
		Requires:     "vendor/bundle",
		EnvDirs:      []string{"vendor/bundle", ".bundle"},
		// Load the bundle for every ruby process, like `bundle exec` does, so
		// `recom ruby app.rb` behaves as it does with system-wide gems.
		Env:           map[string]string{"RUBYOPT": "-rbundler/setup"},
		RelEnv:        map[string]string{"BUNDLE_GEMFILE": "Gemfile"},
		SyncExclude:   []string{"/vendor/bundle/", "/.bundle/"},
		OutputExclude: []string{"/vendor/bundle/", "/.bundle/"},
		Watch:         exists(root, "Gemfile", "Gemfile.lock", ".ruby-version"),
	}
}

// ---------------------------------------------------------------- PHP

func detectPHP(root string) *Plan {
	if !has(root, "composer.json") {
		return nil
	}
	return &Plan{
		PathFallback:  []string{"~/.config/composer/vendor/bin", "~/.composer/vendor/bin"},
		Summary:       "composer install → vendor",
		Sources:       []string{"composer.json"},
		Script:        "need composer \"install Composer there: https://getcomposer.org/download/\"\ncomposer install --no-interaction\n",
		Requires:      "vendor",
		EnvDirs:       []string{"vendor"},
		Path:          []string{"vendor/bin"},
		SyncExclude:   []string{"/vendor/"},
		OutputExclude: []string{"/vendor/"},
		Watch:         exists(root, "composer.json", "composer.lock"),
	}
}

// ---------------------------------------------------------------- Julia

func detectJulia(root string) *Plan {
	files := exists(root, "Project.toml", "JuliaProject.toml")
	if len(files) == 0 {
		return nil
	}
	return &Plan{
		PathFallback: []string{"~/.juliaup/bin"},
		Summary:      "Pkg.instantiate()",
		Sources:      files,
		Script:       "need julia \"install Julia there: https://julialang.org/install/\"\njulia --project=. -e 'using Pkg; Pkg.instantiate()'\n",
		RelEnv:       map[string]string{"JULIA_PROJECT": "."},
		Watch:        exists(root, "Project.toml", "JuliaProject.toml", "Manifest.toml", "JuliaManifest.toml"),
	}
}

// ---------------------------------------------------------------- R

func detectR(root string) *Plan {
	if !has(root, "renv.lock") {
		return nil
	}
	return &Plan{
		Summary:       "renv::restore()",
		Sources:       []string{"renv.lock"},
		Script:        "need Rscript \"install R there\"\nRscript -e 'renv::restore(prompt = FALSE)'\n",
		Requires:      "renv/library",
		EnvDirs:       []string{"renv/library"},
		SyncExclude:   []string{"/renv/library/", "/renv/staging/", "/renv/sandbox/"},
		OutputExclude: []string{"/renv/library/", "/renv/staging/", "/renv/sandbox/"},
		Watch:         exists(root, "renv.lock", ".Rprofile", "renv/activate.R"),
	}
}

// ---------------------------------------------------------------- .NET

func detectDotnet(root string) *Plan {
	sln := globs(root, []string{"*.sln", "*.slnx"})
	proj := globs(root, []string{"*.csproj", "*.fsproj", "*.vbproj"})
	target := ""
	switch {
	case len(sln) > 0:
		target = sln[0]
	case len(proj) > 0:
		target = proj[0]
	default:
		return nil
	}
	return &Plan{
		PathFallback:  []string{"~/.dotnet"},
		Summary:       "dotnet restore " + target,
		Sources:       []string{target},
		Script:        "need dotnet \"install the .NET SDK there: https://dot.net\"\ndotnet restore " + Quote(target) + "\n",
		SyncExclude:   []string{"bin/", "obj/"},
		OutputExclude: []string{"bin/", "obj/"},
		Watch:         append(append(append([]string{}, sln...), proj...), exists(root, "global.json", "Directory.Build.props", "Directory.Packages.props", "NuGet.config", "packages.lock.json")...),
	}
}

// ---------------------------------------------------------------- Elixir

func detectElixir(root string) *Plan {
	if !has(root, "mix.exs") {
		return nil
	}
	return &Plan{
		PathFallback:  []string{"~/.asdf/shims", "~/.local/share/mise/shims"},
		Summary:       "mix deps.get",
		Sources:       []string{"mix.exs"},
		Script:        "need mix \"install Elixir there: https://elixir-lang.org/install.html\"\nmix local.hex --force\nmix local.rebar --force\nmix deps.get\n",
		SyncExclude:   []string{"/deps/", "/_build/"},
		OutputExclude: []string{"/deps/", "/_build/"},
		Watch:         exists(root, "mix.exs", "mix.lock"),
	}
}
