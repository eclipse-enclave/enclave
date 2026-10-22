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

### Recommended: fine-grained PAT via `COPILOT_GITHUB_TOKEN`

Prefer `COPILOT_GITHUB_TOKEN` with a fine-grained personal access token that
only grants the **Copilot Requests** permission. This is the minimum Copilot CLI
needs and the approach recommended by the Copilot CLI maintainers
([github/copilot-cli#953](https://github.com/github/copilot-cli/issues/953)).

### OAuth login (`/login`) grants broad scopes

> **Warning:** The device-code and browser OAuth login (`/login`) issues a token
> with far more scopes than Copilot needs. Observed token scopes:
> `codespace`, `gist`, `read:org`, `read:user`, `repo` (read and write), and
> `write:plugin_gateway_connections`.

A token with `repo` write access can read and modify private repositories, so
anyone who obtains the stored OAuth credentials (under
`~/.local/state/enclave/tools/copilot/`) can act on your GitHub account beyond
Copilot. Use OAuth only when you accept this risk; otherwise use a fine-grained
PAT as described above.

## Memory and Skills

Copilot Memory is **disabled by default** (`"memory": false` in the settings
template). Copilot stores memories on GitHub's servers, not in a local memory
directory. Account-scoped memories can be retrieved by later sessions in other
projects, other Enclave state roots, or Copilot outside Enclave. This can expose
project information across those boundaries and let one project's agent
influence another through shared memories. In YOLO mode, an agent can store
memories without a confirmation prompt.

Enclave's per-project memory isolation applies to local files and cannot isolate
this server-side memory. The extension therefore does not declare a native
memory directory or memory-disable arguments; `--no-memory` is not a control for
Copilot's server-side memory.

If you accept this cross-project sharing, run `/memory on` inside a Copilot
session to enable it, or `/memory off` to disable it again. When an Enclave config
override or patch is active, the changed setting carries forward in that
project's config store. See [Tool Config Patches](../../../docs/configuration.md#tool-config-patches)
for settings persistence and precedence.

For a persistent opt-in, add `"memory": true` to a
[settings patch](../../../docs/configuration.md#tool-config-patches), for example
`~/.config/enclave/patches/copilot/settings.json` on Linux:

```json
{
  "memory": true
}
```

This global patch enables memory for all projects unless overridden by a
project-specific setting. It does not make the stored memories project-scoped.

The extension's `enclave-help` skill is installed alongside managed skills in
`~/.copilot/skills`.

## Network Access

The Copilot service domains are `api.github.com` and `*.githubcopilot.com`.
The DNS allowlist also includes the common GitHub, npm, PyPI, Go, CDN, and
TLS/OCSP infrastructure fragments used by Enclave.

## Settings

The default settings template sets `remoteExport` and `autoUpdate` to `false`,
`includeCoAuthoredBy`, `storeTokenPlaintext`, and `memory` to `false`,
`logLevel` to `default`, and `model` to `auto`. See [Memory and Skills](#memory-and-skills)
before enabling memory.

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