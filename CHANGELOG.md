# Changelog

All notable changes are documented here. The project follows
[Semantic Versioning](https://semver.org/).

## [0.2.0] - 2026-11-01

recom now sets up your project's environment on the backend by itself:
declare dependencies as usual, `recom init`, `recom python3 main.py`.

### Added

- Automatic environment setup. recom detects toolchains from the dependency
  files at the project root and, before a run, installs them in the remote
  replica whenever those files changed (fingerprinted, so unchanged projects
  pay nothing): Python (`uv sync`, `poetry install`, `pipenv install`, or a
  `.venv` + `pip install -r requirements.txt` / `-e .`, preferring uv),
  Conda (`environment.yml` → `.conda`), Node.js (npm, pnpm, yarn, bun),
  Rust, Go, Maven, Gradle, Ruby (Bundler), PHP (Composer), Julia, R (renv),
  .NET and Elixir. Runs get the environment on `PATH` (`.venv/bin`,
  `node_modules/.bin`, …) plus `VIRTUAL_ENV` / `CONDA_PREFIX` / … . See
  [docs/environments.md](docs/environments.md).
- `[setup]` in `.recom.toml`: `toolchains` (`"auto"`, an explicit list, or
  `[]` to turn it off), plus an optional `command` re-run when its `watch`
  files change.
- `recom init` records the detected toolchains and their `PATH` entries,
  then uploads the project and sets up the backend right away
  (`--no-setup` to skip).
- `recom setup [--force] [--clean]` to run the setup on demand or rebuild
  environments from scratch; `--no-setup` skips it for one run;
  `recom status` shows whether each environment is ready.
- Tools installed by version managers that only hook into interactive shells
  (nvm, volta, fnm, asdf, mise, rbenv, rustup, juliaup, Go) are found
  automatically for the detected toolchains.
- Build directories of detected toolchains (`target/`, `build/`, `vendor/`,
  …) are neither uploaded nor downloaded as outputs.
- `recom update`: self-update to the latest release, checksum-verified and
  atomic (`--check`, `--version`).
- `install.sh`: one-line installer (`curl -fsSL …/install.sh | sh`) that
  picks the right release archive, verifies its SHA-256 checksum and installs
  `recom`; now the default install method in the README.
- `recom env` / `doctor` also report node, cargo and go.

### Changed

- Setup output goes to stderr, so `recom cmd > file` still captures only the
  command's own output. A failed setup stops the run with exit 125.
- `--shell` stays available as the manual fallback for environments recom
  does not set up.

### Removed

- Homebrew tap. Uninstall with `brew uninstall recom` and use `install.sh`;
  update later with `recom update`.

## [0.1.0] - 2026-10-31

First public release.

### Added

- `recom <command>`: automatic incremental upload (rsync), remote-only
  execution with exact argv, live stdout/stderr, stdin, exit codes, and
  automatic download of the files the run created or changed.
- Output detection from pre/post-run snapshots (size, mtime, ctime, inode and
  SHA-256), so uploaded inputs, unchanged artifacts and touch-only files are
  never mistaken for outputs.
- Safe installation: staging + checksum verification + atomic rename,
  conflict copies in `.recom/conflicts/`, no deletions in either direction,
  size limits with "held" outputs and `recom pull`.
- Deferred collection: outputs of interrupted runs, disconnected clients and
  detached jobs are collected by the next invocation.
- Detached jobs (`--detach`, `jobs`, `logs [-f]`, `attach`, `stop`) supervised
  on the backend, tmux-backed when available, with durable logs and status.
- `--tty` interactive mode with resize and Ctrl-C forwarding; `--shell` for
  remote shell expressions; `--no-sync` and `--no-collect`.
- `recom ssh`, `hosts`, `default`, `init`, `status`, `env`, `doctor`,
  `version`, shell completion.
- Per-replica lease that serializes runs and makes concurrent use safe;
  per-working-copy identity so clones and copies never share a replica.
- Embedded, content-addressed Linux helpers (amd64, arm64), installed and
  verified automatically over SSH; no daemon, no root.
- Release builds for macOS and Linux (amd64, arm64), SHA-256 checksums,
  Homebrew tap.

[0.2.0]: https://github.com/chaseungjoon/recom/releases/tag/v0.2.0
[0.1.0]: https://github.com/chaseungjoon/recom/releases/tag/v0.1.0
