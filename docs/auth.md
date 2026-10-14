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
`apiKey: false` on OAuth/provider token secrets so those tokens continue to
resolve normally. Feature-declared secrets that are not provider credentials also
continue to resolve normally. In `--ephemeral` mode, `--pass-api-key` controls
only API key secrets.

When persistence is enabled, resolved declared secret and `--pass-env` values
are stored in the host-side env store. Suppressed API key secrets are
skipped before resolution, so they are not persisted. For declared secrets with
`release.http`, the running tool sees placeholders, while the gateway receives
the real value from host-managed state. The gateway releases real values only in
the configured header on HTTPS requests to declared hosts; plaintext HTTP
requests carrying registered placeholders in headers are denied.
If a client checks token syntax before sending requests, extensions
can declare a [custom placeholder shape](extensions/README.md#client-side-credential-format-checks)
without exposing the real credential. Placeholder generation failures abort
startup instead of falling back to raw credentials.

### Env-only bearer tokens

With restricted networking, the following env credentials also use gateway
placeholders:

| Tool | Variable | Release hosts |
|------|----------|---------------|
| Claude | `CLAUDE_CODE_OAUTH_TOKEN` (`claude setup-token`) | `api.anthropic.com` |
| Pi | `ANTHROPIC_OAUTH_TOKEN` | `api.anthropic.com` |
| Codex | `CODEX_ACCESS_TOKEN` (`at-` personal access tokens only) | `auth.openai.com`, `chatgpt.com` |
| Codex | `CODEX_API_KEY` / `OPENAI_API_KEY` | `api.openai.com`, `*.openai.com` |

These bearer tokens have `apiKey: false`, so `--no-api-key` does not suppress
them. Codex API keys remain subject to `--no-api-key`.

Codex receives the resolved API key as both `CODEX_API_KEY` and `OPENAI_API_KEY`.
For `codex exec`, this bypasses onboarding and the env API key takes precedence
over a stored ChatGPT login. Codex's interactive TUI (verified with 0.162.1)
does not use `CODEX_API_KEY` for authentication: a stored login wins, and an
empty store still requires onboarding. Its API-key entry prefills the
`OPENAI_API_KEY` placeholder; accepting it authenticates only for that session.
For persistent interactive use, start with `--no-api-key` and sign in normally.
The two names are [aliases](#env-aliases): conflicting values in the same
winning layer fail rather than choosing one silently.

When using API-key auth, Codex also sends the key to
`chatgpt.com/backend-api/codex/models`.
That host is not an API-key release target: inspected requests are denied and
Codex falls back to its built-in model list. Inspection happens with
`--network-log=requests`, or in coarse mode when `CODEX_ACCESS_TOKEN` is also
injected. This does not prevent requests to the OpenAI API.

`CODEX_ACCESS_TOKEN` also accepts Agent Identity JWTs upstream. These require
client-side signature verification and cannot use a synthetic token. Enclave
rejects non-`at-` values when gateway protection is active; unrestricted
networking retains ordinary, unprotected env injection.

Placeholders are fresh each session. Before copying auth files to shared tool
or feature stores, Enclave warns and skips files containing a placeholder,
without overwriting valid shared credentials. Default placeholders and the
built-in shaped placeholders are recognizable across sessions; arbitrary
custom shapes are checked against the current session's registered values.
This guard does not prevent clients writing local files or writing directly
into mounted auth stores. At startup, tool auth files are relinked when the
shared file is non-empty; feature auth files can also be replaced by links to
empty shared files. The reported local content may therefore already be gone
when the warning is read. With an empty shared tool store, contaminated local
files normally remain. Codex is an exception: startup removes a regular local
`auth.json` containing `ENCLAVE_SECRET_` so an old TUI onboarding key cannot
cause permanent authentication failures. This cleanup also applies to project
auth scope, but never removes a shared auth symlink or its target.

Already-contaminated shared stores still require manual recovery. Inspect the
shared file and re-authenticate, or use `--reset-auth` to clear saved auth and
persisted env credentials before injecting fresh values.

File-backed and refreshable logins are separate from this env-only protection.
Enclave does not automatically reuse a Claude setup token in Pi, and it does
not recommend setting `ANTHROPIC_OAUTH_TOKEN` for Pi: whether Anthropic's terms
allow a subscription token in a third-party client is between you and
Anthropic. Enclave only protects the variable if you set it.

Codex OAuth note: the OAuth callback redirects to `http://localhost:1455/auth/callback`. enclave auto-maps port 1455 when no session exists. If you need to re-login, add `-p 1455`. Gateway logs will show `Loopback proxy (socat) enabled on port 1455` when forwarding is active.

Pi Anthropic OAuth note: Enclave does not auto-map the callback port. Add
`-p 53692` when starting a Pi session in which you intend to log in with
Anthropic OAuth.

OpenCode ChatGPT subscription note: for `opencode`, complete OpenAI browser auth from the CLI inside the container rather than from `/connect` in the TUI. Use:

```bash
enclave --tool opencode shell
opencode auth login --provider openai
```

Then select `ChatGPT Pro/Plus (browser)`, copy the printed URL, and open it in your host browser. `opencode` listens on `http://localhost:1455/auth/callback`, and enclave auto-maps port `1455` when the `openai` entry is missing from `auth.json`.

## Secrets

Layered secrets files are used as inputs for declared env secrets and `--pass-env`. They are not copied as-is into the container.

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

### Env Aliases

A declared secret may list several env-var aliases for one credential, for
example `GH_TOKEN` and `GITHUB_TOKEN`. Each alias is looked up on its own — host
environment first, then the layered secrets files from layer 3 down to layer 1,
then the host-side env store — and the alias found in the highest layer wins.
Its value is injected into *every* alias of that secret and written back to the
env store, so rotating a token in one alias is enough and a stale value in the
env store heals after one run.

If two aliases resolve from that same winning layer with different values, the
run fails and the error names the layer — enclave cannot pick between them.
Aliases that disagree from a lower layer are only logged, since the winning
value overrides them anyway.

## Passing Host Environment Variables

Host environment variables do **not** leak into the container unless explicitly opted in.

The exception is the time zone: `TZ` is set to the host zone (from `$TZ`, the `/etc/localtime` symlink, or `/etc/timezone`) so tool timestamps and git commits match the host. A `TZ` from the project `.env`, devcontainer `containerEnv`, or `--pass-env` takes precedence; set `TZ=UTC` there to keep UTC for one project, or set the `timezone` option (e.g. `"timezone": "UTC"` in global `config.json`) to replace the host zone everywhere. The zone roughly reveals the host's location to anything the agent can reach. Both backends ship the zone database.

**`--pass-env KEY1,KEY2`** forwards specific host environment variables into the container. It takes priority over layered secrets for the same key and is the escape hatch for values that are not declared as extension secrets.

```bash
enclave --pass-env GITHUB_TOKEN,MY_API_KEY
```

This can also be set persistently in `config.json` via the `pass_env` key. See [Configuration](configuration.md).

## SSH Keys

Giving an agent an SSH key is not recommended. The key lets the agent write to systems outside the sandbox, and Enclave cannot restrict how it is used. Let the agent commit in the container, review the diff, and push from the host instead.

Use `ssh-init` only for sessions that must push without host intervention:

```bash
enclave ssh-init
```

`ssh-init` stores keys in Enclave's host cache: `~/.cache/enclave/ssh/` by default on Linux, `$XDG_CACHE_HOME/enclave/ssh/` when set, and `~/Library/Caches/org.eclipse.enclave/ssh/` on macOS.

Docker mounts this directory read-only into every session, across all projects and tools. There is no per-session opt-out. Read-only access stops the agent from changing the key files, but it can still use the key for any operation allowed by the Git provider, including force pushes and branch deletion.

The generated key has no passphrase and is usable as soon as it is mounted.

Scope the key before you register it:

- Prefer a deploy key limited to one repository. On GitHub, use repository Settings > Deploy keys; on GitLab, use repository Settings > Repository > Deploy keys. Enable write access only if needed.
- For access to several repositories, use a dedicated machine account with minimal membership instead of a personal account.
- To revoke access, delete the key at the provider, then delete the host cache directory listed above.

You can replace the generated keys in the same directory. Do not use a key with broader access than the session needs.

Commit and tag signing are disabled inside the container (see [Persistence](persistence.md#git)), so you cannot distinguish an agent's pushes from your own by their signatures.
