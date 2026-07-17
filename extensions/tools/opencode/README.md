# OpenCode

OpenCode is a terminal-based IDE with AI agent support. It supports multiple
LLM providers (OpenAI, Anthropic, Google).

## Configuration

- **Command**: `opencode`
- **YOLO flag**: *(none — runs in autonomous mode by default)*
- **Config directory**: `~/.config/opencode`
- **Settings file**: `~/.config/opencode/opencode.json`
- **OAuth store**: `~/.local/share/opencode/auth.json` (symlinked to `~/.config/opencode/auth.json` in Enclave)

## API Keys

| Variable | Purpose |
|----------|---------|
| `OPENAI_API_KEY` | OpenAI API access |
| `ANTHROPIC_API_KEY` | Anthropic API access |
| `GEMINI_API_KEY` | Google Gemini API access |

## Auth Files

- `auth.json`

OpenCode stores provider credentials in `auth.json`. ChatGPT Plus/Pro login for
the built-in OpenAI/Codex flow uses this file and requires callback port `1455`.
enclave auto-maps port `1455` when the `openai` entry is missing from
`auth.json`; add `-p 1455` (or `-p 1455:1455`) to re-login manually.

For OpenAI browser auth, currently use the CLI from a shell session instead of
`/connect` in the TUI:

```bash
enclave --tool opencode shell
opencode auth login --provider openai
```

Then choose `ChatGPT Pro/Plus (browser)`, copy the printed URL, and open it in
your host browser.

## Network Access

Allowlisted domains include OpenAI, Anthropic, Google, GitHub, and common
package registries (npm, PyPI, Go, Rust, CDNs, TLS/OCSP). Also allows
`opencode.ai` and `models.dev`.

## Settings

The default settings template disables sharing and OpenTelemetry.

## Files

| File | Purpose |
|------|---------|
| `spec.yaml` | Extension manifest (metadata, sandbox behavior, network, credentials) |
| `install.sh` | Installs OpenCode via private agent npm (`enclave-agent-npm-install opencode-ai`) |
| `check-update.sh` | Returns the latest npm version for automatic update probes |
| `gateway-allowlist.conf` | DNS allowlist for network isolation |
| `templates/settings.json` | Default settings template |
| `entrypoint.d/setup.sh` | Runtime setup (normalizes XDG data dir, links shared `auth.json`) |
