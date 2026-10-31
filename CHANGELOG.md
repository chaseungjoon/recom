# Changelog

All notable changes are documented here. The project follows
[Semantic Versioning](https://semver.org/).

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

[0.1.0]: https://github.com/chaseungjoon/recom/releases/tag/v0.1.0
