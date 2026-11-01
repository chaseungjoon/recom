<div align="center">

# recom

[![CI](https://github.com/chaseungjoon/recom/actions/workflows/ci.yml/badge.svg)](https://github.com/chaseungjoon/recom/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/chaseungjoon/recom?include_prereleases)](https://github.com/chaseungjoon/recom/releases)
[![License: GPL v3](https://img.shields.io/badge/license-GPLv3-red.svg)](LICENSE)

[![Python](https://img.shields.io/badge/Python-3776AB?logo=python&logoColor=white)](docs/environments.md#supported-toolchains)
[![C](https://img.shields.io/badge/C-A8B9CC?logo=c&logoColor=black)](examples/c-cpp)
[![C++](https://img.shields.io/badge/C%2B%2B-00599C?logo=cplusplus&logoColor=white)](examples/c-cpp)
[![CUDA](https://img.shields.io/badge/CUDA-76B900?logo=nvidia&logoColor=white)](examples/cuda-pytorch)
[![Rust](https://img.shields.io/badge/Rust-000000?logo=rust&logoColor=white)](docs/environments.md#supported-toolchains)
[![JavaScript](https://img.shields.io/badge/JavaScript-F7DF1E?logo=javascript&logoColor=black)](docs/environments.md#supported-toolchains)
[![TypeScript](https://img.shields.io/badge/TypeScript-3178C6?logo=typescript&logoColor=white)](docs/environments.md#supported-toolchains)
[![Go](https://img.shields.io/badge/Go-00ADD8?logo=go&logoColor=white)](docs/environments.md#supported-toolchains)
[![Java](https://img.shields.io/badge/Java-ED8B00?logo=openjdk&logoColor=white)](docs/environments.md#supported-toolchains)
[![Kotlin](https://img.shields.io/badge/Kotlin-7F52FF?logo=kotlin&logoColor=white)](docs/environments.md#supported-toolchains)
[![Ruby](https://img.shields.io/badge/Ruby-CC342D?logo=ruby&logoColor=white)](docs/environments.md#supported-toolchains)
[![PHP](https://img.shields.io/badge/PHP-777BB4?logo=php&logoColor=white)](docs/environments.md#supported-toolchains)
[![Julia](https://img.shields.io/badge/Julia-9558B2?logo=julia&logoColor=white)](docs/environments.md#supported-toolchains)
[![R](https://img.shields.io/badge/R-276DC3?logo=r&logoColor=white)](docs/environments.md#supported-toolchains)
[![C#](https://img.shields.io/badge/C%23-512BD4?logo=dotnet&logoColor=white)](docs/environments.md#supported-toolchains)
[![F#](https://img.shields.io/badge/F%23-378BBA?logo=fsharp&logoColor=white)](docs/environments.md#supported-toolchains)
[![Elixir](https://img.shields.io/badge/Elixir-4B275F?logo=elixir&logoColor=white)](docs/environments.md#supported-toolchains)

**Write code locally. Compute remotely. Generated outputs sync locally.**

**Supports 17+ languages**

</div>

---

Write code in your macbook air, run the heavy compute on your Linux workstation seamlessly. The output files are automatically sent to your macbook air.

```bash
# instead of
python3 heavy_cuda.py

# run
recom python3 heavy_cuda.py
```

```bash
# instead of
g++ -O3 heavy_sim.cpp -o heavy_sim
./heavy_sim

# run
recom g++ -O3 heavy_sim.cpp -o heavy_sim
recom ./heavy_sim
```

No laggy remote editor, no setting up network filesystems, no daemon, no remote virtualenv to manage by hand.

## Table of Contents

- [Install](#install)
- [Quick Start](#quick-start)
- [Dependencies](#dependencies)
- [What a run does](#what-a-run-does)
- [Long Jobs](#long-jobs)
- [More Information](#more-information)
- [Development](#development)
- [License](#license)


## Install

> You only have to install `recom` on your local machine. No install is needed on the remote compute workstation.
>
> `recom` currently only supports MacOS and Linux for local, and Linux for compute backend.

### Requirements

- Local: `OpenSSH`, `rsync 3.x` (on macOS: `brew install rsync`)
- Backend: A Linux machine, `sshd`, `rsync`, `tmux`(optional), language runtimes (uv is recommended for Python)

### Install Script

- Install

```sh
curl -fsSL https://raw.githubusercontent.com/chaseungjoon/recom/main/install.sh | sh
```

The script above installs `recom` to `~/.local/bin`.

- Update

```sh
recom update
```

### Releases tab

Download the archive for your platform from
[Releases](https://github.com/chaseungjoon/recom/releases), verify it, and
put `recom` on your `PATH`:

```sh
curl -LO https://github.com/chaseungjoon/recom/releases/download/v0.2.1/recom_0.2.1_darwin_arm64.tar.gz
curl -LO https://github.com/chaseungjoon/recom/releases/download/v0.2.1/checksums.txt
shasum -a 256 --check --ignore-missing checksums.txt
tar -xzf recom_0.2.1_darwin_arm64.tar.gz
mkdir -p ~/.local/bin && install -m 0755 recom ~/.local/bin/
```

### Build from source

Requires Golang (see `go.mod`) and `make`:

```sh
git clone https://github.com/chaseungjoon/recom.git && cd recom
make build                      # bin/recom, with the Linux helpers embedded
mkdir -p ~/.local/bin && install -m 0755 bin/recom ~/.local/bin/
```

Use `make build`, not plain `go build`: the client embeds the cross-compiled
Linux helpers it installs on backends.

## Quick start

```sh
# 1) Setup remote compute backend
recom ssh <username>@<host>

# 2) Initialize recom
cd ~/Projects/my-model
recom init                     # creates .recom.toml (commit it) and sets up the backend environment

# 3) Run any script with 'recom' prefix
recom python3 src/run.py
recom nvidia-smi               # anything: gcc, make, pytest, nvcc, ...
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
`recom init` does the first one.

- `recom setup` runs it on demand
- `recom setup --clean` rebuilds it from scratch
- `recom --shell 'python3 -m ~'` runs build commands by hand

Details in [environments](docs/environments.md).

## What a run does

1. **Uploads**

   changed files with rsync. Caches, `.git/`, local virtualenvs,
   build directories and secrets (`.env`, keys, cloud credentials) stay home.
   Nothing is ever deleted remotely. If the upload fails, nothing runs.
2. **Sets up**

   the environment if a dependency file changed since the last
   setup on that backend (see [Dependencies](#dependencies)).

3. **Runs**

   the command on the backend with its exact arguments, the backend's
   environment and toolchain, live stdout/stderr, stdin, and its exit code.
   Ctrl-C stops the remote process.

4. **Downloads outputs**

   files the run created or changed, verified by
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

## More Information

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

[GPL-3.0](LICENSE)
