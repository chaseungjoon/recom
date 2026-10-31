# Architecture

recom is a single Go client plus a small Go helper that runs on the backend
only while a recom command is connected (or while it supervises a detached
job). There is no daemon, no relay, no root access, and nothing of GitHub at
runtime.

```
 laptop                                           backend (Linux)
 ┌──────────────────────────┐   OpenSSH (your    ┌──────────────────────────────┐
 │ recom                    │   config, agent,   │ recom-helper __agent serve   │
 │  ├ config + identity     │   known_hosts)     │  ├ lease (flock + lease.json)│
 │  ├ rsync  ───────────────┼──── upload ───────►│  ├ snapshots + hash cache    │
 │  ├ framed protocol ◄─────┼── one session ────►│  ├ exec / PTY / signals      │
 │  ├ collector (os.Root) ◄─┼──── download ──────┤  ├ manifests (runs/*.json)   │
 │  └ terminal + signals    │                    │  └ jobs → supervisor (tmux)  │
 └──────────────────────────┘                    └──────────────────────────────┘
```

## Packages

| Package | Responsibility |
|---|---|
| `internal/cli` | cobra commands, run orchestration, terminal and signal handling, messages |
| `internal/config` | global TOML config, `.recom.toml`, working-copy identity, local state and lock |
| `internal/sshx` | builds OpenSSH invocations (ControlMaster multiplexing, keep-alives, quoting) |
| `internal/rsync` | rsync capability detection, upload with filters, `--files-from` downloads |
| `internal/remote` | dials the helper, installs/verifies it, request/response and exec streaming |
| `internal/protocol` | versioned frame format and message types shared by both ends |
| `internal/agent` | the helper: lease, snapshots, diff, manifests, exec, jobs, supervisor, probes |
| `internal/collect` | staged, verified, conflict-aware installation of outputs into the project |
| `internal/pathmatch`, `internal/policy` | rsync-style patterns and the built-in exclude/secret lists |
| `internal/helperbin` | embedded, gzip-compressed Linux helpers (amd64, arm64) |

## The helper

Release builds embed `recom-helper` for linux/amd64 and linux/arm64 (built
first by `make helpers` / GoReleaser), so a macOS client never ships a macOS
binary to Ubuntu. On connect, the client starts
`<root>/bin/recom-helper-<sha256[:16]> __agent serve` over SSH. If it is missing
or damaged, the client uploads it (`umask 077`, temp file + `mv`, inside the
private root only) and retries. The helper hashes its own executable and
reports it in the handshake; the client compares it with the SHA-256 of the
binary it embeds and reinstalls on mismatch. Because the file name is
content-addressed, client and helper versions always match exactly, and
upgrading recom simply installs a new helper next to the old one.
Development builds without embedded helpers can drive backends of their own
platform by uploading themselves (the client also understands `__agent`).

## Protocol

The helper prints a magic preamble, then speaks length-prefixed frames
(`type:1 | length:4 | payload`, max 8 MiB). Anything before the preamble,
such as text printed by a chatty `.bashrc`, is discarded and reported by
`recom doctor`. Control frames are JSON (`Hello`, `Request`, `Response`,
`Event`, `Signal`, `Resize`), while program I/O travels in separate data frames
(`stdout`, `stderr`, `stdin`, `stdin-eof`). The protocol never parses program
output as control data. Malformed frames, unknown versions and corrupt
manifests are rejected (fail closed).

A run is one SSH session (`open` → upload → `exec` → collect → `mark` →
`close`). rsync transfers piggyback on the same OpenSSH ControlMaster
connection, so a warm run costs a few round trips.

argv is sent as a JSON array and executed with `execve` after a PATH lookup
in the *remote* environment. No shell is involved unless `--shell` is used,
and the only shell strings recom ever builds contain its own validated,
single-quoted paths.

## Snapshots and attribution

