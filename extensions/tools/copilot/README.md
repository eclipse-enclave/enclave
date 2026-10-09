# Copilot CLI

GitHub Copilot CLI is the command-line coding assistant for GitHub Copilot.

## Configuration

- **Command**: `copilot --disable-builtin-mcps`
- **YOLO flag**: `--yolo` (enabled by default)
- **Config directory**: `~/.copilot`
- **Settings file**: `~/.copilot/settings.json`
- **Skills directory**: `~/.copilot/skills`

The extension also passes through selected project-level Copilot configuration:
`agents/`, `copilot-instructions.md`, `extensions/`, `hooks/`, `instructions/`,
`lsp-config.json`, `mcp-config.json`, `providers.json`, `settings.json`, and
`skills/`.

## Authentication

| Variable | Purpose |
|----------|---------|
| `COPILOT_GITHUB_TOKEN` | GitHub token injected by the network gateway |

The provider declares `~/.copilot/config.json` as its auth file. Enclave uses
its presence to detect an existing auth session and includes it in auth
import/export. See [Authentication & Secrets](../../../docs/auth.md).

The gateway sends the declared token as a Bearer authorization header to
`api.github.com` and `*.githubcopilot.com`.

## Memory and Skills

The extension does not declare a native memory directory or memory-disable
arguments, so Enclave does not provide Copilot-specific project-scoped memory
handling. The extension's `enclave-help` skill is installed alongside managed
skills in `~/.copilot/skills`.

## Network Access

The Copilot service domains are `api.github.com` and `*.githubcopilot.com`.
The DNS allowlist also includes the common GitHub, npm, PyPI, Go, CDN, and
TLS/OCSP infrastructure fragments used by Enclave.

## Settings

The default settings template sets `remoteExport` and `autoUpdate` to `false`,
`includeCoAuthoredBy` and `storeTokenPlaintext` to `false`, `logLevel` to
`default`, and `model` to `auto`.

## Files

| File | Purpose |
|------|---------|
| `spec.yaml` | Extension manifest (launch behavior, settings, credentials, network, and provider auth) |
| `install.sh` | Installs `@github/copilot` and its `copilot` command |
| `check-update.sh` | Returns the latest npm package version for update probes |
| `gateway-allowlist.conf` | DNS allowlist for network isolation |
| `templates/settings.json` | Default Copilot settings template |
| `entrypoint.d/setup.sh` | Creates `~/.copilot` and sets `COPILOT_HOME` |
| `skills/enclave-help/SKILL.md` | Bundled skill for answering questions from Enclave's documentation |
| `go/handler.go` | Registers the Copilot tool handler |