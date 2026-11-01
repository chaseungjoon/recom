<div align="center">

# recom - re(mote) com(pute)

[![CI](https://github.com/chaseungjoon/recom/actions/workflows/ci.yml/badge.svg)](https://github.com/chaseungjoon/recom/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/chaseungjoon/recom?include_prereleases)](https://github.com/chaseungjoon/recom/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**Write locally. Compute remotely. Bring generated files home.**

</div>

recom runs a command on your GPU workstation (or any Linux box you can `ssh`
into) as if it were local.

Edit in your own editor on your laptop, type
`recom python3 train.py`, and recom uploads what changed, installs your
dependencies on the remote machine if they changed, runs the command **only**
there, streams the output, and copies back the files that run produced.

No remote editor, no network filesystem, no daemon, no remote virtualenv to
manage by hand.

```console
$ cat requirements.txt
torch
$ recom init
recom: detected python (requirements.txt): venv + pip install -r requirements.txt → .venv
recom: setting up the python environment on aiworkstation from requirements.txt
…
recom: python environment ready (41.3s)
$ recom python3 src/run.py
recom: synced 1 changed file(s) (343 B) to aiworkstation
running on AIWorkstation x86_64
recom: ← outputs/result.json (96 B)
```

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/chaseungjoon/recom/main/install.sh | sh
```

The script above installs `recom` to `/usr/local/bin` (using sudo if needed, otherwise `~/.local/bin`).
Set `RECOM_VERSION` to pin a release or `RECOM_INSTALL_DIR` to choose the
location:

```sh
curl -fsSL https://raw.githubusercontent.com/chaseungjoon/recom/main/install.sh | RECOM_VERSION=v0.2.0 RECOM_INSTALL_DIR="$HOME/bin" sh
```

Update later with:

```sh
recom update            # sudo recom update if recom is in /usr/local/bin and you don't own it
```

(Homebrew is no longer supported. If you installed recom that way,
`brew uninstall recom` and use the script above.)

### Releases tab

Download the archive for your platform from
[Releases](https://github.com/chaseungjoon/recom/releases), verify it, and
put `recom` on your `PATH`:

```sh
curl -LO https://github.com/chaseungjoon/recom/releases/download/v0.2.0/recom_0.2.0_darwin_arm64.tar.gz
curl -LO https://github.com/chaseungjoon/recom/releases/download/v0.2.0/checksums.txt
shasum -a 256 --check --ignore-missing checksums.txt
tar -xzf recom_0.2.0_darwin_arm64.tar.gz && sudo mv recom /usr/local/bin/
```

### Build from source

Requires Go (see `go.mod`) and `make`:

```sh
git clone https://github.com/chaseungjoon/recom.git && cd recom
make build                      # bin/recom, with the Linux helpers embedded
sudo install -m 0755 bin/recom /usr/local/bin/
```

Use `make build`, not plain `go build`: the client embeds the cross-compiled
Linux helpers it installs on backends.

### Requirements

You need OpenSSH and rsync 3.x locally (on macOS: `brew install rsync`) and,
on the backend, a Linux machine with `sshd`, rsync, and optionally tmux, plus
the language runtimes your projects use (Python, Node.js, …; uv is
recommended for Python). No root: recom installs its own small helper under
`~/.local/share/recom` on the backend.

## Quick start

```sh
recom ssh <username>@<host>    # any ~/.ssh/config alias or user@host
cd ~/Projects/my-model         # with your requirements.txt / pyproject.toml / package.json / …
recom init                     # creates .recom.toml (commit it) and sets up the backend environment
recom python3 src/run.py       # later runs upload only the diff
recom nvidia-smi               # anything: gcc, make, pytest, nvcc, ./bin/main …
```

Run from a subdirectory and the command runs in the same subdirectory
remotely. Outputs land at the matching local paths.

## Dependencies

Declare dependencies the way you already do, once, on your laptop. recom
installs them on the backend and keeps them in sync:

| You have | recom does on the backend | Your commands get |
|---|---|---|
| `requirements.txt`, `pyproject.toml`, `uv.lock`, `poetry.lock`, `Pipfile` | builds `.venv` (with uv when available) | `python3`, `pip`, scripts from `.venv/bin` |
| `environment.yml` | `conda env create -p .conda` | `.conda/bin` |
| `package.json` | `npm` / `pnpm` / `yarn` / `bun install` | `node_modules/.bin` |
| `Cargo.toml`, `go.mod`, `Gemfile`, `composer.json`, `Project.toml`, `renv.lock`, `*.csproj`, `mix.exs`, `pom.xml`, `build.gradle` | `cargo fetch`, `go mod download`, `bundle install`, … | build directories stay remote |

The setup runs before a command only when a dependency file changed, and
`recom init` does the first one. `recom setup` runs it on demand and
`recom setup --clean` rebuilds it from scratch. Anything unusual:
`[setup] command`, or by hand with `recom --shell '…'`. Details in
[environments](docs/environments.md).

## What a run does

1. **Uploads** changed files with rsync. Caches, `.git/`, local virtualenvs,
   build directories and secrets (`.env`, keys, cloud credentials) stay home.
   Nothing is ever deleted remotely. If the upload fails, nothing runs.
2. **Sets up** the environment if a dependency file changed since the last
   setup on that backend (see [Dependencies](#dependencies)).
3. **Runs** the command on the backend with its exact arguments, the backend's
   environment and toolchain, live stdout/stderr, stdin, and its exit code.
   Ctrl-C stops the remote process.
4. **Downloads outputs**: files the run created or changed, verified by
   SHA-256 and installed atomically. A file you edited locally during the run is
   never overwritten (the remote version goes to `.recom/conflicts/`). Large
   files (over 100 MB by default) stay remote until you `recom pull` them.

If your laptop disconnects, outputs are collected by your next `recom` command.

## Long jobs

```sh
recom --detach python3 -u train.py   # prints a job id; survives disconnects
recom jobs
recom logs <id> -f
recom attach <id>                    # live tmux session
recom stop <id>
```

A finished job's outputs come home with your next `recom` command, or `recom pull`.

## More

| | |
|---|---|
| `recom --tty python3 repl.py` | interactive programs (prompts, curses, colors) |
| `recom --shell 'make \| tee log.txt'` | remote pipes and redirection (`recom cmd > f` writes `f` locally); manual environment setup |
| `recom --no-sync nvidia-smi` | skip the upload |
| `recom --no-setup python3 x.py` | skip the environment setup once |
| `recom setup` · `recom update` | prepare the backend now · update recom itself |
| `recom status` · `recom env` · `recom doctor` | pending outputs, remote toolchain/GPU, diagnostics |

- [CLI reference](docs/cli.md): every command, flag and exit code
- [Sync and outputs](docs/sync.md): exactly what is uploaded, what counts as an output, conflicts
- [Environments](docs/environments.md): how dependencies are installed on the backend, per language
- [Configuration](docs/configuration.md): `.recom.toml`, remote environment, Conda wrapper
- [Architecture, security, limitations](docs/architecture.md)
- [Troubleshooting](docs/troubleshooting.md) · [Examples](examples/)

**Limitations (v0.2):** backends must be Linux. Local deletions are not
propagated. While a detached job runs, it owns its project's replica, and
ordinary runs of that project are refused until it ends (`--no-sync` still
works). See
[limitations](docs/architecture.md#limitations).

## Development

```sh
make build              # bin/recom with embedded Linux helpers
make test               # unit tests (race detector)
make test-integration   # end-to-end tests against a throwaway local sshd
```

See [CONTRIBUTING.md](CONTRIBUTING.md) and [SECURITY.md](SECURITY.md).

## License

[MIT](LICENSE)
