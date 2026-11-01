# Troubleshooting

Start with `recom doctor`: it checks every piece and says which one fails.
Add `-v` to any command to see the exact ssh and rsync invocations.

| Symptom | Fix |
|---|---|
| `cannot connect to <host>` (exit 255) | Make sure `ssh <target> true` works without recom. With Tailscale, check `tailscale status` on both machines. recom never accepts unknown host keys for you: run `ssh <target>` once to accept the key. |
| Asked for a passphrase every run | Load the key into your agent (`ssh-add`, or `UseKeychain yes` + `AddKeysToAgent yes` on macOS). recom reuses one multiplexed connection for ~2 minutes. |
| `no usable local rsync` on macOS | macOS ships openrsync / rsync 2.6.9. Install rsync 3, e.g. `brew install rsync`. |
| recom was installed with Homebrew | The tap is discontinued. `brew uninstall recom`, then install with the script in the [README](../README.md#install). Later updates: `recom update`. |
| `recom update`: `permission denied: cannot write to /usr/local/bin` | recom ≤ 0.2.0 installed into `/usr/local/bin` by default. Reinstall with the script in the [README](../README.md#install) (it now installs into `~/.local/bin`, no sudo), then `sudo rm /usr/local/bin/recom`. Or keep it there and run `sudo recom update`. |
| `recom: command not found` after installing | `~/.local/bin` is not on your `PATH` (common on macOS). Add `export PATH="$HOME/.local/bin:$PATH"` to `~/.zshrc` (or your shell's profile) and open a new shell. |
| `rsync … protocol data stream` / doctor warns about shell output | Your shell rc file prints text for non-interactive logins. Wrap it in an interactive-only guard, e.g. `[[ $- == *i* ]] && echo …`. |
| `command not found on the backend` (127) | The program is not on the remote `PATH` in the environment recom uses. Check `recom env`; set `[run] path`, `env_mode` or `wrapper` (see [configuration](configuration.md#remote-environment)). |
| `python3: No module named …` | List the package in `requirements.txt` / `pyproject.toml`; recom installs it on the backend before the next run. `recom status` shows whether the environment is set up; `recom env` shows which `python3` runs. If `[setup] toolchains` is `[]`, install by hand with `recom --shell '…'`. |
| `<tool> is not installed on the backend (or not on its PATH)` during setup | recom does not install runtimes or package managers. Install the named tool on the backend. If it is installed through a version manager recom does not know, add its bin directory to `[run] path`. |
| `… environment setup failed … the command was not run` (125) | The install failed on the backend; its output is printed above the message. Fix the dependency file and run again, `recom setup` to retry only the setup, `recom setup --clean` to rebuild from scratch, or `recom --no-setup <cmd>` to skip it once. |
| `ensurepip is not available` / `python3 -m venv failed` | Debian/Ubuntu split venv into a package: `sudo apt install python3-venv` on the backend, or install uv there (recom then uses it). |
| `working directory … does not exist on the backend` | You are in a directory that is excluded from sync (`.venv/`, `node_modules/`, …). Run recom from the project or a synced subdirectory. |
| `the project is busy on this backend: detached job …` | A job owns the replica. `recom logs <id> -f`, `recom stop <id>`, or `recom --no-sync <cmd>` for quick checks. |
| `waiting for run … by …` | Another run of this working copy is in progress. It continues once that finishes (Ctrl-C to give up). |
| An output did not come back | `recom status` lists pending and held outputs with the reason. Fetch with `recom pull <path>` or `recom pull --all`. |
| `conflict: <file> was changed locally …` | Your version was kept; the remote one is in `.recom/conflicts/<run>/`. Merge by hand; your version is uploaded on the next run. |
| Exit 75 | The command succeeded but some outputs could not be downloaded; they stay pending and are retried on the next run. |
| `refusing to make your home directory a recom project` | Run `recom init` in the project directory, not in `~`. |
| tmux `open terminal failed` on attach | Your terminal's `TERM` is unknown on the backend; recom falls back to `xterm-256color` when `TERM` is empty, otherwise set e.g. `TERM=xterm-256color recom attach <id>`. |

## Starting over

- Re-register a backend: `recom ssh <target>` again (updates in place).
- Forget a backend: `recom hosts remove <name>` (remote files stay).
- New project identity: delete `.recom.toml` and `.recom/`, then `recom init`.
- Remove a replica: check `recom status` for pending outputs, then delete
  `~/.local/share/recom/projects/<id>` and `~/.local/share/recom/state/<id>` on
  the backend.
