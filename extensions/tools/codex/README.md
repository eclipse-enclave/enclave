# Codex

OpenAI Codex CLI is OpenAI's terminal-based AI coding agent.

## Configuration

- **Command**: `codex`
- **YOLO flag**: `--dangerously-bypass-approvals-and-sandbox`
- **Config directory**: `~/.codex`
- **Settings file**: `~/.codex/config.toml`

## API Keys

| Variable | Purpose |
|----------|---------|
| `OPENAI_API_KEY` | OpenAI API access |

## Auth Files

- `auth.json`

## Network Access

Allowlisted domains include OpenAI, GitHub, and common package registries
(npm, PyPI, Go, CDNs, TLS/OCSP).

## Settings

The default `config.toml` template configures:

- Analytics, telemetry (OTEL), and feedback disabled
- Update checks disabled
- Model personality prompt disabled
- Fast mode (fast service tier with increased plan usage) opted out

Model selection and reasoning effort are left at the Codex defaults; override
them via config patches if needed.

In yolo mode (`ENCLAVE_YOLO=1`), the project workspace is pre-trusted by
appending a `[projects."<dir>"]` table with `trust_level = "trusted"` to
`config.toml`, so Codex does not prompt for workspace trust inside the
already-sandboxed container.

An explicit user trust setting always wins; pre-trust applies only when no trust
is configured for the directory being opened.

### Optional: TUI status line

To show branch and usage limits in Codex's TUI status line, add a TOML patch:

- Global: `~/.config/enclave/patches/codex/config.toml`
- Per-project: `~/.config/enclave/projects/<project-hash>/patches/codex/config.toml`

```toml
[tui]
status_line = ["git-branch", "context-remaining", "five-hour-limit", "weekly-limit"]
```

## Files

| File | Purpose |
|------|---------|
| `spec.yaml` | Extension manifest (metadata, sandbox behavior, network, credentials) |
| `install.sh` | Installs Codex via private agent npm (`enclave-agent-npm-install @openai/codex`) |
| `check-update.sh` | Returns the latest npm version for automatic update probes |
| `gateway-allowlist.conf` | DNS allowlist for network isolation |
| `templates/config.toml` | Default settings template |
| `entrypoint.d/setup.sh` | Runtime setup (creates config dir; in yolo mode pre-trusts the workspace) |
| `go/` | Custom Go hooks |
