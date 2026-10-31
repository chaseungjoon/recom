# Contributing

Thanks for helping. Bug reports with `recom doctor` output and a minimal
reproduction are the most useful thing you can send.

## Development

Requirements: Go (see `go.mod`), rsync 3.x, OpenSSH client and server
(`sshd` is used unprivileged by the integration tests), tmux.

```sh
make build              # bin/recom with embedded linux/amd64 + linux/arm64 helpers
make test               # unit tests with the race detector
make test-integration   # end-to-end tests against a throwaway sshd on 127.0.0.1
make lint               # gofmt + go vet
```

The integration tests start their own `sshd` on a random local port with fresh
keys; they never touch `~/.ssh` or your recom configuration.

## Guidelines

- Safety first: no change may make recom run commands locally, overwrite a
  locally modified file, delete user data, or weaken host-key checking.
- Keep the helper protocol versioned (`internal/protocol`); bump
  `protocol.Version` for incompatible changes.
- Add a test with every behavior change, preferably an end-to-end one in
  `internal/e2e` for anything involving sync, outputs or jobs.
- Update `docs/` and `CHANGELOG.md` when user-visible behavior changes.

## Releasing

Tag `vX.Y.Z` on `main`; the release workflow tests, builds with GoReleaser,
publishes checksummed archives and updates the Homebrew tap
(`chaseungjoon/homebrew-recom`, needs the `HOMEBREW_TAP_TOKEN` secret, or run
`scripts/homebrew-formula.sh` by hand).
