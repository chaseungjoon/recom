# Troubleshooting

Start with `recom doctor`: it checks every piece and says which one fails.
Add `-v` to any command to see the exact ssh and rsync invocations.

| Symptom | Fix |
|---|---|
| `cannot connect to <host>` (exit 255) | Make sure `ssh <target> true` works without recom. With Tailscale, check `tailscale status` on both machines. recom never accepts unknown host keys for you: run `ssh <target>` once to accept the key. |
| Asked for a passphrase every run | Load the key into your agent (`ssh-add`, or `UseKeychain yes` + `AddKeysToAgent yes` on macOS). recom reuses one multiplexed connection for ~2 minutes. |
| `Refusing to load formula … from untrusted tap` | Current Homebrew asks you to trust third-party taps once: `brew trust chaseungjoon/recom`, then `brew install recom`. |
| `no usable local rsync` on macOS | macOS ships openrsync / rsync 2.6.9. Run `brew install rsync` (the Homebrew formula depends on it). |
| `rsync … protocol data stream` / doctor warns about shell output | Your shell rc file prints text for non-interactive logins. Wrap it in an interactive-only guard, e.g. `[[ $- == *i* ]] && echo …`. |
| `command not found on the backend` (127) | The program is not on the remote `PATH` in the environment recom uses. Check `recom env`; set `[run] path`, `env_mode` or `wrapper` (see [configuration](configuration.md#remote-environment)). |
| `python3: No module named …` | Packages must be installed **on the backend**, e.g. `recom --shell 'uv venv .venv && uv pip install -r requirements.txt'` plus `path = [".venv/bin"]`. |
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
