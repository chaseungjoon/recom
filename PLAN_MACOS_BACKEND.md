# Plan: macOS compute backends

Status: proposal, no code written yet. Branch: `feat/macos-backend-support`.

## Verdict

Not hard. The helper is already plain POSIX Go: `fork`/`exec`, process
groups, `setsid`, `flock`, PTYs through `creack/pty`, `Statfs`, and
`Stat_t.Ctimespec` all exist on Darwin. `GOOS=darwin go vet ./...` already
passes in CI, `internal/agent` already has a `sys_other.go` (`!linux`) with
Darwin field names, and the macOS CI job already runs the agent unit tests on
Darwin. On the client, `remote.Probe` already maps `uname -s` = `Darwin` to
`darwin`.

The work that turns a build that compiles into a backend that works:

1. Remove the Linux-only gates (registration check, `helperbin`, the ELF-only
   binary check) and embed Darwin helpers.
2. Replace the two `/proc`/`/etc` reads with Darwin equivalents.
3. Handle what a stock Mac does differently over SSH. The ssh `PATH` finds
   macOS's bundled openrsync before Homebrew's rsync, `/usr/bin` shims pop up
   the Xcode installer when the Command Line Tools are missing, and the
   machine idle-sleeps in the middle of a job.

Estimate: about 600–900 lines of Go including tests, plus docs and CI. The
work splits into 4 PRs (phases below). Phases 1 and 2 are enough for a
usable backend.

Measured while writing this plan:

- Compressed helper sizes: darwin/arm64 1.55 MB, darwin/amd64 1.68 MB
  (linux/amd64 is 1.70 MB). Embedding both Darwin helpers adds about 3.2 MB to
  every client.
