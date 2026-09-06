# ASH — Agentic Shell

ASH lets coding agents operate on other machines over SSH. It is a local Go binary with a CLI and MCP tools for execution, SFTP file access, and persistent shells. No remote ASH daemon is required.

## Build

Requires Go 1.26.5 or newer.

```sh
go build -o ash ./cmd/ash
./ash --help
```

## Configure a host

Create `~/.config/ash/config.toml`:

```toml
[hosts.fedora]
address = "100.64.1.20"
user = "ata"
# port = 22
# identity = "~/.ssh/id_ed25519"

[hosts.fedora.policy]
exec = true
read = true
write = true
```

Replace the address and user with your remote machine's values. Each capability defaults to `false`; `stat` requires `read`. Unknown configuration keys are rejected. Use `--config PATH` before the subcommand to select another configuration file.

Authenticate using keys from `SSH_AUTH_SOCK`, or an explicit identity file. Load encrypted keys into your SSH agent and omit `identity` in that case; an explicitly configured identity must be readable and unencrypted. ASH does not prompt for passwords or key passphrases.

ASH verifies the configured address and port against `~/.ssh/known_hosts`. Establish trust yourself with OpenSSH, verifying the host fingerprint through a trusted channel. For the example above:

```sh
ssh ata@100.64.1.20
```

Use `ssh -p PORT USER@ADDRESS` for a custom port. ASH does not interpret OpenSSH aliases, `ProxyJump`, or other `~/.ssh/config` settings. Listing hosts and starting MCP do not require a connection or a known-hosts file; remote operations require a trusted host key.

## CLI

```sh
./ash hosts
./ash exec fedora -- uname -a
./ash exec fedora --cwd '~/projects/zova' --env CI=true --timeout 30s -- go test ./...
./ash read fedora /etc/os-release
printf 'hello from ASH\n' | ./ash write fedora /tmp/ash-test.txt
./ash stat fedora /tmp/ash-test.txt
```

`exec` joins everything after `--` with spaces into **shell code**, executed through the remote user's shell. For shell expressions or arguments containing spaces, pass one quoted command string:

```sh
./ash exec fedora -- 'printf "%s\n" "hello world"; exit 7'
```

`cwd` and environment values are escaped as literal values; environment names must be valid shell identifiers. Execution assumes a POSIX-compatible remote shell. Quote remote `~/` paths so your local shell does not expand them. SFTP resolves `~/` against its initial remote directory, normally the user's home.

Command stdout and stderr stay separate, and the CLI returns the remote process exit code. ASH failures print a diagnostic to stderr and return `1`. `hosts` and `stat` print JSON; `read` writes file bytes to stdout; `write` consumes stdin and creates or truncates the file. Parent directories must exist. Writes are not atomic and interruption may leave a partial file.

Commands default to a five-minute timeout. File operations default to 30 seconds. Cancellation closes the SSH connection/session; it does not guarantee termination of detached remote descendants. Each operation opens and closes its own SSH connection.

Each command output stream is capped at 8 MiB and reports truncation. Reads and writes are capped at 4 MiB. These limits bound captured output/file data, not all memory used by an MCP client's incoming protocol message.

## Persistent shells