Before the command starts (after the upload), the helper walks the replica
(skipping excluded directories), records type, size, mtime, ctime, inode and
SHA-256 (for files ≤ 256 MiB, cached across runs), and persists this baseline
to `runs/<id>.pre`. After the command, it walks again and diffs. ctime makes
"modified but mtime restored" detectable; the hash makes "touched only"
cheap to dismiss. See [sync.md](sync.md#what-counts-as-an-output) for the
exact rule.

The manifest (`runs/<id>.json`, 0600) records the command (with obvious
secret arguments redacted), times, exit status and each output's state
(`pending`, `held`, `collected`, `conflict`, `dismissed`, `superseded`,
`skipped`). A later run producing the same path supersedes older pending
records. Old fully-resolved manifests beyond the newest 100 are pruned.

## Locking

`lease.json` names the holder (run or job, PID, process start time from
`/proc/<pid>/stat`). It is read and written under a short `flock`. Liveness
is checked by PID *and* start time, so PID reuse cannot keep a dead lease
alive, and a crashed holder does not block anyone. On taking over a stale
lease, the helper first finalizes any run or job still marked running, using
its saved baseline (`lost`). Locally, a per-working-copy `flock` serializes
output installation between concurrent recom processes.

## Detached jobs

The launching helper takes the baseline, writes `jobs/<id>/job.json` and a
`spec.json` (argv and environment, mode 0600, deleted as soon as it is read),
hands the lease to the job, and starts `recom-helper __agent supervise`:

- with tmux: `tmux -L recom new-session -d …` (a private tmux server socket).
  The supervisor gives the command its own PTY and copies its output both to
  the tmux pane and to `output.log`. If the tmux session is killed, the job
  keeps running and logging.
- without tmux: a `setsid` process with stdout/stderr redirected to
  `stdout.log` / `stderr.log`.

The command runs in its own process group. When it exits, the supervisor
records the exit status, diffs against the baseline, writes the manifest and
releases the lease, all without a client. `stop` signals the recorded process
group only after verifying PID + start time.

## Security

- Remote-only execution: the client has no code path that executes the user
  command locally.
- OpenSSH does all authentication and host-key verification; recom refuses
  `StrictHostKeyChecking=no` in its own options and stores no credentials.
- Path safety: the helper refuses absolute paths, `..`, NUL bytes and
  symlinked path components in client-supplied paths. The client validates
  every remote-supplied path again and installs through `os.Root` (no escape
  through symlinks, even if the tree changes concurrently). Upload uses
  `--safe-links`; the helper never lists files beneath symlinked
  directories, and every downloaded file must match the SHA-256 recorded at
  snapshot time before it is installed.
- Secrets: common credential files are excluded from upload and download by
  default; job specs holding argv/env are deleted after use; all state files
  are 0600 in 0700 directories.
- Residual risk: anything you explicitly include, credentials embedded in
  regular source files, and whatever your programs print or write.

## Limitations

- Backends must be Linux (amd64 or arm64). Clients: macOS and Linux.
- Deleting or renaming a local file does not delete the remote copy (no
  `--delete` in v0.1). Old modules can linger in the replica; clean up with
  `recom -- rm …` or delete the replica directory.
- A detached job owns its replica: ordinary runs of the same project on that
  backend wait for it (use `--no-sync`, another backend, or `stop`).
- Foreground and job output detection walks the replica twice per run; very
  large trees (millions of files) add noticeable latency unless excluded.
- Files above 256 MiB are compared by metadata, not content.
- Processes that daemonize out of the command's process group (`setsid`,
  double fork) are not stopped and may write files after collection. Those
  writes are attributed to whichever run next sees them change.
- `--tty` merges stdout and stderr, like any terminal.
- tmux reads your `~/.tmux.conf` on the backend; recom reports your prefix key.

## Roadmap

Ideas, not commitments: tracked deletion propagation via a source manifest;
per-job private workspaces so several jobs of one project can run at once;
`recom clean` for replicas; configurable remote data mounts; a progress bar
for large transfers.
