# Environments: dependencies on the backend

You declare dependencies once, in your project's usual files. recom
installs them **on the backend**, in the project's remote replica, so that
`recom python3 main.py` behaves as `python3 main.py` would on your laptop
with the project's virtualenv active.

```sh
mkdir my-model && cd my-model
echo "torch" > requirements.txt     # 1. declare dependencies as usual
$EDITOR main.py                     # 2. write code
recom init                          # 3. detects requirements.txt, creates .venv on the backend
recom python3 main.py               # 4. runs with the backend's .venv; 5. output streams in
```

## How it works

1. **Detection (local).** recom looks at the project root for the files in
   the table below. `recom init` records the toolchains it found in
   `.recom.toml` (`[setup] toolchains = ["python"]`) together with their
   directories on `PATH` (`[run] path = [".venv/bin"]`).
2. **Fingerprint.** For each toolchain, recom hashes its dependency files
   (e.g. `requirements*.txt`, `pyproject.toml`, `uv.lock`, `.python-version`)
   together with the setup script it will run. Editing your code never
   changes the fingerprint; editing a dependency file does.
3. **Setup (remote).** After uploading and before running your command,
   recom compares the fingerprint with the one recorded in the replica. If
   they differ, or the environment was deleted on the backend, the setup
   runs first and its output streams to your terminal's stderr (so
   `recom python3 main.py > out.txt` still captures only your program).
   When nothing changed, this costs nothing: the check rides on the
   connection recom opens anyway.
4. **Run.** Your command gets the toolchain directories prepended to `PATH`
   (`.venv/bin`, `node_modules/.bin`, ...), and variables such as
   `VIRTUAL_ENV`.

`recom init` performs the first setup right away when a backend is
registered, so your first run starts immediately. A failed setup stops the
run with exit status 125 and the command does not run.

Environments live only on the backend: `.venv/`, `node_modules/`,
`target/`, ... are never uploaded from your laptop (they would hold macOS
binaries) and never downloaded as outputs.

## Supported toolchains

| Toolchain | Detected from | What recom runs on the backend | Run environment | Stays on the backend |
|---|---|---|---|---|
| `python` | `uv.lock`, or a `pyproject.toml` with `[project]` | `uv sync` (falls back to a venv + pip if uv is missing) | `.venv/bin` on PATH, `VIRTUAL_ENV` | `.venv/` |
| | `poetry.lock` / `[tool.poetry]` | `poetry install` with an in-project `.venv` | same | same |
| | `Pipfile` | `pipenv install` with an in-project `.venv` | same | same |
| | `requirements.txt` | create `.venv` (uv, else `python3 -m venv`), then `pip install -r requirements.txt` | same | same |
| | `setup.py` / `setup.cfg` / `[build-system]` | create `.venv`, then `pip install -e .` | same | same |
| `conda` | `environment.yml` / `.yaml` | `conda env create -p .conda` (update with `--prune` later); micromamba works too | `.conda/bin` on PATH, `CONDA_PREFIX` | `.conda/` |
| `node` | `package.json` | `npm install`, or `pnpm` / `yarn` / `bun install` (from `packageManager` or the lockfile; via corepack if needed) | `node_modules/.bin` on PATH | `node_modules/` |
| `rust` | `Cargo.toml` | `cargo fetch` | | `target/` |
| `go` | `go.mod` | `go mod download` (skipped with `vendor/`) | | |
| `maven` | `pom.xml` | nothing (Maven resolves dependencies on the first build) | | `target/` |
| `gradle` | `build.gradle(.kts)`, `settings.gradle(.kts)` | nothing (Gradle resolves on the first build) | | `build/`, `.gradle/` |
| `ruby` | `Gemfile` | `bundle install` into `vendor/bundle` | `RUBYOPT=-rbundler/setup`, so plain `ruby app.rb` sees the bundle | `vendor/bundle/`, `.bundle/` |
| `php` | `composer.json` | `composer install` | `vendor/bin` on PATH | `vendor/` |
| `julia` | `Project.toml` | `Pkg.instantiate()` | `JULIA_PROJECT` | |
| `r` | `renv.lock` | `renv::restore()` | (renv activates via `.Rprofile`) | `renv/library/` |
| `dotnet` | `*.sln`, `*.csproj`, `*.fsproj` | `dotnet restore` | | `bin/`, `obj/` |
| `elixir` | `mix.exs` | `mix deps.get` | | `deps/`, `_build/` |