- A darwin/arm64 helper cross-compiled on Linux already carries an ad-hoc
  `LC_CODE_SIGNATURE` (Go's internal linker adds it), so Apple Silicon will
  run it without `codesign`. darwin/amd64 has no signature and doesn't need
  one.

## Inventory of Linux assumptions

| # | Where | What is Linux-specific | Fix (phase) |
|---|---|---|---|
| 1 | `internal/cli/hosts.go:86` | `recom ssh` refuses `info.OS != "linux"` | allow `darwin` (P1) |
| 2 | `internal/helperbin/helperbin.go:45,81,125` | platform list, `goos != "linux"` refusal, `checkELF` | add darwin platforms, Mach-O check (P1) |
| 3 | `Makefile` `helpers`, `.goreleaser.yaml` hook | builds `linux/{amd64,arm64}` only | loop over `linux darwin` × `amd64 arm64` (P1) |
| 4 | `internal/agent/sys_linux.go` `procStart` | `/proc/<pid>/stat` start time; `sys_other.go` falls back to `kill(pid,0)` with start = 1, so a reused PID could keep a stale lease alive | `sys_darwin.go` with `sysctl kern.proc.pid` (P1) |
| 5 | `sys_other.go` `kernelRelease` | returns `""` | `unix.Uname` / `kern.osrelease` (P1) |
| 6 | `internal/agent/probe.go` `osPretty` | `/etc/os-release` | `kern.osproductversion` → `macOS 15.2` (P1) |
| 7 | rsync server side (`internal/rsync`, `internal/sshx`) | assumes the `rsync` on the remote non-login `PATH` is rsync 3.x; on macOS that is `/usr/bin/rsync` (openrsync on macOS 15+, 2.6.9 earlier), which lacks options and protocol 31 | detect it on the backend, store it per host, pass `--rsync-path` (P2) |
| 8 | `internal/agent/probe.go`, setup scripts, `exec` | none, but on a Mac without the Command Line Tools, `/usr/bin/{python3,git,gcc,clang,make,…}` are shims that open a GUI install dialog on the backend's screen | detect CLT; skip or refuse shims (P2) |
| 9 | `internal/agent/env.go` `buildEnv` | none, but with `env_mode = "ssh"`, Homebrew (`/opt/homebrew/bin`) is not on `PATH` | Darwin default PATH fallbacks (P2) |
| 10 | foreground runs and detached jobs | none, but a Mac idle-sleeps: the SSH session drops (the run becomes `lost`) and jobs stall | hold a `caffeinate` assertion while work runs (P2) |
| 11 | `probe.go` GPU section | NVIDIA-only; the note says "no NVIDIA GPU tooling found" | report Apple GPU / Metal on Darwin (P3) |
| 12 | `internal/policy` `OutputExcludes` | none, but Finder writes `.DS_Store` and `._*` into a replica, which would be detected as outputs | add them to output excludes (P2) |
| 13 | APFS default is case-insensitive | none, but a Linux client with `a.txt` and `A.txt` would collide in the replica | probe and warn (P3) |
| 14 | user-facing strings | "recom backends must run Linux", `sudo apt install rsync`, `longHelp`, `cmd/recom/main.go` doc comment | make OS-aware (P1/P2) |
| 15 | CI / release | `grep -q "linux/amd64, linux/arm64"` in `ci.yml` and `release.yml`; e2e runs only on Ubuntu | update strings; add macOS e2e job (P1/P4) |

Already fine, no change needed: `statExtra` (Darwin `Ctimespec`, `Ino`),
`syscall.Statfs` (fields match), `Setpgid`/`Setsid`, `kill(-pgid)`, signal
names, `flock` in `lease.go` and `config/state.go`, `creack/pty` (supports
Darwin), the helper install script (`umask`/`mkdir`/`cat`/`chmod`/`mv`), the
login-env capture (`$SHELL -l -c`; zsh as a login shell reads
`/etc/zprofile` → `path_helper` and `~/.zprofile` → `brew shellenv`), the
setup script prelude and bodies (no GNU-only tools; checked against
`detect.go`), and the default remote root `~/.local/share/recom`, which sits
outside TCC-protected folders.

## Design decisions

**D1. Embed `darwin/arm64` and `darwin/amd64`.** Recommended. It costs one
entry in the Makefile loop and about 1.7 MB. Intel Mac minis and Mac Pros
still exist as build boxes. If client size matters more, ship arm64 only and
make `recom ssh` print a clear error for Intel Macs.

**D2. Require rsync ≥ 3.1.3 on macOS backends** (`brew install rsync`)
instead of trying to work with openrsync. Download needs `--files-from` with
a remote source and `--from0`, and the nanosecond mtime comparison
(`--modify-window=-1`) needs protocol 31 on both ends. This is the same rule
the client already applies to itself (`internal/rsync.required`).

**D3. Store the backend's rsync path in the host config** (`rsync_path` in
`config.Host`) and pass `--rsync-path=<quoted abs path>` whenever it is set.
Hosts registered earlier have no value and keep today's behavior, so nothing
changes for existing Linux users. It also helps Linux hosts whose rsync lives
off the default `PATH`.

**D4. Keep the client platform-agnostic where possible.** Platform knowledge
lives in the helper (`sys_darwin.go`, Darwin PATH fallbacks, CLT detection),
and the setup-script prelude checks the platform at runtime with `uname -s`
instead of making the client build different scripts per OS. The only
client-side branching is error hints keyed on `Host.OS`.

**D5. No protocol version bump.** The new `EnvResult` fields are additive and
`omitempty`, and the content-addressed helper always matches the client
build exactly. Bump `protocol.Version` only if a later phase changes request
semantics (for example, the tmux socket in D6).

**D6. tmux socket stays `-L recom` for now.** macOS's daily periodic job can
delete `/tmp` entries older than 3 days, which may include tmux sockets. The
job and its `output.log` keep running either way; only `recom attach` would
break. Verify this live in phase 4. If it happens, switch Darwin to
`-S <root>/tmux.sock` (the job record already carries `Socket`, so
`attachCmd` can choose `-S` when the value contains a `/`) and bump the
protocol version.

## Phase 1: core enablement (PR 1)

Goal: `recom ssh mac-mini` registers a Mac and `recom -- uname -a` works when
GNU rsync is already on the backend's ssh `PATH`.

1. **`internal/helperbin/helperbin.go`**
   - `Embedded()`: iterate over `linux/amd64, linux/arm64, darwin/amd64,
     darwin/arm64`.
   - `load()`: replace `goos != "linux"` with an allowlist
     (`linux`, `darwin`). Error text: "recom backends must run Linux or macOS".
   - Replace `checkELF` with `checkBinary(b, goos, goarch)`:
     - linux: the current ELF check.
     - darwin: `debug/macho.NewFile`, require `macho.CpuAmd64`/`CpuArm64` to
       match `goarch`, reject fat binaries, and for arm64 require an
       `LC_CODE_SIGNATURE` load command (without one the kernel SIGKILLs the
       binary at exec, which would look like a mysterious handshake failure).
   - Update the package doc comment and `assets/README.txt`.
