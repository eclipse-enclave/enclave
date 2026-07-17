# Pi

Pi is a coding agent by Mario Zechner (`@earendil-works/pi-coding-agent`). It
supports multiple LLM providers (OpenAI, Anthropic, Google).

## Configuration

- **Command**: `pi`
- **YOLO flag**: `--approve`
- **Config directory**: `~/.pi`
- **Settings file**: `~/.pi/agent/settings.json`

## API Keys

| Variable | Purpose |
|----------|---------|
| `OPENAI_API_KEY` | OpenAI API access |
| `ANTHROPIC_API_KEY` | Anthropic API access |
| `GEMINI_API_KEY` | Google Gemini API access |

## Auth Files

- `agent/auth.json`

Pi uses Codex OAuth for OpenAI subscriptions. enclave auto-maps port 1455
when the `openai-codex` entry is missing from `agent/auth.json`; add `-p 1455`
(or `-p 1455:1455`) to re-login.

## Network Access

Allowlisted domains include OpenAI, Anthropic, Google, GitHub, and common
package registries (npm, PyPI, Go, CDNs, TLS/OCSP).

## Settings

Pi settings are seeded from `templates/settings.json` on first run. The upstream
Pi settings schema supports `defaultProjectTrust`, but the default template stays
empty so host settings passthrough behaves the same inside and outside Enclave.

Enclave runs Pi with `--approve` by default, so project-local Pi resources
are trusted inside the sandbox without showing Pi's project trust prompt. This
runtime override does not modify persisted Pi settings; use `--no-yolo` to omit
it.

## Files

| File | Purpose |
|------|---------|
| `spec.yaml` | Extension manifest (metadata, sandbox behavior, network, credentials) |
| `install.sh` | Installs Pi via private agent npm (`enclave-agent-npm-install @earendil-works/pi-coding-agent`) |
| `check-update.sh` | Returns the latest npm version for automatic update probes |
| `gateway-allowlist.conf` | DNS allowlist for network isolation |
| `templates/settings.json` | Default settings template |
| `entrypoint.d/setup.sh` | Runtime setup (creates config dir) |
