# Security policy

## Reporting a vulnerability

Please report vulnerabilities privately through
[GitHub security advisories](https://github.com/chaseungjoon/recom/security/advisories/new)
rather than in public issues. Include the recom version (`recom version`), the
client and backend platforms, and steps to reproduce. You should get a reply
within a week.

## Trust model

recom runs the commands you type on a backend you registered, over your own
OpenSSH setup. Treat a backend like any machine you `ssh` into: anyone who can
run code as your user there can also read the project files recom uploads.

What recom guarantees, and what is in scope for reports:

- Commands run **only** on the backend; recom never falls back to running them locally.
- Arguments reach the remote program as an exact argv (framed protocol, no shell),
  unless you explicitly use `--shell`.
- OpenSSH host-key verification is never disabled; recom stores no passwords or keys.
- Common secret files (`.env`, private keys, cloud credentials, …) are neither
  uploaded nor downloaded unless you opt them in.
- Remote output paths cannot escape the local project (`os.Root`, path validation,
  symlink checks), and local files changed during a run are never overwritten.
- The remote helper is content-addressed by SHA-256 and verified on every
  connection; it lives in your private `~/.local/share/recom` and needs no root.

Residual risks: files you explicitly include, secrets embedded in ordinary
source files, and anything printed by your own programs. See
[docs/architecture.md](docs/architecture.md#security) for details.