2. **`Makefile` `helpers`**: nested loop `for os in linux darwin; for arch in
   amd64 arm64`, output `recom-helper-$os-$arch.gz`. The GoReleaser hook
   already calls `make helpers`, so only its comment changes.
3. **`internal/cli/hosts.go`**: accept `info.OS` in `{linux, darwin}`. Keep
   the arch check.
4. **`internal/agent/sys_darwin.go`** (new, `//go:build darwin`), and narrow
   `sys_other.go` to `!linux && !darwin` so other GOOS values still compile:
   - `statExtra`: as today (`Ctimespec`).
   - `procStart(pid)`: `unix.SysctlKinfoProc("kern.proc.pid", pid)`. Return
     `false` on error, if `kp.Proc.P_pid != pid` (verify what a non-existent
     PID returns: error or zeroed struct), or if `P_stat == SZOMB`. Return
     `uint64(sec)*1e6 + usec` of `P_starttime`. `golang.org/x/sys` is
     already a dependency.
   - `kernelRelease()`: `unix.Uname` (or `unix.Sysctl("kern.osrelease")`).
   - Move `osPretty()` into the sys files: Linux keeps `/etc/os-release`;
     Darwin returns `"macOS " + sysctl kern.osproductversion` (plus
     `kern.osversion` build number when present).
5. **Strings**: `internal/cli/root.go` `longHelp`, `cmd/recom/main.go` doc
   comment, and the `hosts.go` error messages.
6. **CI**: `ci.yml` build job and `release.yml` expect
   `darwin/amd64, darwin/arm64, linux/amd64, linux/arm64` (whatever order
   `Embedded()` returns; keep Linux first if the string should read
   naturally).
7. **Tests**:
   - `helperbin`: table tests for `checkBinary` using small synthesized ELF
     and Mach-O headers (wrong arch, missing signature on arm64, fat binary,
     garbage).
   - `agent`: a `procStart` test (own PID is stable across calls; a reaped
     child is not alive; a zombie child is not alive). It runs on both CI OSes.

## Phase 2: make a stock Mac work (PR 2)

Goal: a Mac with Homebrew and default settings works without per-user
tweaks, and fails with clear messages when something is missing.

1. **Backend rsync detection (D2, D3)**
   - Helper (`probe.go` `envReport`): resolve rsync candidates in order: the
     env's `PATH`, then on Darwin `/opt/homebrew/bin/rsync` and
     `/usr/local/bin/rsync`. Run `--version` on each and report the first
     acceptable one. Reuse the parsing in `internal/rsync`: move
     `versionAtLeast`/openrsync detection into a small shared function, since
     the helper must not import client-only code.
   - `protocol.EnvResult`: add `RsyncPath`, `RsyncVersion`,
     `RsyncProblem string` (`omitempty`).
   - `recom ssh`: currently probes with `EnvSSH`. Use the login env for this
     probe, because that is where Homebrew appears. Then:
     - no usable rsync → refuse, with an OS-aware hint (`brew install rsync`
       on Darwin, `sudo apt install rsync` on Linux);
     - otherwise store `Host.RsyncPath`, but only when it differs from what a
       bare `rsync` resolves to under `EnvSSH`, to keep Linux configs clean.
   - `internal/rsync` `Upload`/`Download`: add
     `--rsync-path=<sshx.Quote(path)>` when `t.Host.RsyncPath != ""`.
   - `recom doctor` (`status.go:401`): report the backend rsync path and
     version, and suggest re-running `recom ssh` if the stored path no longer
     exists.
