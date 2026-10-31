# Configuration

recom has three layers. More specific wins: **command-line flags > project
file > global config**. Nothing in any of them is a secret: authentication
is entirely OpenSSH's job.

| File | Scope | Commit it? |
|---|---|---|
| `~/.config/recom/config.toml` | Registered backends, default backend | no (machine-specific) |
| `<project>/.recom.toml` | Project id and sync/output/run policy | **yes** |
| `<project>/.recom/` | This working copy's identity, last-run state, staging, conflicts, backups | no (ignores itself) |

## Global config

Written by `recom ssh` and `recom default` (atomically, mode 0600). Respects
`XDG_CONFIG_HOME`; `RECOM_CONFIG` points elsewhere.

```toml
default_host = "aiworkstation"

[hosts.aiworkstation]
ssh_target = "aiworkstation"        # passed to ssh as-is
remote_home = "/home/me"            # discovered at registration
remote_root = "/home/me/.local/share/recom"
os = "linux"
arch = "amd64"
added = 2026-10-31T12:00:00Z
# port = 2222
# identity = "/Users/me/.ssh/id_work"
# ssh_options = ["ProxyJump=bastion"]
# multiplex = false                 # disable ControlMaster connection sharing
```

The remote root is resolved to an absolute path once, at registration, so
recom never relies on `~` expansion in quoted remote paths.

## Project file: `.recom.toml`

`recom init` writes this with comments. Every key is optional except `id`.

```toml
id = "8b0e0f1e-…"          # random UUID; never edit
# host = "aiworkstation"   # preferred backend for this project

[sync]
exclude = ["data/raw/", "*.ckpt"]   # also never upload these
include = [".env.ci"]               # upload even though a built-in rule excludes it
# default_excludes = false          # drop the built-in cache/VCS/venv excludes (secrets stay excluded)

[outputs]
exclude = ["tmp/", "*.log"]         # never bring these back automatically
include = []                        # if set, only these come back automatically; others are held
max_file_size = "100MB"             # larger outputs are held on the backend ("0" = unlimited)
max_run_size = "1GB"                # per-run automatic download budget ("0" = unlimited)

[run]
env_mode = "login"                  # or "ssh"
env = { CUDA_VISIBLE_DEVICES = "0", WANDB_MODE = "offline" }
path = [".venv/bin"]                # prepended to PATH; relative = inside the remote project
wrapper = []                        # argv prefix, e.g. ["conda", "run", "--no-capture-output", "-n", "ml"]

[jobs]
tmux = "auto"                       # "auto", "always" or "never"
```

Patterns use rsync syntax; see [sync.md](sync.md#upload-rules). Unknown keys
and malformed values are rejected with an error rather than ignored.

## Remote environment

Commands run with the **backend's** environment, never your laptop's.

- `env_mode = "login"` (default): recom captures the environment of
  `$SHELL -l` on the backend, i.e. what `~/.profile`, `~/.bash_profile` or
  `~/.zprofile` set up (typically `~/.local/bin`, CUDA paths, cargo…). If that
  fails, recom warns and falls back to the plain SSH environment.
- `env_mode = "ssh"`: the environment sshd provides to a non-interactive
  command.

Interactive-only setup (code after `case $- in *i*` guards in `~/.bashrc`,
like conda's init block) is not sourced in either mode. Use `path`, `env` or
`wrapper` instead of relying on it. `recom env` shows the effective `PATH`
and which interpreters and compilers it resolves.

`env` entries override the remote environment; an empty value removes a
variable. recom sets `PYTHONUNBUFFERED=1` unless you set it, so Python output
streams live through pipes.

### Python environments

Create the environment **on the backend**, then point `path` at it:

```sh
recom --shell 'uv venv .venv && uv pip install -r requirements.txt'   # or python3 -m venv .venv
```

```toml
[run]
path = [".venv/bin"]
```

`.venv/` is excluded from upload and from output detection, so your local
macOS virtualenv and the remote Linux one never mix.

For Conda, use the wrapper:

```toml
[run]
wrapper = ["conda", "run", "--no-capture-output", "-n", "ml"]
```

(`conda` itself must be on the backend `PATH`; if `conda init` only runs in
interactive shells, add its `condabin` directory to `path`.)

## Working-copy identity

The remote replica is named `<project id>-<copy id>`. The copy id lives in
`.recom/local.toml`:

- A **git clone** has no `.recom/` (it ignores itself), so it gets a new copy
  id and its own replica.
- A **moved or renamed** directory keeps its copy id and replica.
- A **copied** directory (`cp -r`, including `.recom/`) is detected because
  the original still exists with the same id; the copy gets a new id.

So two checkouts never share or fight over a replica, while renaming a
project never costs a full re-upload.

## Backend layout

```
~/.local/share/recom/                 (remote_root, mode 0700)
├── bin/recom-helper-<sha256 prefix>  content-addressed helper binaries
├── projects/<project>-<copy>/        replicas (your project files + outputs)
└── state/<project>-<copy>/           lease, run manifests, snapshots, job logs (0700)
```

Removing a project's replica and state directories is always safe once
nothing is pending (check `recom status`). recom never deletes them itself.
