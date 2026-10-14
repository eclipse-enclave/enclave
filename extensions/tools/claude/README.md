# Claude Code

Claude Code is Anthropic's official AI coding assistant CLI.

## Configuration

- **Command**: `claude`
- **YOLO flag**: `--dangerously-skip-permissions`
- **Config directory**: `~/.claude`
- **Settings file**: `~/.claude/settings.json`

## API Keys

| Variable | Purpose |
|----------|---------|
| `ANTHROPIC_API_KEY` | Anthropic API access |
| `CLAUDE_CODE_OAUTH_TOKEN` | Env-only token from `claude setup-token` |

With restricted networking, the setup token is replaced by a gateway
placeholder and released only to `api.anthropic.com`. Stored `/login` credentials
are not covered by this env-only protection. See [authentication](../../../docs/auth.md#env-only-bearer-tokens).

When `ANTHROPIC_API_KEY` is injected, `entrypoint.d/setup.sh` pre-approves it in
`~/.claude.json` (`customApiKeyResponses.approved`, the last 20 characters of the
key) so Claude Code does not show its interactive "Detected a custom API key"
prompt. This is seeded on every run so it also works under `--ephemeral`, where
`~/.claude.json` is not persisted.

## Auth Files

Credential files mounted from the host `~/.claude` directory:

- `config.json`
- `.credentials.json`

In the default `shared` auth scope, enclave points
`CLAUDE_SECURESTORAGE_CONFIG_DIR` at the shared auth store. Claude itself reads
and writes `.credentials.json` and `.oauth_refresh.lock` there and performs its
native OAuth refresh coordination, so parallel, long-running sessions share the
same Claude-managed credential store instead of invalidating each other's
tokens. See `docs/auth.md`.

## Memory

Claude Code's auto memory is enabled by default (`autoMemoryEnabled`), pinned to
`~/.claude/memory` inside the container. That directory is bind-mounted from the
per-project host location `~/.local/state/enclave/projects/<hash>/claude/memory/`, so
memory is scoped per project and stored outside the working directory (never
committed). Pass `--no-memory` to skip the mount and disable memory generation
(`--settings '{"autoMemoryEnabled": false}'` is added to the launch arguments).
Ephemeral (`--ephemeral`) sessions do the same, so no memory is written or
carried over.

## Network Access

Allowlisted domains include Anthropic, GitHub, GitLab, and common package
registries (npm, PyPI, Go, Rust, CDNs, TLS/OCSP).

## Settings

The default settings template disables telemetry, error reporting, feedback
surveys, and non-essential network traffic. Reasoning/thinking settings are
left at the Claude Code defaults.

## Files

| File | Purpose |
|------|---------|
| `spec.yaml` | Extension manifest (metadata, sandbox behavior, network, credentials) |
| `install.sh` | Installs the Claude Code client via `claude.ai/install.sh` and `@anthropic-ai/sandbox-runtime` from npm with `--ignore-scripts` (asserting the package landed) |
| `check-update.sh` | Returns the latest GitHub release tag for automatic update probes |
| `gateway-allowlist.conf` | DNS allowlist for network isolation |
| `templates/settings.json` | Default settings template |
| `entrypoint.d/setup.sh` | Runtime setup |
| `go/` | Custom Go hooks |

## Experimental protected OAuth

`--protect-auth` (with a fresh `--auth-name`, `--auth-scope project` or `--ephemeral`) runs the same
Claude client as ordinary sessions, with its normal updates and settings
sources; host-provided credential-source overrides are unsupported. The gateway
mediates all Anthropic and Claude domains, so custody does not depend on the
client version. The built-in `github-cli` feature uses its own protected adapter;
other features with native auth files are rejected. Env-only credentials are
unaffected. Config and history persist in a protected-only config store at
`~/.claude`; the handle file lives in a disposable session view at
`~/.enclave-auth`. The gateway keeps the real
grant outside the workload. Multiple protected sessions can share the same named
identity: gateways coordinate refresh through a common lock while retaining
separate handles and views. Reset/downgrade require all its sessions to end.
See [auth](../../../docs/auth.md#experimental-gateway-owned-claude-oauth)
and the [pending verification plan](../../../docs/runtime/oauth-broker-verification.md).
