# Authentication and secrets

How agents log in, where credentials are stored, and how API keys, secrets
files, host environment variables, and SSH keys reach a session.

## Log in from the agent

Log in inside the container on the first run; most agents prompt for OAuth on
first launch. Enclave saves the login in a persistent auth store on the host and
reuses it on later runs, with no host-side setup. Some OAuth flows need a
callback port; see [OAuth callback ports](#oauth-callback-ports).

## Auth stores

Auth files live in host directories under `~/.local/state/enclave/`,
bind-mounted into the container (no Docker volumes). Store internals are in
[Persistent stores](runtime/stores.md).

| Flag | Where auth lives |
|------|------------------|
| `--auth-scope=shared` (default) | One store per tool, shared by all projects; tool configuration stays per project |
| `--auth-scope=project` | The project's config store |

- Credentials are written back to the shared store on foreground exit, `exec`
  exit, background stop, and GUI stop or remove.
- `--reset-auth` clears saved auth and persisted env secrets, then injects fresh
  credentials. With shared scope it clears both the shared store and the
  current project or session config store, so stale local credentials cannot
  reseed shared auth.
- `--ephemeral` uses no persistent store at all;
  `--ephemeral --pass-api-key` still injects API keys from env and secrets
  files.

### Named auth identities

`--auth-name <slug>` selects a separate shared store,
`~/.local/state/enclave/tools/<tool>/auth/<slug>/`, instead of `.../default/`.
Use it to keep several logins for one tool, such as two subscriptions or a
subscription and API-key billing, and pick one per run:

```bash
enclave --tool codex --auth-name personal   # ChatGPT subscription login
enclave --tool codex --auth-name api        # API-key billing login
enclave --tool claude                       # Default identity
```

- The slug is lowercased: 1 to 32 characters of `[a-z0-9]` with interior
  hyphens.
- Set a default per project or globally with the `auth_name` key in
  `config.json` (see [Configuration](configuration.md)).
- Ignored with a warning under `--auth-scope=project`.

## Import and export host logins

Enclave reconciles auth files between the config store and the shared store by
itself. These commands only move credentials between the host tool's native
config and Enclave's stores:

```bash
enclave auth import --tool claude   # Seed the store from a host login, once
enclave auth export --tool <tool>   # Copy store files back to the host
```

`--tool` is required. `--auth-scope` and `--auth-name` select the store as for
a session (`--auth-name` has no effect with project scope). Declared env
secrets and the env store are not touched. Do not run the host tool on the same
credentials while importing or while a container uses them.

### Claude and concurrent sessions

With shared scope, Enclave points `CLAUDE_SECURESTORAGE_CONFIG_DIR` at the
shared store. Claude keeps `.credentials.json` and `.oauth_refresh.lock` there
and coordinates refreshes itself (lock-serialized refresh, mtime-based
re-reads, adopting a token another session rotated), so parallel long-running
sessions keep working after a refresh. `--auth-scope=project` opts out into
isolated credentials without this coordination.

## OAuth callback ports

| Tool | Callback | Mapping |
|------|----------|---------|
| Codex | `localhost:1455/auth/callback` | Automatic when no session exists; add `-p 1455` to log in again. The gateway log shows `DNAT port 1455 to loopback listener` while active, or `Loopback proxy (socat) enabled on port 1455` when DNAT is unavailable |
| OpenCode (ChatGPT Pro/Plus) | `localhost:1455/auth/callback` | Automatic when `auth.json` has no `openai` entry |
| Pi (ChatGPT/Codex OAuth) | port 1455 | Automatic when `auth.json` has no `openai-codex` entry; add `-p 1455` to log in again |
| Pi (Anthropic OAuth) | port 53692 | Not automatic; add `-p 53692` |

For OpenCode, log in from the CLI rather than `/connect` in the TUI, select
`ChatGPT Pro/Plus (browser)`, and open the printed URL in the host browser:

```bash
enclave --tool opencode shell
opencode auth login --provider openai
```

## API keys

Provider credentials are declared by extensions and resolved like other
[secrets](#secrets-files).

- `--no-api-key` withholds provider credentials that are API keys. Credentials
  marked `apiKey: false` (OAuth and provider tokens) and feature secrets that
  are not provider credentials still resolve.
- In `--ephemeral` mode, `--pass-api-key` controls only API key secrets.
- With persistence on, resolved declared secrets and `--pass-env` values are
  saved in the host-side env store. Withheld API keys are skipped before
  resolution and never saved.

With restricted networking, credentials that an extension routes through the
gateway reach the tool only as placeholders; the gateway inserts the real value
in one header on HTTPS requests to the declared hosts and denies plaintext HTTP
requests that carry a placeholder. If a placeholder cannot be generated,
startup fails instead of falling back to the real value. Extension authors
find the rules, including
[custom placeholder shapes](extensions/credentials.md#client-side-credential-format-checks),
in [Placeholders](extensions/credentials.md#placeholders); the request path is
in [Network request flow](runtime/network-request-flow.md).

### Env-only bearer tokens

These env credentials use gateway placeholders under restricted networking:

| Tool | Variable | Release hosts |
|------|----------|---------------|
| Claude | `CLAUDE_CODE_OAUTH_TOKEN` (`claude setup-token`) | `api.anthropic.com` |
| Pi | `ANTHROPIC_OAUTH_TOKEN` | `api.anthropic.com` |
| Codex | `CODEX_ACCESS_TOKEN` (`at-` personal access tokens only) | `auth.openai.com`, `chatgpt.com` |
| Codex | `CODEX_API_KEY` / `OPENAI_API_KEY` | `api.openai.com`, `*.openai.com` |

- The bearer tokens are `apiKey: false`, so `--no-api-key` does not withhold
  them; Codex API keys are withheld.
- `CODEX_API_KEY` and `OPENAI_API_KEY` are [aliases](#env-aliases) and get the
  same value. The key authenticates `codex exec` without onboarding; the TUI
  uses stored auth, so for persistent TUI use start with `--no-api-key` and
  sign in.
  See the [Codex README](../extensions/tools/codex/README.md#env-credentials).
- An inspected request that sends a placeholder to a host outside its release
  list is denied; HTTPS to hosts the gateway does not inspect is tunneled
  unchanged ([request flow](runtime/network-request-flow.md#flow-summary)).
  Codex's API-key model-list request to `chatgpt.com` is one such denial;
  Codex falls back to its built-in model list.
- `CODEX_ACCESS_TOKEN` must start with `at-` while gateway protection is active:
  Agent Identity JWTs need client-side signature checks and cannot use a
  placeholder. The startup error names the source; `--reset-auth` clears it
  from the persisted env store.
- File-backed and refreshable logins are not protected. Enclave does not reuse
  a Claude setup token in Pi; see the
  [Pi README](../extensions/tools/pi/README.md#env-credentials).
- Placeholders change every session. Auth files containing one are not copied
  to shared stores (Enclave warns), and Codex startup removes a regular local
  `auth.json` containing `ENCLAVE_SECRET_`; shared auth symlinks and their
  targets are never removed. A shared store that already holds a placeholder
  needs a new login or `--reset-auth`.

## Secrets files

Layered `.env` files supply values for declared env secrets and `--pass-env`.
They are never copied into the container as files.

| Layer | File | Scope |
|-------|------|-------|
| 1 | `~/.local/state/enclave/secrets/global.env` | All agents, all projects |
| 2 | `~/.local/state/enclave/secrets/global/<tool>.env` | One agent, all projects |
| 3 | `~/.local/state/enclave/secrets/projects/<hash>/<tool>.env` | One agent, one project |

Later layers override earlier ones. The format is standard `.env` (`KEY=VALUE`,
`#` comments, optional `export`, single or double quotes). A value in
`global.env` is enough as long as an active extension declares the secret.

`--secrets-scope` selects the per-tool layers: `both` (default, 1–3),
`global` (1–2), or `project` (1 and 3). Layer 1 is always read.

```bash
# All runs of the tool can use this credential
echo 'ANTHROPIC_API_KEY=sk-ant-readonly...' > ~/.local/state/enclave/secrets/global.env

# This project overrides it for one repository
echo 'ANTHROPIC_API_KEY=sk-ant-project...' > ~/.local/state/enclave/secrets/projects/<hash>/claude.env
```

### Env aliases

A declared secret can list several aliases for one credential, such as
`GH_TOKEN` and `GITHUB_TOKEN`. Each alias is looked up on its own: host
environment, then secrets layers 3 to 1, then the env store. The alias found in
the highest layer wins, and its value is injected into every alias and written
back to the env store, so rotating one alias is enough and a stale stored value
is replaced after one run. Two aliases with different values in the winning
layer fail the run, naming the layer; disagreement in a lower layer is only
logged.

## Host environment variables

Host variables reach the container only when you opt in.
`--pass-env KEY1,KEY2` forwards them and takes priority over the secrets files
for the same key:

```bash
enclave --pass-env GITHUB_TOKEN,MY_API_KEY
```

- The `pass_env` config key sets it persistently. On the command line,
  `--pass-env` replaces that list for the run and `--pass-env +KEY` adds to it
  (see [List options](configuration.md#list-options)).
- A secret withheld by `--no-api-key`, or by `--ephemeral` without
  `--pass-api-key`, is forwarded only if `--pass-env` names it on the command
  line (`+KEY` keeps the configured list). A `pass_env` config entry for it is
  skipped with a warning.

### Time zone

`TZ` is set to the host zone, read from `$TZ`, the `/etc/localtime` symlink, or
`/etc/timezone`, so timestamps and Git commits match the host. Both backends
ship the zone database.

- A `TZ` from the project `.env`, devcontainer `containerEnv`, or `--pass-env`
  wins; set `TZ=UTC` there for one project.
- The `timezone` option (for example `"timezone": "UTC"` in global
  `config.json`) replaces the host zone everywhere.
- The zone roughly reveals the host's location to anything the agent can reach.

## SSH keys

> [!WARNING]
> Giving an agent an SSH key is not recommended. The key lets the agent write
> to systems outside the sandbox, and Enclave cannot restrict how it is used.
> Let the agent commit in the container, review the diff, and push from the
> host.

For sessions that must push without you, run `enclave ssh-init`:

- Keys are stored in `~/.cache/enclave/ssh/` on Linux
  (`$XDG_CACHE_HOME/enclave/ssh/` when set) and
  `~/Library/Caches/org.eclipse.enclave/ssh/` on macOS. You can replace them
  there.
- The directory is mounted read-only into every session of every project and
  tool, with no per-session opt-out. Read-only stops edits to the key files, not
  force pushes or branch deletion at the Git provider.
- The generated key has no passphrase and works as soon as it is mounted.

Scope the key before registering it:

- Prefer a deploy key limited to one repository, with write access only if
  needed (GitHub: repository Settings > Deploy keys; GitLab: Settings >
  Repository > Deploy keys). Never use a key with broader access than the
  session needs.
- For several repositories, use a dedicated machine account with minimal
  membership, not a personal account.
- To revoke, delete the key at the provider, then the host directory above.

Commit and tag signing are disabled in the container (see
[Git](git.md#signing)), so signatures cannot tell an agent's pushes from yours.
SSH on port 22 bypasses the gateway proxy and is not logged.

## Protected native auth (unavailable)

`--protect-auth` is reserved, not an available option. Ordinary sessions still
receive native auth files. The internal Claude implementation and remaining
activation gates are described in [Persistent stores](runtime/stores.md#internal-native-auth-exchanges).
