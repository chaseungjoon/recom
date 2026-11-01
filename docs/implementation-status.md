# Implementation status (v0.2.0)

Status of each requirement in the original specification. **Tested** means
covered by automated tests (`internal/e2e` runs recom end to end against a real
sshd); **verified live** means also exercised on real machines: a Raspberry Pi 5
client (Ubuntu 24.04, arm64) driving an x86-64 Ubuntu 24.04 workstation with an
RTX 5070 over Tailscale.

## Commands

| Requirement | Status |
|---|---|
| `recom ssh`, `hosts`, `default` (no SSH config changes, `--port`, `--identity`, `-o`) | implemented, tested, verified live |
| `recom init` (UUID, refuses reinit / nesting / `$HOME`; detects toolchains and sets up the backend) | implemented, tested (`TestInitSetsUpPythonVenvAndRunsUseIt`) |
| Automatic environment setup (`[setup]`, `recom setup [--force] [--clean]`, `--no-setup`) | implemented; Python (pip and uv), Node (npm via nvm, pnpm), Rust and Go verified against a local backend; the custom command, watch files, failure and `--no-setup` tested end to end; detection and scripts of every toolchain unit-tested (scripts checked with `sh -n`) |
| `recom update` | implemented; download, checksum, version check and atomic replace unit-tested against a fake release server |
| `recom <cmd>`: auto upload, remote-only exec, mapped cwd, streaming, outputs | implemented, tested, verified live |
| `--host`, `--no-sync`, `--tty`, `--detach`, `--shell`, `--` | implemented, tested, verified live |
| `jobs`, `logs [-f]`, `attach`, `stop` | implemented, tested, verified live (attach tested via a PTY on the client) |
| `pull` (explicit paths, `--force`, `--all`, `--dismiss`) | implemented; paths and held outputs tested |
| `status`, `env`, `doctor`, `version` (+ `--json`) | implemented, tested, verified live |
| Shell completion | implemented (cobra `completion`) |
| `recom config`, `recom clean`, `recom export` | deferred (optional "later" commands) |

## Invariants

| Invariant | How it is enforced / tested |
|---|---|
| Remote-only execution, no local fallback | no local exec path; `TestCommandNotFoundAndNoLocalFallback` (a local-only tool is never run); live: aarch64 client, x86-64 results |
| Automatic pre-run sync; failed upload blocks execution | `TestIncrementalUploadSendsOnlyChanges`, `TestFailedUploadPreventsExecution` |
| Persistent replica per (project, working copy, backend), stable across renames | `TestProjectIdentity` |
| Output-aware reverse sync, no blind overwrite | `TestOutputRules`, `TestUploadedInputIsNotAnOutput`, `TestLocalEditDuringRunIsNeverOverwritten`, `collect` unit tests |
| Outputs survive disconnects / crashes | `TestClientDisconnectKeepsOutputs`, `TestInterruptedDownloadStaysPendingAndResumes`, `TestJobSurvivesClientAndLostSupervisorIsRecovered`; live: killed client on the Pi |
| Safety: no deletions, secrets excluded, no symlink escapes | `TestNoDeletionPropagation`, `TestSecretsAndDefaultExcludesStayLocal`, `TestSymlinkSafety`, `TestUnusualFilenames`, `SafeRel`/`os.Root` unit tests |
| Terminal correctness (stdout/stderr, stdin, exit codes, signals, PTY, resize) | `TestArgvIsPreservedExactly`, `TestStdinIsForwarded`, `TestOutputStreamsBeforeExit`, `TestInterruptTerminatesRemoteProcess`, `TestTTYMode`, `TestFailedCommandStillCollectsOutputs`; live: 12 interactive checks |
| Isolation and concurrency | `TestConcurrentRunsAreSerialized`, `TestDetachedJobOwnsReplicaAndCanBeStopped`, lease unit tests |
| Reproducible, checksummed releases | GoReleaser (`-trimpath`, commit timestamps, `gzip -n` helpers), SHA-256 `checksums.txt`, release workflow verifies its own assets |

## Failure scenarios (§11 operational quality)

| Scenario | Test |
|---|---|
| SSH disconnect mid-run | `TestClientDisconnectKeepsOutputs` (client and ssh killed with SIGKILL) |
| Backend offline | `TestBackendOffline` (exit 255) |
| Broken rsync / interrupted transfer | `TestFailedUploadPreventsExecution`, `TestInterruptedDownloadStaysPendingAndResumes` (corrupted + failed download) |
| Malformed config | `TestMalformedConfig`, config unit tests |
| Corrupt manifest | `TestCorruptManifestFailsClosed`, manifest unit tests |
| Old / damaged / tampered helper | `TestHelperIsReinstalledAndVerified` (content-addressed, SHA-256 verified, reinstalled) |
| Permission denied / full disk | surfaced as rsync/helper errors with exit 125; `doctor` reports writability and free space. No dedicated automated test (needs a quota-limited filesystem) |
| Lost supervisor (reboot, kill -9) | `TestJobSurvivesClientAndLostSupervisorIsRecovered` |
| Killed tmux session | `TestKilledTmuxSessionKeepsJobAndLog` |

## Platforms

| Platform | Status |
|---|---|
| Client linux/arm64 → backend linux/amd64 | verified live (Pi 5 → workstation, Tailscale) |
| Client linux/amd64 → backend linux/amd64 | integration tests (local sshd) |
| Client macOS arm64/amd64 | built and unit-tested in CI on macOS; **not** run against a live backend from a Mac during this release |
| Backend linux/arm64 | helper built and embedded; not exercised live |

## Release v0.1.0

- Built, tested and published by the tag workflow, which re-downloads its own
  assets, checks `checksums.txt` and runs the linux/amd64 binary.
- The published linux/arm64 archive was downloaded on the Pi, checksum-verified,
  and passed the full demo (17 steps) and the 12 interactive checks.
- Homebrew distribution was dropped in v0.2.0; `install.sh` and
  `recom update` replace it.

## GPU

`recom nvidia-smi` and a CUDA PyTorch matmul (torch 2.14.1+cu130, installed by
a detached job into a backend-side `.venv`) ran on the RTX 5070 from the Pi
client, and the results were collected locally. `nvcc` was not installed on
the backend, so the CUDA C example was not compiled.

## Not yet verified (v0.2.0)

- Setups for conda, Maven, Gradle, Ruby, PHP, Julia, R, .NET and Elixir were
  checked for detection and valid shell, but not run against a backend with
  those toolchains installed.
- `recom update` was not run against the real GitHub release before v0.2.0
  was published (no newer release existed yet).

## Deferred

- Deletion propagation via a tracked source manifest (recom never deletes remotely).
- Private per-job workspaces (a job owns its replica).
- Remote data mounts outside the project.
