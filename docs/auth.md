# Authentication & Secrets

## Logging In

**The recommended approach is to simply log in from inside the container the first time you run.** Most AI coding agents support OAuth and will prompt you to authenticate on first launch. Your session is saved to a persistent auth store (a host directory under `~/.local/state/enclave/`) and reused automatically on every subsequent run — no host-side configuration needed.

```bash
enclave        # First run: log in from inside the agent
enclave        # Subsequent runs: session reused automatically
```

## Auth Stores

OAuth and session files are stored in host directories under
`~/.local/state/enclave/` (bind-mounted into the container; no Docker
volumes). By default, auth files are shared per tool across projects while tool
configuration remains per-project. Foreground exits, `exec` exits, background
stops, and GUI stop/remove actions finalize credentials into the shared auth
store.

```bash
--auth-scope=shared   # Shared per-tool auth store across all projects (default)
--auth-scope=project  # Per-project auth (kept in the project config store)
```

Use `--reset-auth` to clear saved auth and start fresh:

```bash
enclave --reset-auth   # Clears auth files + persisted env secrets, then injects fresh credentials
```

With shared auth scope, reset clears both the shared auth store and the current
project/session config store so stale local credentials cannot immediately
reseed shared auth.

### Named Auth Identities

By default each tool has a single shared auth store
(`~/.local/state/enclave/tools/<tool>/auth/default/`). `--auth-name <slug>`
selects a *separate* shared auth store per tool
(`.../tools/<tool>/auth/<slug>/`), so you can keep several credential sets
logged in at once and pick one per run without wiping the others:

```bash
enclave --tool codex --auth-name personal   # ChatGPT subscription login
enclave --tool codex --auth-name api        # API-key billing login
enclave --tool claude                        # Unnamed default identity (unchanged)
```

This covers two cases: keeping two subscriptions for the same tool logged in at
once, and holding a subscription login alongside API-key billing so you can pick
one per run. When the flag is unset, the default identity is used.

The slug is lowercased and must be 1 to 32 characters of `[a-z0-9]` with interior
hyphens (e.g. `personal`, `api-key`). Pin a default per project or globally with
the `auth_name` key in `config.json` (see [Configuration](configuration.md)).

`--auth-name` applies only under the default `--auth-scope=shared`; it is ignored
(with a warning) under `--auth-scope=project`, where auth already lives in the
per-project config store.

Use `--ephemeral` for a completely isolated run with no persistent stores:

```bash
enclave --ephemeral              # No auth or env stores
enclave --ephemeral --pass-api-key  # Ephemeral but inject API keys from env/secrets
```

## Auth Import / Export

Move host auth files into or out of the auth store:

```bash
enclave auth import --tool <tool>   # Copy host auth files into the auth store
enclave auth export --tool <tool>   # Copy auth store files back to the host
```

Both commands honor `--auth-scope` (`shared` or `project`) and `--auth-name`
(seed or extract a specific named identity's shared auth store).

**Host OAuth reuse** (only when the host agent is not running concurrently):

```bash
enclave auth import --tool claude   # One-time seed from host
enclave                             # Subsequent runs reuse the auth store
```

Avoid running the host agent and the container at the same time in this mode.

Claude concurrency note: in the default `shared` auth scope, enclave only
co-locates Claude's secure-storage directory by pointing
`CLAUDE_SECURESTORAGE_CONFIG_DIR` at the shared auth store. Claude itself reads
and writes `.credentials.json` and `.oauth_refresh.lock` there and handles its
native refresh coordination, including lock-based refresh serialization,
mtime-based credential re-reads, and adopting a peer's freshly rotated token.
Because all concurrent containers use the same Claude-managed store, parallel,
long-running sessions keep working after a token refresh. Use
`--auth-scope=project` to opt out into per-project isolated credentials (no
cross-session coordination).

## API Keys

Provider credentials follow the same layered resolution as other secrets (see
below). Use `--no-api-key` to suppress provider credential secrets that are API
keys. Provider credential secrets default to API keys; profiles can set
`api_key: false` on OAuth/provider token secrets so those tokens continue to
resolve normally. Feature-declared secrets that are not provider credentials also
continue to resolve normally. In `--ephemeral` mode, `--pass-api-key` controls
only API key secrets.

When persistence is enabled, resolved declared secret and `--pass-env` values
are stored in the host-side env store. Suppressed API key secrets are
skipped before resolution, so they are not persisted. For declared secrets with
`release.http`, the running tool sees placeholders, while the gateway receives
the real value from host-managed state. The gateway releases real values only on
HTTPS requests to declared hosts; plaintext HTTP requests carrying placeholders
are denied.

Codex OAuth note: the OAuth callback redirects to `http://localhost:1455/auth/callback`. enclave auto-maps port 1455 when no session exists. If you need to re-login, add `-p 1455`. Gateway logs will show `Loopback proxy (socat) enabled on port 1455` when forwarding is active.

OpenCode ChatGPT subscription note: for `opencode`, complete OpenAI browser auth from the CLI inside the container rather than from `/connect` in the TUI. Use:

```bash
enclave --tool opencode shell
opencode auth login --provider openai
```

Then select `ChatGPT Pro/Plus (browser)`, copy the printed URL, and open it in your host browser. `opencode` listens on `http://localhost:1455/auth/callback`, and enclave auto-maps port `1455` when the `openai` entry is missing from `auth.json`.

## Secrets

Layered secrets files are used as inputs for declared env secrets and `--pass-env`. They are not blindly copied into the container.

| Layer | File | Scope |
|-------|------|-------|
| 1 | `~/.local/state/enclave/secrets/global.env` | All agents, all projects |
| 2 | `~/.local/state/enclave/secrets/global/<tool>.env` | Specific agent, all projects |
| 3 | `~/.local/state/enclave/secrets/projects/<hash>/<tool>.env` | Specific agent, specific project |

Later layers override earlier ones. File format: standard `.env` (`KEY=VALUE`, `#` comments, `export` prefix, single/double quotes).

Declared secrets from the selected tool profile and enabled feature manifests use this same resolution. Placing a value in `global.env` (layer 1) is sufficient as long as the secret is declared by the active extensions.

**`--secrets-scope`** controls which per-tool layers are read (`both` by default):

- `both` — layers 1–3 (global shared + global per-tool + project per-tool)
- `global` — layers 1–2 only
- `project` — layers 1 and 3 only

Layer 1 (`global.env`) is always read regardless of scope.

**Example:** declared Anthropic credential globally, project override for the same tool:

```bash
# All runs of the tool can use this credential
echo 'ANTHROPIC_API_KEY=sk-ant-readonly...' > ~/.local/state/enclave/secrets/global.env

# This project overrides it for one repository
echo 'ANTHROPIC_API_KEY=sk-ant-project...' > ~/.local/state/enclave/secrets/projects/<hash>/claude.env
```

## Passing Host Environment Variables

Host environment variables do **not** leak into the container unless explicitly opted in.

**`--pass-env KEY1,KEY2`** forwards specific host environment variables into the container. It takes priority over layered secrets for the same key and is the escape hatch for values that are not declared as extension secrets.

```bash
enclave --pass-env GITHUB_TOKEN,MY_API_KEY
```

This can also be set persistently in `config.json` via the `pass_env` key. See [Configuration](configuration.md).

## SSH Keys

Initialize isolated SSH keys for use inside containers:

```bash
enclave ssh-init
```

Keys are generated at `~/.cache/enclave/ssh/` and mounted into the container read-only.
