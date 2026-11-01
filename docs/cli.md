# CLI reference

```
recom [flags] <command> [args...]     run <command> on the backend
recom <subcommand> [flags]            manage backends, projects and jobs
```

Anything after `recom` that is not a subcommand is the **remote command**.
Its arguments are passed through byte-for-byte (spaces, quotes, unicode,
empty strings, leading dashes) and are never interpreted by a shell.
recom's own flags must come **before** the command:

```sh
recom -v python3 train.py --lr 3e-4   # -v is recom's; --lr goes to train.py
recom python3 -v train.py             # -v goes to python3
recom -- status                       # run a remote program named "status"
```

## Running commands

| Flag | Meaning |
|---|---|
| `--host, -H <name>` | Backend for this invocation (overrides `RECOM_HOST`, the project's `host`, and the default). Each backend keeps its own replica. |
| `--no-sync` | Skip the pre-run upload (e.g. `recom --no-sync nvidia-smi`). Outputs are still collected if the replica is idle. Before the first sync, or outside any project, the command runs in the remote home directory and nothing is collected. |
| `--no-setup` | Skip the automatic [environment setup](environments.md) for this run, even if dependency files changed. |
| `--no-collect` | Run normally but leave outputs on the backend (they stay pending for the next run or `recom pull`). |
| `--tty, -t` | Run inside a remote pseudo-terminal: prompts, REPLs, curses programs, colors, Ctrl-C/Ctrl-Z as keys, window resizing. stdout and stderr are merged, as on any terminal. |
| `--detach, -d` | Start a persistent job, print its id, and return. See [jobs](#jobs). |
| `--shell '<expr>'` | Run a shell expression remotely with `$SHELL -c` (pipes, redirection, globbing, `&&`, env expansion). Takes no further arguments. Also the manual fallback for environments recom does not set up itself. |
| `--verbose, -v` | Show ssh/rsync commands, uploaded file names and timings. |
| `--quiet, -q` | Only print warnings and errors from recom itself. |

Redirection is performed by whichever shell sees it:

```sh
recom python3 run.py > log.txt            # log.txt is written on this machine
recom --shell 'python3 run.py > log.txt'  # written on the backend, then collected as an output
```

**Default mode is pipes**, not a terminal: stdout and stderr stay separate
and are streamed as data arrives (recom sets `PYTHONUNBUFFERED=1` unless you
set it yourself). stdin is forwarded; when stdin is a terminal it is only read
while recom is in the foreground.

**Ctrl-C** (pipe mode): the first press sends SIGINT to the remote process
group, the second SIGKILL, the third disconnects immediately. SIGTERM, SIGHUP
and SIGQUIT received by recom are forwarded. If the connection drops, the
backend hangs up the command (SIGHUP and SIGTERM, then SIGKILL after 5 s)
and keeps its outputs pending. When the command exits, anything it left in
its process group receives SIGHUP, like closing a terminal; use `nohup`,
`setsid` or `--detach` for work that must outlive the run.

## Exit status

| Code | Meaning |
|---|---|
| 0–255 | The remote command's own exit status (128+N if killed by signal N). |
| 126 / 127 | Remote command not executable / not found on the backend. |
| 2 | Usage error (unknown flag, bad arguments). |
| 75 | The command succeeded, but some outputs could not be collected; they stay pending. |
| 125 | recom failed before the command started (config, upload, environment setup, busy replica, helper). |
| 130 | Interrupted with Ctrl-C. |
| 255 | SSH connection failed or was lost. |

## Setup

### `recom ssh <alias | user@host>`

Register a backend. The target is passed to your `ssh` unchanged, so
`~/.ssh/config` aliases, agents, ProxyJump and Tailscale work. recom never
edits your SSH configuration and never disables host-key checking.

| Flag | |
|---|---|
| `--name` | Backend name (default: the alias, or the host part of `user@host`). |
| `--port, -p`, `--identity, -i` | Passed to ssh as `-p` / `-i`. |
| `--option, -o Key=Value` | Extra ssh option (repeatable), e.g. `-o ProxyJump=bastion`. |
| `--remote-root <abs path>` | Where replicas and state live (default `~/.local/share/recom`). |

Registration connects once, records the remote home, OS and architecture,
installs the helper, and checks rsync/tmux. Re-running it updates the entry.
The first backend becomes the default.

### `recom hosts` · `recom hosts remove <name>` · `recom default <name>`

List backends (`--json` available), forget one (remote files are left alone),
or choose the default.

### `recom init`

Create `.recom.toml` (project id + documented settings; commit it) and
`.recom/` (local state; ignores itself) in the current directory. Refuses to
reinitialize, to nest inside another project, and to run in your home
directory or `/`. `recom --host <name> init` records a preferred backend in
the project file.

init detects the project's toolchains from its dependency files
(`requirements.txt`, `pyproject.toml`, `environment.yml`, `package.json`,
`Cargo.toml`, `go.mod`, `Gemfile`, ...; see [environments](environments.md))
and records them under `[setup]`, with their directories (e.g. `.venv/bin`)
in `[run] path`. If there is something to install and a backend is
registered, init then uploads the project and sets up the environment on the
backend right away. `--no-setup` only writes the configuration; the first run
then does the setup. A project without dependency files uploads nothing at
init.

### `recom setup [--force] [--clean]`

Upload the project and run its environment setup now: the step every run
performs automatically, before the command, when the dependency files changed
since the last setup on that backend. Only steps whose files changed run;
`--force` runs all of them, and `--clean` first deletes the environments
(`.venv`, `.conda`, `node_modules`, ...) in the replica and rebuilds them.
Setup output goes to stderr.

### `recom update [--check] [--version vX.Y.Z] [--force]`

Replace this `recom` binary with the latest GitHub release (or the given
one; downgrades are allowed with `--version`). The archive is verified
against the release's `checksums.txt`, the new binary must run and report
the expected version, and the swap is atomic. `--check` only reports whether
a newer release exists. The installer puts `recom` in `~/.local/bin`, which you own, so no
sudo is needed. If you installed into a directory you cannot write to (e.g.
`/usr/local/bin`), run `sudo recom update`, or reinstall into `~/.local/bin`
and remove the old copy. Backends receive the matching
helper automatically on the next connection.

## Jobs

### `recom --detach <command>`

Uploads, takes the baseline snapshot, starts the command under a supervisor
on the backend (inside a tmux session when tmux is installed), prints the job
id to stdout and exits. The job survives SSH disconnects and your laptop
sleeping. When it ends, the supervisor records the exit status and the outputs
itself. The next `recom` command in the project downloads them.

While a job runs it **owns the project's replica**: ordinary runs fail fast
with a "busy" message (exit 125) instead of syncing code underneath it.
`recom --no-sync <cmd>` still works (without output tracking), as do `jobs`,
`logs`, `attach`, `stop`, `pull` and `status`.

### `recom jobs`

List this project's jobs on the backend: state (`running`, `exited (N)`,
`stopped`, `lost`, `failed`), age, duration, pending outputs. `--json` available.
A job is `lost` if its supervisor vanished (e.g. reboot); its outputs are
reconstructed from the saved snapshot on the next run.

### `recom logs <job> [-f] [-n N]`

Print the job's durable log; `-f` follows until the job ends. Plain (non-tmux)
jobs keep stdout and stderr apart; tmux jobs have one terminal log. Any unique
prefix of a job id works.

### `recom attach <job>`

Attach to a running tmux job. recom prints the backend's actual tmux prefix
(it honours your `~/.tmux.conf`); detach with prefix + `d` and the job keeps
running. Ctrl-C goes to the job. Jobs without tmux cannot take input, so attach
follows their log instead; for a finished job, attach prints the log.

### `recom stop <job> [--grace 10s]`

Send SIGTERM to the job's recorded process group (after verifying the process
identity, so a reused PID is never hit), then SIGKILL after the grace period.
Outputs written before the stop are recorded as usual.

