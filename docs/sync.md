# Synchronization and outputs

recom keeps one persistent copy of your project (the **replica**) per
working copy and backend. Every run does the same five steps:

1. **Lock.** Take the replica's lease, so no other run or job can change files
   or claim outputs at the same time.
2. **Reconcile.** Download outputs left pending by earlier runs (interrupted
   downloads, disconnected clients, finished detached jobs) *before*
   uploading anything, so nothing generated remotely can be lost.
3. **Upload.** rsync the project to the replica. Only changed files are sent;
   if the upload fails, the command does not run.
4. **Run.** Snapshot the replica, run the command, snapshot again.
5. **Collect.** Download the files this run created or changed, even if it
   failed, without ever overwriting a local file you changed meanwhile.

## Upload rules

rsync runs with `-rlpt --safe-links` plus nanosecond timestamp comparison
(rsync ≥ 3.1.3), so:

- Only differences are transferred; unchanged trees cost one rsync scan.
- **Nothing is deleted on the backend.** Deleting a local file leaves the
  remote copy in place (a stale module may still be importable there; see
  [limitations](architecture.md#limitations)).
- Symlinks are copied as links; links pointing outside the project are
  dropped (`--safe-links`). Sockets, FIFOs and devices are skipped.
- Paths with remote outputs that have not been collected yet are left
  untouched, so an older local copy can never overwrite a newer remote result.

Filters are applied in this order (first match wins):

| # | Rule | Overridable |
|---|---|---|
| 1 | `.recom/` (local state) | no |
| 2 | Paths with uncollected remote outputs | collect, or `recom pull --dismiss` |
| 3 | `[sync] include` | — |
| 4 | Secrets: `.env`, `.env.*` (but not `.env.example`/`.sample`/`.template`/`.dist`), `.envrc`, `*.pem`, `*.key`, `*.p12`, `*.pfx`, `*.keystore`, `*.jks`, `id_rsa*`, `id_dsa*`, `id_ecdsa*`, `id_ed25519*`, `.ssh/`, `.gnupg/`, `.aws/`, `.azure/`, `.kube/`, `.docker/`, `.config/gcloud/`, `.netrc`, `.git-credentials`, `.npmrc`, `.pypirc`, `credentials.json`, `service-account*.json`, `*.tfstate*`, `.terraform/`, `.vault-token` | with `[sync] include` |
| 5 | Defaults: `.git/`, `.hg/`, `.svn/`, `.venv/`, `venv/`, `.conda/`, `__pycache__/`, `*.pyc`, `.mypy_cache/`, `.pytest_cache/`, `.ruff_cache/`, `.tox/`, `.nox/`, `.ipynb_checkpoints/`, `node_modules/`, `.idea/`, `.vscode/`, `.DS_Store`, `*.swp`, `._*`, `.cache/`, `wandb/`, `mlruns/`, `lightning_logs/`, `checkpoints/` | `default_excludes = false` |
| 6 | `[sync] exclude` | — |

Local virtualenvs are excluded on purpose: macOS or ARM wheels are useless on
a Linux GPU box. Create the environment on the backend instead (see
[configuration](configuration.md#python-environments)). `data/` and `bin/`
are **not** excluded by default, because they are often real inputs.

Patterns use rsync syntax: a trailing `/` matches directories only, a leading
`/` anchors at the project root, `*` stays within one path component, `**`
crosses components. To include a file inside an excluded directory, include
the directory too (rsync does not descend into excluded directories).

## What counts as an output

A **run output** is a regular file, or a symlink that stays inside the
project, that **did not exist before the run** or **whose contents changed
during the run**. The baseline is taken *after* the upload, so files you just
uploaded are never mistaken for outputs.

Change detection compares size, mtime, ctime and inode. Because ctime cannot be
set by programs, a file rewritten with its old mtime restored is still noticed.
When metadata changed but the SHA-256 is identical (a `touch`, or a rewrite with
the same bytes), the file is **not** an output. Files modified within two
seconds of the baseline are always content-compared, to defeat coarse
filesystem timestamps.

Hashing cost: before each run the helper hashes files up to 256 MiB that it
has not seen before (results are cached by size/mtime/ctime/inode, so an
unchanged tree is cheap). Larger files are compared by metadata only; if
they change they are reported as outputs, and since they exceed the default
size limit they are held, not downloaded. The snapshot walks the replica
twice per run but skips excluded directories (`.venv/`, `node_modules/`, …)
entirely.

Never outputs: deleted files (deletions are counted, never propagated), files
under `[outputs] exclude` or the default output excludes (`.git/`, `.hg/`,
`.svn/`, `.venv/`, `venv/`, `.conda/`, `__pycache__/`, `*.pyc`, caches,
`.ipynb_checkpoints/`, `node_modules/`, `.cache/`, `wandb/`, `*.swp`, `*.tmp`,
`.nfs*`), secret-looking files, sockets/FIFOs/devices, and symlinks pointing
outside the project (reported as skipped).

## Size limits and held outputs

| Setting | Default | |
|---|---|---|
| `max_file_size` | `100MB` | Larger files are held. |
| `max_run_size` | `1GB` | Once a run's downloads reach this, the remaining outputs are held. |
| `include` | `[]` | If set, only matching outputs download automatically; the rest are held. |

Held outputs are never discarded. They stay on the backend and show up in the
run summary and `recom status` with the exact reason and the command to fetch
them (`recom pull <path>` or `recom pull --all`), and they are protected from
being overwritten by uploads until you collect or dismiss them.

## Installing outputs locally

Each output is downloaded with rsync into `.recom/staging/<run>/` (same
filesystem as the project, partial files kept for resumption), checked against
the SHA-256 recorded on the backend, and moved into place with an atomic
rename through Go's `os.Root`, which refuses any path or symlink that would
escape the project. Then:

| Local destination | Result |
|---|---|
| does not exist | installed |
| identical content | nothing to do |
| exactly the version the run started from | replaced (the remote change wins) |
| anything else (you edited it during the run, or it appeared locally) | **conflict**: your file is kept, the remote version goes to `.recom/conflicts/<run>/<path>` |
| a directory or special file | conflict |

A file is marked collected on the backend only after it has been installed
and verified. A download interrupted halfway leaves no partial file in your
project; the output stays pending and is retried on the next run. Remote
outputs are never deleted after download.

After a conflict, your version is what the next run uploads. The remote
version stays available in `.recom/conflicts/`.

## Concurrency

- **Foreground runs** of the same replica are serialized: a second run prints
  "waiting for run … by user@host" and starts when the first finishes. Its
  outputs can therefore never be attributed to the wrong run.
- **Detached jobs** own the replica for their whole lifetime, so code is never
  synced underneath a running job and its outputs are attributed exactly.
  Ordinary runs fail fast with exit 125 and a hint; `--no-sync` commands may
  run, without output tracking.
- Two working copies (clones, copies, other laptops) always have different
  replicas, so they never interfere.
- The lease identifies its holder by PID and process start time. If a holder
  crashes (kill -9, reboot) the lease is considered stale, and the next run
  first reconstructs the lost run's outputs from its saved baseline snapshot.

## Disconnects

| Situation | What happens |
|---|---|
| Laptop sleeps or the network drops during a foreground run | The backend hangs up the command, records its outputs as pending, releases the lock. Your next `recom` command collects them. |
| Laptop offline while a detached job finishes | The supervisor records exit status and outputs by itself; collected on the next connection. |
| Download interrupted | Nothing partial is installed; the output stays pending and the staged partial file is resumed. |
| Backend reboots during a job | The job shows as `lost`; outputs written so far are reconstructed and collected. |
