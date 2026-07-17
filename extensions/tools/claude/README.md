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
| `CLAUDE_CODE_OAUTH_TOKEN` | OAuth-based authentication |

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
committed). Pass `--no-memory` to disable the mount. Ephemeral
(`--ephemeral`) sessions skip the host mount, so memory written during them
is discarded with the session's config store.

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
| `install.sh` | Installs Claude Code via `claude.ai/install.sh` |
| `check-update.sh` | Returns the latest GitHub release tag for automatic update probes |
| `gateway-allowlist.conf` | DNS allowlist for network isolation |
| `templates/settings.json` | Default settings template |
| `entrypoint.d/setup.sh` | Runtime setup (creates config dir, sets env vars) |
| `go/` | Custom Go hooks |
