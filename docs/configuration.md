# Configuration

Where Enclave reads configuration, how layers combine, every config key, list
syntax, and environment variables. Agent-native config (settings, skills,
patches) is in [Tool config](tool-config.md).

## Config files

| File | Scope |
|------|-------|
| `~/.config/enclave/config.json` | Global defaults |
| `~/.config/enclave/projects/<hash>/config.json` | One project, keyed by project hash |

Project config lives outside the worktree, so neither the repository nor a
session can change its own isolation policy. Since only you edit it, it may set
every option global config can, except:

| Option | Why |
|--------|-----|
| `auto_update`, `update_channel` | Global-only because they replace the host executable; project and tool-override values are ignored with a warning |
| `disabled_commands` | Global-only because commands resolve before the project is known |
| Top-level `host_config_paths` | Ignored with a warning in any file; set it under `tool_overrides.<tool>` |
| Opt-ins without a config key | See [CLI-only options](#cli-only-options) |

Extensions come from the built-in `extensions/` tree and
`~/.config/enclave/extensions/`; see
[Installing extensions](extensions/installing.md).

## Host directories

Paths in these docs use the Linux layout.

| Root | Linux | macOS |
|------|-------|-------|
| Config | `~/.config/enclave/` (`$XDG_CONFIG_HOME`) | `~/Library/Application Support/org.eclipse.enclave/config/` |
| State | `~/.local/state/enclave/` (`$XDG_STATE_HOME`) | `~/Library/Application Support/org.eclipse.enclave/state/` |
| Cache | `~/.cache/enclave/` (`$XDG_CACHE_HOME`) | `~/Library/Caches/org.eclipse.enclave/` |

`XDG_*` overrides are ignored on macOS. Contents:
[Data on the host](persistence.md#data-on-the-host).

## Precedence

Highest to lowest:

1. CLI flags
2. Selected tool override (`tool_overrides.<tool>`)
3. Project config
4. Global config
5. Built-in defaults

A tool override beats a top-level value in either file, so a global
`tool_overrides.<tool>` value beats a project's top-level value; between the
two files' overrides, the project's wins. Lists merge across layers (see
[List options](#list-options)); string maps such as `host_env` merge per key.
`enclave config --view source` shows where each value came from.

### CLI-only options

Never read from config files, so no config can run Enclave as root or mount all
sensitive data:

| Flag | Environment alternative |
|------|-------------------------|
| `--rebuild`, `--force-base-image`, `--name` | None |
| `--no-rebuild` | `ENCLAVE_NO_REBUILD=1`; see [Suppressing rebuilds](tools.md#suppressing-rebuilds) |
| `--build-uid`, `--build-gid`, `--runtime-uid-remap`, `--buildx-cache-*` | None |
| `--allow-root` | `ENCLAVE_ALLOW_ROOT=1`; see [Running as root](install.md#running-as-root) |
| `--allow-sensitive-mounts` | `ENCLAVE_ALLOW_SENSITIVE_MOUNTS=1`; see [Mounts](mounts.md#mounting-all-sensitive-paths) |
| `--add-sensitive-dir`, `--add-readonly-sensitive-dir` | None; config can exempt paths with `sensitive_dir_exceptions` |

## Config keys

```json
{
  "tool": "codex",
  "project_mount": "readonly",
  "secrets_scope": "project",
  "pass_env": ["GITHUB_TOKEN"],
  "allow_all_network": false
}
```

Every key except `tool` and the global-only ones can also be set under
`tool_overrides.<tool>`. An override that sets `tool` or a nested
`tool_overrides` is ignored as a whole with a warning.

### Global only

| Key | Description |
|-----|-------------|
| `auto_update` | Replace a stale standalone binary with the latest build of its channel during the daily check (default `false`; package and source builds ignore it). See [Updating](install.md#updating) |
| `update_channel` | `rolling` (builds of `main`) or `stable` (tagged releases). Unset follows the channel the binary was published on |
| `disabled_commands` | Command names that never become `enclave <name>` verbs, from any provider; the name then fails as unknown instead of falling through. Literal names, no `+`/`-`/`none` syntax; built-in verbs cannot be disabled. See [Custom commands](commands.md#where-commands-come-from) |

### Tool and session

| Key | Description |
|-----|-------------|
| `tool` | Default tool; unset means the first interactive session asks once and saves it ([Tool selection](cli-reference.md#tool-selection)) |
| `backend` | `auto` (default), `docker`, `podman`, or experimental `qemu`; see [Backends](backends.md) |
| `yolo` | YOLO mode (default `true`). Only consulted when the tool profile leaves `yoloEnabled` unset; every bundled CLI agent sets it, so use `--yolo`/`--no-yolo` |
| `host_config` | `none` (default) or `passthrough`; see [Host config passthrough](tool-config.md#host-config-passthrough) |
| `tool_overrides.<tool>.host_config_paths` | Passthrough path directives (`default`, `+path`, `-path`, or a list) |
| `skills_validation` | `strict` (default) or `agent`; see [Managed skills](tool-config.md#validation) |
| `session_tint` | `#rrggbb` background for session terminals (unset: none); see [Session tint](#session-tint) |
| `host_env` | Env vars for the host process running the session; see [Host environment](#host-environment) |
| `timezone` | Container `TZ`, e.g. `UTC` (unset: host zone); see [Time zone](auth.md#time-zone) |
| `verbose` | Verbose logging |

### Auth and secrets

See [Authentication and secrets](auth.md).

| Key | Description |
|-----|-------------|
| `ephemeral` | No persistent auth/env stores |
| `protect_auth` | Opt-in [native Claude credential protection](auth.md#protected-native-auth); default `false`, requires restricted networking |
| `auth_scope` | `shared` (default) or `project` |
| `auth_name` | Named per-tool shared auth identity; unset uses the default |
| `secrets_scope` | `both` (default), `global`, or `project` |
| `reset_auth` | Clear auth on start |
| `no_api_key` | Disable API key injection |
| `pass_api_key` | Allow API keys in ephemeral mode |
| `pass_env` | Host env vars to forward |

### Network

See [Networking](networking.md).

| Key | Description |
|-----|-------------|
| `allow_all_network` | Disable network restrictions; a gateway still logs HTTP/HTTPS unless `disable_gateway` is set |
| `disable_gateway` | Run without a network gateway, so no network log is written; requires unrestricted network mode, otherwise the run fails |
| `allow_domains` | Extra allowlisted domains (bare DNS names; ignored with `allow_all_network`) |
| `network_log` | `coarse` (default), `requests`, or `full` (`requests` plus stored headers, bodies, and WebSocket messages in restricted mode; sensitive, see [Full capture](networking.md#full-capture)); see [Coverage and granularity](networking.md#coverage-and-granularity) |
| `ports` | Publish container ports to the host |
| `bridge_ports` | Forward host ports into the container |

### Persistence, mounts, image

| Key | Description |
|-----|-------------|
| `no_cache`, `no_history` | Disable package caches or shell history |
| `no_memory` | Disable agent memory; see [Agent memory](runtime/stores.md#agent-memory) |
| `project_mount` | `writable` (default) or `readonly`; see [Mounts](mounts.md) |
| `worktree_metadata` | `follow` (default), `readonly`, or `none` |
| `add_dirs`, `add_readonly_dirs` | Additional directories to mount (read-write or read-only) |
| `sensitive_dir_exceptions` | Sensitive paths allowed to be mounted; mounts nothing |
| `base_image` | Base image override; see [Tools and images](tools.md) |
| `devcontainer` | Derive the base image from devcontainer.json |
| `slim` | Build without features |
| `features` | Features to enable; see [Selections](#selections) |
| `use_remote_user` | Honor devcontainer `remoteUser` for agent sessions |
| `image_name` | Override the image name/tag |
| `cache_from` | Inline build cache sources |
| `progress` | Build output style: `quiet`, `compact` (default), or `verbose` |

### Session tint

`session_tint` sets the terminal background while `run` (and `shell`,
`continue`, `resume`), `exec`, or `attach` owns the terminal, and resets it on
exit. Inside tmux 3.3+ only the pane is tinted.

- Pick a color that keeps your text readable; Enclave does not adjust it.
  Invalid values are reported and ignored.
- Per-tool and per-project layers apply. `attach` and `exec --name` use the
  color of the session's tool and project, not of the current directory.
- Skipped when stdout is not a terminal, or with `NO_COLOR` or
  `ENCLAVE_COLOR=never`; `ENCLAVE_COLOR=always` does not enable it.
- Agents that paint their own background can cover it.
- The reset restores the terminal's configured default, so a color set at
  runtime by a theme switcher is lost. After `SIGKILL` no reset runs; clear it
  with `printf '\e]111\a'`.

### Host environment

`host_env` sets variables on the host process running an agent session, so
tools that inspect a terminal's foreground processes can recognize the agent.
For example, [Herdr](https://herdr.dev) reads `HERDR_AGENT`:

```json
{
  "tool_overrides": {
    "claude": { "host_env": { "HERDR_AGENT": "claude" } },
    "codex": { "host_env": { "HERDR_AGENT": "codex" } }
  }
}
```

- Receives them: the `docker` client (or qemu) of `run`, `continue`, `resume`,
  and `attach`, and what it spawns (e.g. podman's `conmon` and cleanup).
- Does not: the container (use `pass_env`), Enclave's separate setup and
  teardown engine calls, `shell`, and `exec`. `attach` to a foreground `shell`
  session still applies it, since sessions do not record that.
- `attach` resolves the keys of the session it enters.
- A later layer replaces a key's value but cannot remove it; `""` sets empty.
- Ignored with a warning: invalid names; `HOME`, `PATH`, `DOCKER_*`,
  `CONTAINER_*`, `CONTAINERS_*`, `XDG_*` (a non-exhaustive denylist of engine
  settings); and `HTTP_PROXY`, `HTTPS_PROXY`, `FTP_PROXY`, `NO_PROXY` in any
  case, which podman would copy into the container.

> [!WARNING]
> `enclave config` prints `host_env` values; do not put secrets there.

## List options

Each layer (global, project, global then project tool override, CLI) merges
onto the list it inherits. `disabled_commands` is not a list option.

### Extras

`add_dirs`, `add_readonly_dirs`, `sensitive_dir_exceptions`, `ports`,
`allow_domains`, `bridge_ports`, `pass_env`, and `cache_from`, in config and on
the CLI:

| Entry | Effect |
|-------|--------|
| `x` | Replaces the inherited list |
| `+x` | Adds |
| `-x` | Removes |
| `[]`, `["none"]`, `--flag none` | Clears |

A list is all plain or all `+`/`-`; mixing, or `none` with other entries, is an
error. Exception: on the CLI, a plain `--add-dir`/`--add-readonly-dir` path
adds; `-dir` removes and `none` makes the other paths replace.

```json
// Global config:
{ "ports": ["8080:80"], "add_dirs": ["~/shared"] }

// Project config:
{ "ports": ["+3000"], "add_dirs": ["-~/shared", "+./data"] }

// Result: ports ["8080:80", "3000"], add_dirs ["./data"]
```

- Removing a missing entry is silent.
- One prefix is stripped: write `./+dir`, `./-dir`, `./none` for such names;
  `++x` adds `+x`. If a directory is missing but `+<name>` exists, the error
  suggests `./+x`.
- Entries compare normalized: `3000`, `3000:3000`, and `127.0.0.1:3000:3000`
  are one port; domains are case-insensitive; directories compare resolved.
- Ports clash on the same host port with overlapping addresses (omitted means
  `127.0.0.1`; `0.0.0.0` and `::` overlap all; host port `0` never clashes).
  Within a layer that is an error; across layers the more specific entry wins,
  so `-p 8080:81` overrides a configured `8080:80`.
- Invalid entries, removals included, name the flag or key and file, e.g.
  `tool_overrides.claude.pass_env in ~/.config/enclave/config.json`.

### Selections

`features` and `host_config_paths`:

```json
// Global config:
{ "features": ["node-dev", "python-dev"] }

// Project config:
{ "features": ["+devtools", "-python-dev"] }

// Result: ["node-dev", "devtools"]
```

- Any bare entry (name or keyword) replaces the inherited list; `[]` selects
  nothing.
- Only `+`/`-` entries amend it, and the more specific layer wins per item (a
  project `+x` restores an `x` removed globally). Same on the CLI:
  `--features +x` adds, `--features a,b` replaces.
- If no layer selects features, directives amend the default-enabled set:
  `["-node-dev"]` drops a default feature, `["+gitlab-cli"]` adds an opt-in
  one. In devcontainer mode that set is empty.
- `--features none` (in config `[]`) selects nothing and takes no other values.

Feature names must match an installed extension. An unknown name, including
`+name`, fails before building, even with `--no-rebuild`. Unknown `-name`
removals, unknown names from config that `--slim` or `--backend qemu` discard,
and unknown names in `network print`/`status`/`diff` (previewed without them)
only warn.
Messages name the flag or key and file; `enclave features list` shows the
names. `host_config_paths` resolution:
[Host config passthrough](tool-config.md#host-config-passthrough).

## Environment variables

| Variable | Description |
|----------|-------------|
| `ENCLAVE_HOME` | Point asset discovery at a specific repo checkout |
| `ENCLAVE_LOG_LEVEL` | `info` (default) or `debug` |
| `ENCLAVE_QEMU_CPU`, `ENCLAVE_QEMU_TRANSPORT`, `ENCLAVE_QEMU_KERNEL_APPEND_EXTRA` | QEMU backend tuning; see [Backends](backends.md#tuning-variables) |
| `ENCLAVE_AGENT_UPDATE_INTERVAL_HOURS` | Hours after a tool's last automatic update before `check-update.sh` may probe again (`0` = always) |
| `ENCLAVE_NO_REBUILD` | `1` suppresses session image/bundle builds and update probes; see [Suppressing rebuilds](tools.md#suppressing-rebuilds) |
| `ENCLAVE_DEVCONTAINER_REWRITE_VARS` | Extra env var names (comma-separated) for devcontainer home-path normalization |
| `ENCLAVE_ALLOW_ROOT` | `1` = `--allow-root` |
| `ENCLAVE_ALLOW_SENSITIVE_MOUNTS` | `1` = `--allow-sensitive-mounts` |
| `ENCLAVE_COLOR` | `always` forces color, `never` disables it; otherwise only on a terminal. A non-empty `NO_COLOR` wins |

Windows launcher variables (`ENCLAVE_WSL_*`):
[Windows](windows.md#environment-variables).

## Inspecting resolved config

```bash
enclave config                   # All layers side by side (matrix)
enclave config --view source     # Where each value came from
enclave config --view diff       # Values overridden by higher precedence
enclave config --view effective  # Effective values only
enclave config --json            # Versioned JSON envelope
```

JSON schema and session drift: [`config --json`](integration.md#config---json).