2. **Xcode Command Line Tools shims**
   - Helper on Darwin: `cltInstalled()` runs `/usr/bin/xcode-select -p`
     (this never prompts) with a short timeout and checks that the directory
     exists. Cache the result per process.
   - `probe.go`: when CLT is missing, do not execute any `/usr/bin` tool from
     the shim list (`python3 pip3 git gcc g++ cc c++ clang clang++ make`). Mark
     them missing and add a warning: "Xcode Command Line Tools are not
     installed on the backend (run `xcode-select --install` there)".
   - `exec` (`prepare` → `lookPath`): if argv[0] resolves to one of those
     shims and CLT is missing, fail with exit 127 and the same hint instead
     of exec'ing it, so nothing pops up on the Mac's screen.
   - Setup prelude (`internal/toolchain/toolchain.go`): on Darwin, `have()`
     treats `/usr/bin/<shim>` as absent when `xcode-select -p` fails. One
     `case "$(uname -s)"` block in the prelude; the prelude is not part of the
     fingerprint, so existing stamps stay valid. Make the `python3-venv` note
     in `pythonLib` OS-aware the same way.
3. **PATH fallbacks**: in `buildEnv`, on Darwin append `/opt/homebrew/bin`,
   `/opt/homebrew/sbin` and `/usr/local/bin` through the existing
   `appendFallback` (existing directories only, after everything else). This
   only matters for `env_mode = "ssh"` and for the rsync probe.
4. **Conda on macOS**: add `/opt/homebrew/Caskroom/miniforge/base/bin/conda`
   and `/usr/local/Caskroom/miniforge/base/bin/conda` to the candidate list in
   `detectConda`.
5. **Keep the Mac awake** (Darwin only, in the helper):
   - Foreground `exec`: start `/usr/bin/caffeinate -i -s -w <helper pid>` before
     starting the command, and kill it when the command finishes. `-w` makes it
     exit on its own if the helper dies.
   - Detached jobs: the supervisor starts `caffeinate -i -s -w <supervisor
     pid>` after it has claimed the lease.
   - Best effort: a missing `caffeinate` is ignored. Document that `-s` only
     applies on AC power and that a closed MacBook lid without an external
     display still sleeps.
   - Opt-out: environment variable `RECOM_NO_CAFFEINATE=1` on the backend.
     Leave out a config key until someone asks for one.
6. **Output excludes**: add `.DS_Store` and `._*` to
   `policy.OutputExcludes` (they are already upload excludes). This is
   harmless on Linux.
7. **Hints keyed on `Host.OS`**: `sudo apt install …` vs `brew install …` in
   `hosts.go:112` and `status.go:401`.
8. **Tests**: unit tests for the rsync version classifier (GNU 3.4.1,
   openrsync, 2.6.9), `--rsync-path` argument construction, the prelude's
   `have()` on Darwin (run the script under `sh -n`, plus a fake `xcode-select`
   on `PATH` in a temp dir), and the Darwin PATH fallbacks.

## Phase 3: polish (PR 3)