Persistent shells use [Zellij’s headless CLI](https://zellij.dev/documentation/cli-recipes.html) and require Zellij 0.44 or newer installed on the remote host. They are separate from one-shot `exec`: shell variables, working directory, and running commands survive ASH process exits and SSH disconnects. ASH opens short SSH connections to control Zellij; it does not keep an SSH session alive.

```sh
./ash shell create fedora --cwd '~/projects'
./ash shell list fedora
```

Creation returns JSON containing an ASH `id`, `host`, and `backend`. Set `SHELL_ID` to the returned `id`, then:

```sh
printf 'pwd\n' | ./ash shell send fedora "$SHELL_ID"
./ash shell read fedora "$SHELL_ID"
./ash shell close fedora "$SHELL_ID"
```

`send` accepts an optional literal INPUT argument, or reads stdin when omitted. It adds no newline: include one to submit a command. It returns after delivering input, without waiting for the shell command to finish. Input is UTF-8 without NUL and is limited to 64 KiB.

`read` returns a snapshot of rendered terminal text and available scrollback, with stdout and stderr merged. Repeated reads can repeat output. Snapshots are bounded by the existing 8 MiB transport limit and report truncation. This is a polling interface, with no incremental cursor, streaming, full-screen TUI support, or per-command exit status. Use one-shot `exec` when you need a structured command result.

Every shell operation requires the host's existing `exec` capability. No additional policy flag is introduced. ASH-generated IDs map to reserved remote session names; ASH lists and controls only sessions in that namespace. Liveness is queried from Zellij, with no local session metadata to become stale. Exited sessions are not listed. Closing a shell is idempotent and also removes its ASH-owned backend configuration after an external termination. The namespace is organizational ownership, not isolation from other processes running as the same remote account. Zellij startup settings live in the remote `~/.cache/ash/shells/ID/config.kdl` until the shell is closed; this file is configuration, not a liveness record.

Control operations have a 30-second deadline. That deadline limits the control request, not the lifetime of the persistent shell or a command sent to it. Persistence covers ASH/SSH disconnects, not host reboots or termination of Zellij. Treat a canceled or failed send as potentially delivered; do not blindly retry commands with side effects.

## MCP

Configure your MCP client to launch the built binary over stdio:

```json
{
  "mcpServers": {
    "ash": {
      "command": "/absolute/path/to/ash",
      "args": ["--config", "/absolute/path/to/config.toml", "mcp"]
    }
  }
}
```

Client configuration formats vary. ASH serves only stdio; stdout is reserved for protocol messages.

| Tool | Inputs | Result |
| --- | --- | --- |
| `ash_hosts` | `{}` | Public host metadata and capabilities |
| `ash_exec` | `host`, `command`; optional `cwd`, `env`, `timeout_ms` | `exit_code`, `stdout`, `stderr`, truncation flags, `duration_ms` |
| `ash_read` | `host`, `path` | UTF-8 `content` and byte `size` |
| `ash_write` | `host`, `path`, `content` | Written byte `size` |
| `ash_stat` | `host`, `path` | `path`, `size`, `mode`, `is_dir`, `modified_at` |
| `ash_shell_create` | `host`; optional `cwd` | `id`, `host`, `backend` |
| `ash_shell_list` | `host` | `shells`: live ASH-owned shells |
| `ash_shell_send` | `host`, `shell_id`, `input` | `sent` |
| `ash_shell_read` | `host`, `shell_id` | `content`, `truncated` |
| `ash_shell_close` | `host`, `shell_id` | `closed` |

A non-zero remote process exit is a successful MCP tool result. Connection, authentication, trust, policy and timeout failures are tool errors. MCP reads reject invalid UTF-8; binary MCP file semantics are not supported. Host listings omit identity paths and authentication internals.

Capabilities grant access with the remote account's permissions. They do not constrain paths or commands: an enabled `exec` capability can itself read or modify files. Configure only hosts and accounts you intend the connected agent to operate.

## Architecture

CLI and MCP call the same service layer. Services resolve hosts, enforce policy, and set deadlines. The transport interface implements execution and file operations; its SSH backend uses verified SSH sessions and SFTP. Transport code does not make authorization decisions. A separate shell service enforces the same host policy and calls a shell backend interface. The Zellij backend uses the SSH transport for short control commands; CLI and MCP contain no Zellij-specific logic.

ASH excludes connection pooling, full-screen terminal attachment, jobs, synchronization, forwarding, discovery, sudo handling, and HTTP.

## Verify

```sh
go test ./...
go vet ./...
```

The tests cover configuration, policy, shell escaping, output limits, CLI behavior and the official MCP client/server protocol. Run `ASH_INTEGRATION=1 go test -race ./integration` to exercise a real local OpenSSH daemon. See [integration](integration/README.md) for prerequisites. The daemon fixture is skipped unless explicitly enabled.

Persistent-shell integration requires a trusted host with Zellij 0.44 or newer:

```sh
ASH_ZELLIJ_HOST=fedora ASH_ZELLIJ_CONFIG="$HOME/.config/ash/config.toml" \
  go test -v ./integration ./cmd/ash -run 'TestZellij|TestPersistentShellMCPReconnect'
```

These opt-in tests create and clean up their own remote ASH sessions. They cover shell creation, state and output across reconnects, listing, external termination, and cleanup. Ordinary test runs skip them. The host must already be configured and trusted.

## CI and releases

[GitHub Actions](.github/workflows/ci.yml) follows Elephant's native build and release workflow. Pushes, pull requests, and manual runs check formatting, release packaging tests, Go tests (including real MCP subprocesses), race detection, and `go vet` on:

- Linux ARM64 (`ubuntu-24.04-arm`)
- Linux AMD64 (`ubuntu-24.04`)
- Windows AMD64 (`windows-2022`)
- macOS ARM64 (`macos-15`)

The two tests that execute a POSIX shell locally run on Unix runners. Configured-host Zellij tests and the opt-in local OpenSSH daemon fixture retain their explicit opt-in settings; the standard CI run needs no SSH credentials.

Pushing a tag such as `v0.1.0` or `v0.1.0-rc.1` builds four native release archives. Unix targets use `.tar.gz`; Windows uses `.zip`. Each contains the executable, this README, and dependency/Go license notices. A release-wide `SHA256SUMS.txt` covers exactly those four archives.

The release job waits for every platform to succeed, creates or resumes a draft GitHub release, uploads all assets, then publishes it. Tags with a prerelease suffix are marked as prereleases. Already-published releases are not overwritten. Release publishing uses the workflow's GitHub token with `contents: write`; build jobs have read-only permissions.

Release builds stamp the same version into `ash --version` and MCP server metadata. Branch builds use `dev-COMMIT`; ordinary local builds report `dev`.

```sh
python3 -m unittest discover -s scripts -p 'test_*.py' -v
python3 scripts/release.py version v0.1.0-rc.1
```

The workflow becomes active once this project is pushed to GitHub. Tagging and publishing are separate from implementing these workflow files.