## Files

### `recom pull [path ...]`

| Form | |
|---|---|
| `recom pull` | Download pending outputs (what runs collect automatically). |
| `recom pull --all` | Also download *held* outputs (over the size limits or not matched by `[outputs] include`). |
| `recom pull <path>...` | Fetch these files or directories from the replica, regardless of size limits. Paths are relative to your current directory. |
| `--force` | Overwrite differing local files; the previous local versions go to `.recom/backups/`. |
| `--dismiss <path>...` | Stop tracking pending outputs without downloading them (remote files stay). |

Without `--force`, a local file is replaced only if it is exactly what the run
started from; otherwise the remote version goes to `.recom/conflicts/<run>/`.

## Inspection

| Command | |
|---|---|
| `recom status` | Project id, working copy, backend, replica path, connectivity, last sync and run, environment setup state, running jobs, pending and held outputs. `--json` available. |
| `recom env` | The backend as your commands see it: OS, kernel, architecture, shell, effective `PATH` (after `[run]` settings and toolchain directories), Python/Node/Rust/Go/compilers/CUDA tools, GPUs, free space. `--json` available. Use `recom -- env` to run the `env` program. |
| `recom doctor` | Checks local ssh/rsync/config/project, connectivity, noisy shell startup files, helper install and integrity, backend rsync/tmux/storage/GPU, and the replica lease. Exits non-zero on failures. |
| `recom version` | Version, commit, build date, platform, protocol version and embedded helpers. `--json` available. `recom --version` prints the short form. |
| `recom completion <bash\|zsh\|fish>` | Shell completion script. |

## Environment variables

| Variable | |
|---|---|
| `RECOM_HOST` | Backend to use when `--host` is not given (beats the project's `host` and the default). |
| `RECOM_CONFIG` | Path of the global config file. |
| `XDG_CONFIG_HOME`, `XDG_CACHE_HOME` | Base directories for `recom/config.toml` and SSH control sockets. |
| `RECOM_SSH` / `RECOM_SSH_CONFIG` | ssh binary / an ssh config file passed as `-F`. |
| `RECOM_NO_MULTIPLEX=1` | Disable OpenSSH connection sharing (ControlMaster). |
| `RECOM_RSYNC` | rsync binary to use. |
| `RECOM_HELPER_PATH` | Use this Linux helper binary instead of the embedded one (development). |
| `RECOM_UPDATE_URL` | Release base URL for `recom update` (default `https://github.com/chaseungjoon/recom/releases`). |
| `NO_COLOR` | Disable colored recom messages. |

Remote commands additionally see `RECOM=1`, `RECOM_RUN_ID`,
`RECOM_PROJECT_ROOT` (the replica path) and, for jobs, `RECOM_JOB_ID`.
