# Antigravity CLI

Google's Antigravity CLI (`agy`) is a terminal coding agent that runs on the
shared Antigravity agent engine. See the
[official docs](https://antigravity.google/docs/overview?tab=cli) and the
[`antigravity-cli`](https://github.com/google-antigravity/antigravity-cli)
repository.

## Configuration

- **Command**: `agy`
- **YOLO flag**: `--dangerously-skip-permissions` (enabled by default)
- **Continue**: `enclave continue` runs `agy --continue`. `agy` has no
  session-picker flag, so `enclave resume` falls back to `--continue`; use
  `/resume` inside the session to pick an older conversation.
- **Config directory**: `~/.gemini`
- **Settings file**: `~/.gemini/antigravity-cli/settings.json`
- **Skills directory**: `~/.gemini/config/skills`

`agy` splits its data across two directories under `~/.gemini`:

| Path | Content |
|------|---------|
| `config/` | Shared customization: `mcp_config.json`, `skills/`, `rules/`, `hooks.json`, `AGENTS.md`, `GEMINI.md`, `projects/` |
| `antigravity-cli/` | CLI data: `settings.json`, `keybindings.json`, OAuth token, conversations, logs, cache |

The whole `~/.gemini` directory is the tool's config store, so conversations
and settings persist per project. The extension passes through selected host
configuration: `antigravity-cli/settings.json`, `antigravity-cli/keybindings.json`,
and `config/` entries for `AGENTS.md`, `GEMINI.md`, `agents.json`, `agents/`,
`hooks.json`, `mcp_config.json`, `plugins.json`, `rules.json`, `rules/`,
`skills.json`, and `skills/`. Conversations, logs, caches, MCP OAuth tokens, and
the sign-in token are never passed through.

## Authentication

The default method is the Google sign-in. Because the container has no browser
or D-Bus keyring, `agy` prints an authorization URL, stores the token as a file
and asks you to paste the authorization code after signing in on the host.

- Declared auth file: `~/.gemini/antigravity-cli/antigravity-oauth-token`.
  Enclave uses its presence to detect an existing auth session and includes it
  in auth import/export. See [Authentication & Secrets](../../../docs/auth.md).
- The token is stored in plaintext in the auth store, like other file-based
  OAuth logins. Anyone who can read it can act with your Google account's
  Antigravity entitlement.
- `agy` selects file storage by detecting the container. Upstream tracks cases
  where a stored token is not picked up by a fresh process
  ([antigravity-cli#479](https://github.com/google-antigravity/antigravity-cli/issues/479));
  if you are asked to sign in every session, that is the cause.

### Gemini API key

| Variable | Purpose |
|----------|---------|
| `GEMINI_API_KEY` | Gemini API key injected by the network gateway into `x-goog-api-key` for `generativelanguage.googleapis.com` |

When `GEMINI_API_KEY` is set, `setup.sh` sets `"modelProvider": "gemini"` in
`settings.json`, which is what makes `agy` use the key instead of the Google
sign-in. Remove the key (or pass `--no-api-key`) to go back to the sign-in.

### Not covered

Application Default Credentials (`AGY_ADC_AUTH`), Workforce Identity
Federation, and custom endpoints (`GOOGLE_GEMINI_BASE_URL`) are not wired up by
this extension. Business sign-in reaches `aiplatform.googleapis.com`; regional
Vertex AI hosts need `--allow-domain`.

## Settings and Privacy

The default settings template sets `enableTelemetry` and `showFeedbackSurvey`
to `false`. The environment sets `AGY_CLI_DISABLE_AUTO_UPDATE=true`, since the
binary is installed into the image and is updated by rebuilding it.

In yolo mode, `setup.sh` also adds the project directory to `trustedWorkspaces`
in `settings.json` so `agy` does not show its workspace trust dialog for the
already-sandboxed project.

## Memory and Skills

The extension does not declare a native memory directory or memory-disable
arguments, so Enclave does not provide Antigravity-specific memory handling.
The extension's `enclave-help` skill is installed alongside managed skills in
`~/.gemini/config/skills`.

## Network Access

Allowlisted Antigravity and Google domains:

| Purpose | Domains |
|---------|---------|
| Product site and docs | `antigravity.google` |
| Sign-in | `accounts.google.com`, `oauth2.googleapis.com`, `www.googleapis.com` |
| Model and agent backends | `cloudcode-pa.googleapis.com`, `aicode.googleapis.com`, `businessaicode.googleapis.com` |
| Gemini API key | `generativelanguage.googleapis.com` (credential injected) |
| Google Cloud sign-in | `aiplatform.googleapis.com`, `sts.googleapis.com`, `iamcredentials.googleapis.com` |

The DNS allowlist also includes the common GitHub, npm, PyPI, Go, CDN, and
TLS/OCSP infrastructure fragments used by Enclave. The `agy` self-updater and
its download hosts are intentionally not allowlisted.

The terminal sandbox (`enableTerminalSandbox`, `--sandbox`) needs unprivileged
user namespaces, which are generally unavailable inside the container; Enclave's
own isolation applies instead.

## Files

| File | Purpose |
|------|---------|
| `spec.yaml` | Extension manifest (launch behavior, state paths, credentials, network, and provider auth) |
| `install.sh` | Installs `agy` with the official native installer into `~/.local/bin` |
| `check-update.sh` | Returns the latest `antigravity-cli` release tag for update probes |
| `gateway-allowlist.conf` | DNS allowlist for network isolation |
| `templates/settings.json` | Default Antigravity CLI settings template |
| `entrypoint.d/setup.sh` | Creates `~/.gemini`, enables the Gemini API provider, and pre-trusts the project in yolo mode |
| `skills/enclave-help/SKILL.md` | Bundled skill for answering questions from Enclave's documentation |
| `go/handler.go` | Registers the Antigravity tool handler |
