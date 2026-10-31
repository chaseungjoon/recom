# recom

[![CI](https://github.com/chaseungjoon/recom/actions/workflows/ci.yml/badge.svg)](https://github.com/chaseungjoon/recom/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/chaseungjoon/recom?include_prereleases)](https://github.com/chaseungjoon/recom/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**Write locally. Compute remotely. Bring generated files home.**

recom runs a command on your GPU workstation (or any Linux box you can `ssh`
into) as if it were local. Edit in your own editor on your laptop, type
`recom python3 train.py`, and recom uploads what changed, runs it **only** on
the remote machine, streams the output, and copies back the files that run
produced. No remote editor, no network filesystem, no daemon.

```console
$ recom python3 src/run.py
recom: synced 1 changed file(s) (343 B) to aiworkstation
running on AIWorkstation x86_64
recom: ← outputs/result.json (96 B)
```

## Install

```sh
brew tap chaseungjoon/recom
brew trust chaseungjoon/recom   # current Homebrew requires this once for third-party taps
brew install recom
```

Or download an archive for macOS or Linux (amd64/arm64) from
[Releases](https://github.com/chaseungjoon/recom/releases), check it against
`checksums.txt`, and put `recom` on your `PATH`.

You need OpenSSH and rsync 3.x locally (Homebrew installs rsync on macOS) and,
on the backend, a Linux machine with `sshd`, rsync, and optionally tmux. No
root and no extra software: recom installs its own small helper under
`~/.local/share/recom` on the backend.

## Quick start

```sh
recom ssh aiworkstation        # any ~/.ssh/config alias or user@host
cd ~/Projects/my-model
recom init                     # creates .recom.toml (commit it); uploads nothing yet
recom python3 src/run.py       # first run uploads the project, later runs only the diff
recom nvidia-smi               # anything: gcc, make, pytest, nvcc, ./bin/main …
```

Run from a subdirectory and the command runs in the same subdirectory
remotely. Outputs land at the matching local paths.

## What a run does

1. **Uploads** changed files with rsync. Caches, `.git/`, local virtualenvs and
   secrets (`.env`, keys, cloud credentials) stay home. Nothing is ever deleted
   remotely. If the upload fails, nothing runs.
2. **Runs** the command on the backend with its exact arguments, the backend's
   environment and toolchain, live stdout/stderr, stdin, and its exit code.
   Ctrl-C stops the remote process.
3. **Downloads outputs**: files the run created or changed, verified by
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
| `recom --shell 'make \| tee log.txt'` | remote pipes and redirection (`recom cmd > f` writes `f` locally) |
| `recom --no-sync nvidia-smi` | skip the upload |
| `recom status` · `recom env` · `recom doctor` | pending outputs, remote toolchain/GPU, diagnostics |

- [CLI reference](docs/cli.md): every command, flag and exit code
- [Sync and outputs](docs/sync.md): exactly what is uploaded, what counts as an output, conflicts
- [Configuration](docs/configuration.md): `.recom.toml`, remote virtualenvs and Conda
- [Architecture, security, limitations](docs/architecture.md)
- [Troubleshooting](docs/troubleshooting.md) · [Examples](examples/)

**Limitations (v0.1):** backends must be Linux. Local deletions are not
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