1. **GPU report on Darwin**: `sysctl machdep.cpu.brand_string` (for example
   `Apple M2 Max`), `hw.memsize`, and `hw.perflevel0.physicalcpu`/
   `hw.perflevel1.physicalcpu`. `GPUs = ["Apple M2 Max GPU (Metal, 64 GiB
   unified memory)"]`, with `GPUNote` reading "CUDA is unavailable on macOS;
   use Metal/MPS (e.g. PyTorch device \"mps\")". Avoid
   `system_profiler` (slow). Skip the `nvidia-smi`/`nvcc` probes on Darwin.
2. **Case-insensitive filesystem**: in `envReport`, create
   `<root>/.case-probe-Ab`, `Lstat` `.case-probe-ab`, and report
   `CaseInsensitive`. `recom ssh` stores it in the host config. Before
   upload, the client warns when two files to upload fold to the same
   lowercase name. This only affects Linux clients; macOS clients are already
   case-insensitive.
3. **Spotlight / Time Machine**: when the helper creates the root on Darwin,
   write `<root>/.metadata_never_index` and run `tmutil addexclusion <root>`
   (no root needed for user-owned paths). Replicas full of `.venv` and
   `node_modules` otherwise get indexed and backed up. Best effort, and
   documented.
4. **Probe tools**: add `brew` to `probeTools` (harmless on Linux, where it is
   usually absent).
5. **Example**: `examples/mps-pytorch/` (one script choosing `mps` and
   falling back to `cpu`) next to `examples/cuda-pytorch`.

## Phase 4: CI, live verification, docs (PR 4, can be split across earlier PRs)

1. **macOS e2e job** in `ci.yml` (`macos-latest`, arm64):
   `brew install rsync tmux`, then `make test-integration`.
   - The harness already runs an unprivileged `/usr/sbin/sshd` with
     `UsePAM no`. Verify this works on macOS runners. Known differences: the
     temp dir under `/var/folders/...` is long (ControlPath already falls
     back when it is over 100 bytes), and `sshd` may require an absolute path
     to the config.
   - `RECOM_HELPER_PATH` now goes through the Mach-O check; the e2e build
     uses the internal linker, so the arm64 signature is present.
   - The remote side of this local sshd sees macOS's `/usr/bin/rsync` first,
     so the e2e run exercises the `--rsync-path` path from phase 2 for free.
   - Audit `internal/e2e` for Linux assumptions (only the hard-coded
     `os = "linux"` host in `identity_test.go` was found, and that one is
     fine) and skip-guard anything that turns out Linux-only.
   - Optional: an Intel runner (`macos-15-intel` or current equivalent) for
     darwin/amd64.
2. **Live checklist**, run on a real Apple Silicon Mac from a Linux client
   and from a Mac client:
   - `recom ssh` with Homebrew rsync and without it (error message), with and
     without CLT;
   - foreground run, outputs, `--tty`, resize, Ctrl-C, stdin;
   - `recom init` with uv, pip and Node setups;
   - `--detach`, `jobs`, `logs -f`, `attach`, `stop`; job survives client
     disconnect; `kill -9` supervisor → `lost` recovery;
   - idle sleep: start a 30-minute job, leave the Mac idle, confirm it
     doesn't sleep (`pmset -g assertions` shows `caffeinate`);
   - PyTorch MPS example over SSH (confirm Metal is available in an SSH
     session);
   - reboot recovery; tmux socket after more than 3 days (D6).
3. **Docs**:
   - `README.md`: requirements ("Backend: Linux or macOS (Remote Login
     enabled), rsync 3.x — `brew install rsync` on macOS"), plus the
     "currently only supports" sentence.
   - `docs/architecture.md`: diagram label, helper section ("embeds Linux and
     macOS helpers"), Limitations, and the Locking section (Darwin start time
     from `sysctl`).
   - `docs/troubleshooting.md`: enabling Remote Login, openrsync, CLT
     prompts, sleep, TCC ("Allow full disk access for remote users" is only
     needed for replicas or data under Desktop/Documents/Downloads, iCloud, or
     external volumes), the case-insensitive FS, and Unicode normalization.
   - `docs/environments.md`: Homebrew, PATH under `env_mode`, Metal/MPS
     instead of CUDA.
   - `docs/implementation-status.md`: platforms table.
   - `CHANGELOG.md`, and the `.goreleaser.yaml` release notes ("Every client
     embeds the Linux helpers…").

## Risks and open questions

- **`kern.proc.pid` for a non-existent PID**: may return success with a
  zeroed struct rather than an error. Check `P_pid` either way (covered by the
  P1 unit test).
- **Metal over SSH**: expected to work for compute (no window server
  needed), but confirm live before the docs promise it.
- **Login shell rc files**: `zsh -l -c` reads `.zprofile`, not `.zshrc`.
  Users who put `brew shellenv`, nvm or `conda init` in `.zshrc` won't get
  those under `env_mode = "login"`. This already holds for bash `.bashrc` on
  Linux and is mostly covered by `PathFallback`; document it, and consider
  `zsh -l -i -c` only if it causes real complaints (interactive rc files are
  noisy and slow).
- **TCC**: if a user points `--remote-root` at a protected folder, writes
  fail with `EPERM`. `doctor` already tests writability; add the TCC hint to
  that failure on Darwin.
- **Client size**: about 3.2 MB more per client (D1).
- **macOS runner sshd**: if an unprivileged sshd doesn't work on GitHub's
  macOS images, fall back to running the helper protocol over a local pipe
  for most e2e tests and keep the full SSH path for the live checklist.

## Out of scope

- Windows backends.
- Running commands inside macOS sandboxes or as other users.
- CUDA on macOS (not possible). Metal-specific features beyond reporting the
  GPU.
- Automatically installing Homebrew, rsync or CLT on the backend (recom
  doesn't need root, and doesn't install system software).