Python details:

- A `requirements.txt` wins over a `pyproject.toml` without a lock file:
  many projects keep tool settings (ruff, black) in `pyproject.toml` and
  their dependencies in `requirements.txt`. A `pyproject.toml` that only has
  `[tool.*]` sections is not an environment.
- `uv` is preferred whenever the backend has it: installs are much faster,
  and uv honors `.python-version` and `requires-python` (downloading that
  Python if needed). Without uv, recom uses the `python3.X` that
  `.python-version` names, else `python3`.
- Only `requirements.txt` is installed, but every `requirements*.txt` and
  `requirements/*.txt` file is watched, so changing a file it includes with
  `-r` also triggers a new setup.
- With both `environment.yml` and `requirements.txt`, Conda wins (put pip
  packages under `pip:` in `environment.yml`).

Build directories (`target/`, `build/`, `bin/` + `obj/`) stay on the backend
because they hold Linux build artifacts. Fetch one explicitly when you need
it: `recom pull target/release/myapp`.

### Tools installed by version managers

nvm, fnm, volta, asdf, mise, rbenv, rustup and juliaup often only hook into
*interactive* shells, which SSH commands do not start. For the detected
toolchains, recom also looks in their usual install directories
(`~/.nvm/versions/node/*/bin`, picking the newest version, `~/.volta/bin`,
`~/.cargo/bin`, `/usr/local/go/bin`, `~/.juliaup/bin`, ...) and appends the
ones that exist to the **end** of `PATH`, so anything already on your
`PATH` still wins. `recom env` shows the result.

recom never installs language runtimes or package managers for you. If one
is missing, the setup fails with a message naming the tool to install.

## Configuration

```toml
[setup]
toolchains = ["python", "node"]   # what init detected; "auto" = detect on every run; [] = off
command = "make deps"             # optional extra step, after the toolchains
watch = ["deps/*.txt", "Makefile"] # files whose change re-runs `command`
```

- **`toolchains`.** With an explicit list, a dependency file of a toolchain
  that is not listed is ignored (`recom setup` points it out). Projects
  created by recom 0.1 have no `[setup]` section and behave like `"auto"`.
- **`command`.** Runs with your remote `$SHELL` in the project root, after
  the toolchain setups, with their directories on `PATH` (so `pip` is the
  venv's pip). It runs once, then again whenever a file matching `watch`
  changes or the command itself changes.
- **`[run] path` / `env`** still apply. `[run] env` overrides toolchain
  variables, and an empty value removes one (e.g. `RUBYOPT = ""`).

## Commands

```sh
recom setup              # upload and run the setup now (only steps whose files changed)
recom setup --force      # re-run every step
recom setup --clean      # delete .venv / node_modules / ... on the backend and rebuild
recom --no-setup <cmd>   # skip the setup for one run
recom status             # shows "Setup  python (requirements.txt): ready"
recom env                # PATH and tools as your commands see them
```

### Manual control: `--shell`

Automatic setup covers the common layouts. For anything else, turn it off
(`toolchains = []`) or keep it and add a `command`. You can also prepare the
backend by hand:

```sh
recom --shell 'uv venv .venv && uv pip install -r requirements-gpu.txt --index-url https://download.pytorch.org/whl/cu128'
```

`--shell` runs any remote shell expression in the project replica. Point
`[run] path` at whatever you create (`.venv/bin` is already there for Python
projects).

## Limitations

- Detection only looks at the project root. In a monorepo, run recom from
  the root, or use `[setup] command` for subprojects.
- Removing a package from `requirements.txt` does not uninstall it from the
  existing `.venv` (as with `pip install -r` on your laptop). Use
  `recom setup --clean` for a fresh environment.
- recom does not upload `.npmrc`, `.pypirc` or `.netrc` (they usually hold
  tokens). For private registries, configure them on the backend, or opt in
  with `[sync] include`.
