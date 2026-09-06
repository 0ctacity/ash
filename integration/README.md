# OpenSSH integration tests

Run from the repository root:

```sh
ASH_INTEGRATION=1 go test -race -v ./integration
```

The suite starts the locally installed `sshd` on an ephemeral loopback port, generates temporary client and host keys, and uses an isolated configuration, authorized-keys file, known-hosts file, and working directory. It runs as the current user and requires an OpenSSH daemon that can run under that user. It does not connect to configured ASH hosts or modify your SSH configuration. On macOS it falls back to `/usr/sbin/sshd` if `sshd` is absent from PATH.

Coverage includes commands, cwd and environment quoting, nonzero exit status, separate output streams, output limits, SFTP create/truncate/read/stat and read limits, timeout, cancellation, stalled SSH handshake, unknown host keys, incorrect identity, SSH agent authentication, and fallback from an unrecognized agent key to an explicit identity. It also builds ASH and uses the official MCP client over stdio to execute a command and write/read/stat a file through the same server.

Ordinary `go test ./...` skips the local daemon fixture unless `ASH_INTEGRATION=1` is set. Docker is not required; the development environment's Docker daemon was unavailable, so the fixture uses real local OpenSSH instead. Stalled SFTP subsystem negotiation is not separately simulated.

An explicitly configured identity must currently be a readable, unencrypted private key even when an agent is present. For encrypted keys, load the key into your SSH agent and omit `identity` from ASH configuration.

## Persistent Zellij shells

Zellij integration uses a configured remote host with Zellij 0.44 or newer and `exec = true`:

```sh
ASH_ZELLIJ_HOST=fedora ASH_ZELLIJ_CONFIG="$HOME/.config/ash/config.toml" \
  go test -race -v ./integration ./cmd/ash -run 'TestZellij|TestPersistentShellMCPReconnect'
```

The backend test verifies an isolated ASH shell, literal input/output, an adversarial cwd, reconnection, external termination, and cleanup. The MCP test disconnects one ASH subprocess and reconnects through another, verifies a retained shell variable, then checks output and idempotent closure. Each test uses its own random shell ID and cleans up its session. Neither test operates on personal Zellij sessions.
