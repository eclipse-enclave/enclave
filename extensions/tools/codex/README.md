# Codex

OpenAI Codex CLI is OpenAI's terminal-based AI coding agent.

## Configuration

- **Command**: `codex`
- **YOLO flag**: `--dangerously-bypass-approvals-and-sandbox`
- **Config directory**: `~/.codex`
- **Settings file**: `~/.codex/config.toml`

## Env Credentials

| Variable | Purpose |
|----------|---------|
| `CODEX_API_KEY`, `OPENAI_API_KEY` | Aliases for OpenAI API access |
| `CODEX_ACCESS_TOKEN` | `at-` personal access token |

With restricted networking, these values are replaced by gateway placeholders.
API keys are injected under both aliases, so `OPENAI_API_KEY` alone authenticates
`codex exec` without onboarding and takes precedence over a stored ChatGPT login
there. The interactive TUI (verified with 0.162.1) uses stored auth instead:
an empty store still requires onboarding, whose API-key entry prefills the
placeholder. Accepting it works only for the current session. Startup removes
regular local `auth.json` files containing `ENCLAVE_SECRET_` rather than reusing
an old session key; shared auth files are not removed. For persistent TUI use,
start with `--no-api-key` and sign in normally.
Conflicting alias values in the same winning layer are rejected.

The API-key model-list request to `chatgpt.com` can be denied when that host is
inspected; Codex falls back to its built-in model list. API keys are released
only to the declared OpenAI API hosts. See the
[authentication notes](../../../docs/auth.md#env-only-bearer-tokens).

Personal access tokens are released to `auth.openai.com` and `chatgpt.com`.
Agent Identity JWTs in `CODEX_ACCESS_TOKEN` are rejected while gateway protection
is active: their client-side signature verification cannot use placeholders.
Stored ChatGPT logins are outside this env-only protection. See
[authentication](../../../docs/auth.md#env-only-bearer-tokens).

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
- App-server daemon disabled

Model selection and reasoning effort are left at the Codex defaults; override
them via config patches if needed.

In yolo mode (`ENCLAVE_YOLO=1`), the project workspace is pre-trusted by
appending a `[projects."<dir>"]` table with `trust_level = "trusted"` to
`config.toml`, so Codex does not prompt for workspace trust inside the
already-sandboxed container.

An explicit user trust setting always wins; pre-trust applies only when no trust
is configured for the directory being opened.

### App-server daemon

By default, Codex runs its threads in a detached app-server daemon that updates
itself from OpenAI at runtime. Its binaries live under `~/.codex/packages/` in
the persistent config store, so later sessions keep running the downloaded
version even after the image is rebuilt. The container exits with the TUI, so
the daemon adds nothing here. The template sets
`features.daemon_auto_start = false`, so the TUI runs its app server in-process
instead. This default applies only to newly created config stores. Existing
stores keep their settings and downloaded daemon copies; template updates for
existing stores are tracked in [#159](https://github.com/eclipse-enclave/enclave/issues/159).
To disable the daemon in an existing store, set `daemon_auto_start = false`
under `[features]` with a config patch. Set it to `true` to opt in.

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
| `entrypoint.d/setup.sh` | Runtime setup (creates config dir; removes stale placeholder auth; in yolo mode pre-trusts the workspace) |
| `go/` | Custom Go hooks |
