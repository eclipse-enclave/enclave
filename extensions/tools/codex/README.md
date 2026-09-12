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
- Memories enabled

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

## Memory

Enclave's default template enables [Codex memories](https://developers.openai.com/codex/memories)
with `features.memories = true`. This lets Codex use eligible conversation content
for memory generation and consumes additional model quota. A full override such
as `~/.config/enclave/tools/codex/config.toml` replaces the template, so those
users get the feature setting from their own config (Codex defaults to off).

Enclave isolates memory by config-store key. See [Agent Memory](../../../docs/runtime/stores.md#agent-memory)
for session reuse, `--no-memory`, ephemeral runs, and cleanup semantics.

To disable memories by default, set the `no_memory`
[config option](../../../docs/configuration.md) in the enclave project or global
config, the per-project equivalent of `--no-memory`, or add a Codex config patch:

```toml
[features]
memories = false
```

For finer control while the feature is enabled, the
[configuration reference](https://developers.openai.com/codex/config-reference)
documents `[memories]` settings including `generate_memories` (allow new chats
as generation inputs), `use_memories` (inject existing memories),
`disable_on_external_context`, and `max_unused_days`.

Codex consolidates eligible idle conversations in the background. Enclave
containers exit with the tool, terminating in-flight consolidation; memories
from one session typically appear during a later session using the same
store. Use `/memories` inside Codex to control memory use and generation for the
current conversation.

## Files

| File | Purpose |
|------|---------|
| `spec.yaml` | Extension manifest (metadata, sandbox behavior, network, credentials) |
| `install.sh` | Installs Codex via private agent npm (`enclave-install-npm-tool --ignore-scripts @openai/codex`) |
| `check-update.sh` | Returns the latest npm version for automatic update probes |
| `gateway-allowlist.conf` | DNS allowlist for network isolation |
| `templates/config.toml` | Default settings template |
| `entrypoint.d/setup.sh` | Runtime setup (creates config dir; in yolo mode pre-trusts the workspace) |
| `go/` | Custom Go hooks |
