# CLI reference

Every `enclave` command and flag; behavior is described in the linked topic
docs.

```
enclave [FLAGS] [COMMAND]
```

With no command, `enclave` starts a new agent session (`run` is implicit).

On Windows, `enclave.exe` forwards every argument to the Linux binary inside a
WSL2 distribution and parses none of them itself, so everything below applies
unchanged. See [Windows](windows.md).

## Commands

### Session

| Command | Description |
|---------|-------------|
| `enclave` | Start a new session with the selected tool |
| `enclave run` | Explicit alias for the above |
| `enclave continue` | Continue the latest session for the selected tool |
| `enclave resume` | Session picker/list when supported (falls back to `continue`) |
| `enclave ps` | List enclave containers (`--all` includes stopped, `--json` emits [structured output](integration.md#ps---json)) |
| `enclave attach [name]` | Attach to a background session by session name or container name |
| `enclave exec` | Attach to a running container (`--name` selects one by session or container name) |
| `enclave exec --admin` | Attach with limited sudo (apt/dpkg) |
| `enclave shell` | Open an interactive shell in the container |
| `enclave shell --admin` | Shell with limited sudo |
| `enclave stop [name]` | Stop background containers, or one session by name |

`ps` shows each container's `NAME` and `SESSION` (the `--name`, an
auto-assigned `1`, `2`, …, or `-` for the default container); either can be
passed to `attach`, `exec --name`, `stop`, and `theia`. `ps` also filters by
`--name` and `--tool`. Name resolution, `stop` scope, and signals:
[Sessions](persistence.md#sessions).

`attach` takes `--detach-keys <sequence>` (default `ctrl-\`) and `--tool` to
disambiguate a name; when the session has exited, it stops the gateway.
`ps`, `stop`, `attach`, and `exec` take `--backend` for one invocation; see
[Backends](backends.md#selecting-the-engine-per-command).

### Inspect

| Command | Description |
|---------|-------------|
| `enclave info` | Show configuration and image details |
| `enclave version`, `enclave --version` | Show the binary version, source commit, and commit date |
| `enclave self-update` | Replace the host binary with the latest build of its release channel; see [Updating](install.md#updating) |
| `enclave config` | Show configuration values |
| `enclave tools` | List available tool profiles (`list\|add\|remove\|update` manage installed tool extensions; see [Extensions](#extensions)) |
| `enclave features` | List available feature extensions (`list\|add\|remove\|update` manage installed feature extensions) |
| `enclave commands` | List installed command extensions (`list\|add\|remove\|update` manage them) |
| `enclave completion <shell>` | Generate shell completion |

`info` takes the build flags, `--tool`, `--backend`, and `--auth-name` to
preview the matching image and auth store. `version` prints one line for bug
reports (`--json`: an object). Modified builds append `-dirty` to the commit;
unavailable values read `unknown`.
`config` takes `--view matrix|source|diff|effective` and `--json`; see
[Inspecting resolved config](configuration.md#inspecting-resolved-config).

### Extensions

`enclave tools|features|commands` without a verb means `list`; every verb
exists for all three. See [Installing extensions](extensions/installing.md).

| Command | Flags |
|---------|-------|
| `list` | `--json` (machine-readable output). `features` (bare or `list`) also takes `--slim`, `--features`, and `--tool`, so `enabled` matches a run with those options, including `tool_overrides.<tool>.features`. `tools` takes `--tool` to mark the selected tool |
| `add <source>` | `--path <dir>`, `--ref <ref>`, `--name <name>` (repeatable), `--all`, `--yes`/`-y`, `--force`, `--dry-run`, `--json` (requires `--yes`) |
| `update [<name\|source>...]` | `--ref <ref>` (single extension only), `--yes`/`-y`, `--force`, `--dry-run`, `--json` (requires `--yes`) |
| `remove <name>...` | `--yes`/`-y`, `--force`, `--json` (requires `--yes`) |

These `update` verbs refresh extension sources; top-level `enclave update`
rebuilds images.

### IDE

| Command | Description |
|---------|-------------|
| `enclave theia [container]` | Attach the host Theia IDE to a running container |
| `enclave theia-next [container]` | Attach the host Theia Next preview IDE to a running container |

They attach the host-installed IDE to a running container of any tool; the
name is optional when one container runs. To start a new session with the IDE,
use `enclave --tool theia`, which runs detached, opens the IDE, and prints the
container name for a later `enclave theia <container>`.

### Network

| Command | Description |
|---------|-------------|
| `enclave network status` | Show network policy status |
| `enclave network print` | Print effective dnsmasq config (`--json` emits the allowlist; see [`network print --json`](integration.md#network-print---json)) |
| `enclave network diff` | Show changes from built-in defaults |
| `enclave network add-domain <domain> --global` | Allow a domain |
| `enclave network remove-domain <domain> --global` | Remove a domain |
| `enclave network set-mode restricted\|unrestricted --global` | Set network mode; running sessions keep their mode until restarted |
| `enclave network apply` | Apply policy to running gateways; gateways of unrestricted sessions are skipped |
| `enclave network log` | Show gateway network audit events |

`status`, `print`, and `diff` accept `--tool`, `--features`, `--slim`,
`--allow-domain`, and `--allow-all-network` (`status` also `--backend`).
Mutations are global-only; `--project` is planned.

| Apply flag | Accepted by | Effect |
|------------|-------------|--------|
| `--no-apply` | `add-domain`, `remove-domain`, `set-mode` | Persist only; do not touch running gateways |
| `--all-running` | mutations, `apply` | Target every running gateway on the host |
| `--tool <tool>` | `apply` | Target that tool's gateway (mutations do not accept it) |

Details: [Managing the network policy](networking.md#managing-the-network-policy).

`network log` reads the gateway audit log of the current project and tool,
including sessions that have already exited:

| Flag | Description |
|------|-------------|
| `-f`, `--follow` | Stream new events as they arrive |
| `--summary` | Show a per-domain aggregate instead of rows |
| `--json` | Emit JSON: the raw JSONL event stream, or a single aggregate object with `--summary` (the integration contract) |
| `--since <dur\|ts>` | Only events since a duration (`10m`), an RFC3339 timestamp, or `session` (which also scopes the output to that session) |
| `--verdict <pass\|deny>` | Filter by verdict |
| `--domain <glob>` | Filter by domain pattern (`example.com` or `*.example.com`) |
| `--type <dns\|http\|tcp>` | Filter by event type |
| `--tool <tool>` | Read another tool's log |
| `--session <name>` | Read one session's events, named by its container (an exited session too) |
| `--all-running` | Merge the logs of all running gateways on the host |

`--follow` excludes `--summary`; `--since session` needs exactly one session
in scope, so not `--all-running`. See
[Reading the network log](networking.md#reading-the-network-log).

### Auth

| Command | Description |
|---------|-------------|
| `enclave auth import --tool <tool>` | Copy host auth files into the auth store |
| `enclave auth export --tool <tool>` | Copy auth store files back to the host |
| `enclave ssh-init` | Initialize isolated SSH keys at `~/.cache/enclave/ssh/`. Not recommended: the key lets the agent push to your Git provider. See [SSH keys](auth.md#ssh-keys) |

### Devcontainer

| Command | Description |
|---------|-------------|
| `enclave devcontainer run` | Start from `devcontainer.json` |
| `enclave devcontainer shell` | Interactive shell from `devcontainer.json` (`--admin` for limited sudo) |
| `enclave devcontainer generate` | Generate `.devcontainer/devcontainer.json` (`--force` overwrites an existing file) |

See [Devcontainer support](tools.md#devcontainer-support).

### Build

| Command | Description |
|---------|-------------|
| `enclave update [tool...]` | Rebuild tool image(s) with the latest agent CLI, then exit (no session). Defaults to the selected tool; accepts the same build flags as a run and `--backend` to select the engine for the image |

See [Agent updates](tools.md#agent-updates).

### Cleanup

| Command | Description |
|---------|-------------|
| `enclave cleanup` | Remove persistent stores and caches for the current tool and project |
| `enclave cleanup --all` | All projects and tools |
| `enclave cleanup --ephemeral` | Remove stopped containers, their networks, gateway orphans, and ephemeral session stores |
| `enclave cleanup --dry-run` | Preview what would be removed |
| `enclave cleanup --keep <kinds>` | Preserve `cache`, `history`, `auth`, or `memory` (comma-separated or repeated) |
| `enclave cleanup --build-cache` | Prune Docker build cache (requires confirmation) |

Details: [Cleanup](persistence.md#cleanup).

## Flags

### Value syntax

Boolean flags accept explicit values (`--no-cache=false`) in Go
`strconv.ParseBool` form: `true`/`false`, `1`/`0`, `t`/`f`, and their upper-
and title-case variants; other values are rejected. `--no-yolo=false` equals
`--yolo`.

List flags (`-p`, `--allow-domain`, `--bridge-port`, `--pass-env`,
`--cache-from`) act on the matching config list:

| Value | Effect |
|-------|--------|
| `x` | Replace the configured list |
| `+x` | Add to the configured list (`-p +3000`) |
| `-x` | Drop a configured entry for this run (`-p -8080:80`, `--pass-env -GH_TOKEN`) |
| `none` | Clear the list |

`--add-dir` and `--add-readonly-dir` are the exception: a plain path adds to
the configured directories, and `none` makes the other paths replace them.
Write `./-dir` or `./+dir` for a relative directory starting with `-` or `+`.
See [List options](configuration.md#list-options).

### Tool and session

| Flag | Description |
|------|-------------|
| `--tool <tool>` | Tool profile for this run (`enclave tools` lists them); see [Tool selection](#tool-selection) |
| `--backend <backend>` | `auto` (default), `docker`, `podman`, or experimental `qemu`. Management commands that use an engine accept it for one invocation. See [Backends](backends.md) |
| `--name <name>` | Named persistent session |
| `--background` | Detached background session |
| `-p <port>` | Publish a container port to the host (container → host, e.g. `-p 3002`). Host port `0` (`-p 0:3000`) lets Docker or Podman pick a free port; read it back with `enclave ps --json`. Repeatable, comma-separated list flag for `ports`; `+port` also replaces a configured entry for the same host port |
| `--bridge-port <port>` | Forward a host port into the container (host → container). Repeatable, comma-separated list flag for `bridge_ports` |
| `--add-dir <path>` | Mount an additional host directory. Repeatable, one path per flag (commas are part of the path); adds to `add_dirs` |
| `--add-readonly-dir <path>` | Mount an additional host directory read-only. Repeatable, one path per flag; adds to `add_readonly_dirs` |
| `--add-sensitive-dir <path>` | `--add-dir <path>` plus a sensitive-dir exception for `<path>` for this run (repeatable, CLI-only). See [Sensitive mounts](mounts.md#sensitive-mounts) |
| `--add-readonly-sensitive-dir <path>` | `--add-readonly-dir <path>` plus a sensitive-dir exception for `<path>` for this run (repeatable, CLI-only) |
| `--allow-sensitive-mounts` | Mount the project or additional directories even when they expose sensitive host data (CLI-only). See [Mounting all sensitive paths](mounts.md#mounting-all-sensitive-paths) |
| `--project-mount <writable\|readonly>` | Mount the project/worktree read-write (default) or read-only |
| `--worktree-metadata <follow\|readonly\|none>` | Linked-worktree git metadata mounts: follow the project mount (default), force read-only, or skip |
| `--yolo` | Enable YOLO mode explicitly |
| `--no-yolo` | Disable YOLO mode (agents will prompt for confirmation) |
| `--host-config <none\|passthrough>` | Reuse reviewed paths from the host tool config; see [Host config passthrough](tool-config.md#host-config-passthrough) |
| `--skills-validation <strict\|agent>` | Validate shared skill frontmatter strictly (default) or leave metadata interpretation to the agent; values are case-insensitive |
| `--verbose` | Verbose logging |
| `--allow-root` | Skip the root check (accepted by every command; CLI-only). See [Running as root](install.md#running-as-root) |

See [Port direction](networking.md#port-direction--p-vs---bridge-port) and
[Mounts](mounts.md).

### Image and build

| Flag | Description |
|------|-------------|
| `--rebuild` | Force image rebuild |
| `--no-rebuild` | Use existing images and suppress all image builds; fail if a required image is missing. See [Suppressing rebuilds](tools.md#suppressing-rebuilds) |
| `--base-image <image>` | Override Docker base image |
| `--use-remote-user` | Honor devcontainer `remoteUser` for agent sessions |
| `--slim` | Build without features (tools only) |
| `--image-name <name>` | Override image name/tag |
| `--features <list\|default\|all\|none>` | Enable selected feature extensions (comma-separated), or use `default`, `all`, or `none`. A bare list replaces the configured selection; `+name`/`-name` amend it. `none` cannot be combined with other values. See [Selections](configuration.md#selections) |
| `--cache-from <image>` | Reuse inline build cache from an image. Repeatable, comma-separated list flag for `cache_from` |
| `--build-uid <uid>` | UID to bake into the runtime image instead of the host UID |
| `--build-gid <gid>` | GID to bake into the runtime image instead of the host GID |
| `--runtime-uid-remap` | Start as root and remap the container user to the host UID/GID before running |
| `--buildx-cache-dir <path>` | Import/export a local buildx `mode=max` cache directory |
| `--buildx-cache-from <spec>` | Raw buildx cache import spec (repeatable) |
| `--buildx-cache-to <spec>` | Raw buildx cache export spec (repeatable) |
| `--progress <quiet\|compact\|verbose>` | Build output style |
| `--force-base-image` | Bypass devcontainer base image compatibility checks |

`--slim` and `--features none` build tool-only images without feature
packages or home files. See [Build cache](tools.md#build-cache).

### Auth and secrets

| Flag | Description |
|------|-------------|
| `--ephemeral` | Run without persistent auth/env stores (isolated session) |
| `--reset-auth` | Clear auth files and persisted keys, then inject fresh |
| `--no-api-key` | Disable API key injection |
| `--pass-api-key` | Allow API key injection in `--ephemeral` mode |
| `--pass-env <KEY1,KEY2>` | Forward host env vars into the container; can forward a key `--no-api-key` withholds. Repeatable, comma-separated list flag for `pass_env` |
| `--auth-scope <scope>` | `shared` (default) or `project` |
| `--auth-name <slug>` | Select a named per-tool shared auth store; ignored under `--auth-scope=project` |
| `--secrets-scope <scope>` | `both` (default), `global`, or `project` |

See [Authentication and secrets](auth.md).

### Networking

| Flag | Description |
|------|-------------|
| `--allow-all-network` | Disable network restrictions. A gateway still logs HTTP/HTTPS on ports 80 and 443 unless `--disable-gateway` is set; see [Unrestricted mode](networking.md#unrestricted-mode) |
| `--disable-gateway` | Run without a network gateway, so no network log is written. Requires unrestricted network mode (`--allow-all-network`, `allow_all_network`, or policy mode `unrestricted`) and never implies it |
| `--allow-domain <domain>` | Allow a domain for this run only, without persisting it. Repeatable, comma-separated list flag for `allow_domains` |
| `--network-log <coarse\|requests>` | Audit mode. `coarse` (default): one pass/deny event per TLS connection, plus per-request events for plaintext HTTP and hosts already MITMed for secret release. `requests`: MITM all allowlisted HTTPS hosts (every host in unrestricted mode) and log every request. See [Coverage and granularity](networking.md#coverage-and-granularity) |

### Persistence

| Flag | Description |
|------|-------------|
| `--no-cache` | Disable package caches |
| `--no-history` | Disable shell history |
| `--no-memory` | Disable per-project agent memory; see [memory controls](runtime/stores.md#agent-memory) |

## Defaults

| Setting | Default |
|---------|---------|
| Tool | Unset; asked once, then saved (see [Tool selection](#tool-selection)) |
| Backend | `auto` (docker or podman, whichever is installed) |
| Network | Restricted (allowlisted domains only) |
| Persistence | Enabled (auth, env, history host-directory stores) |
| YOLO mode | Enabled |
| Auth scope | `shared` |
| Secrets scope | `both` |

Change them in [config files](configuration.md).

## Tool selection

The tool is unset by default. The first command that needs one asks once and
saves the answer as `"tool"` in `~/.config/enclave/config.json`.

| Commands | Behavior when no tool is configured |
|----------|-------------------------------------|
| Start a session or build its image: `run`, `shell`, `continue`, `resume`, `exec`, and `update` without explicit tool arguments | Ask, or fail |
| Scope policy, stores, or containers by tool: `info`, `auth import`, `auth export`, `network …` except `add-domain`, `remove-domain`, and `set-mode`, `cleanup` without `--all`, `stop` without a session argument | Ask, or fail |
| Never read the default tool: `ps`, `attach`, `tools`, `features`, `config`, `review-target`, `theia`, … | Neither ask nor fail; `--tool` is at most an explicit filter |

- It asks only when stdin, stdout, and stderr are terminals and neither
  `--json` nor `--yes` is given. Otherwise, or if unanswered, the command fails
  naming `--tool` and the `tool` key; it never guesses.
- The menu lists installed agents, not the IDE profiles (`theia`,
  `theia-next`); select those with `--tool` or the `tool` key.
- `--tool` overrides the choice for one run. Set `"tool": "auto"` to be asked
  again.

## Topics documented elsewhere

| Topic | Doc |
|-------|-----|
| Running as root, `--allow-root` | [Installation](install.md#running-as-root) |
| Sensitive mounts and exceptions | [Mounts](mounts.md#sensitive-mounts) |
| Backend detection, Podman, QEMU | [Backends](backends.md) |
| Session names, signals, detach | [Sessions](persistence.md#sessions) |
| User-defined `enclave <name>` commands | [Commands](commands.md) |
| `self-update` and release channels | [Updating](install.md#updating) |
| `ENCLAVE_NO_REBUILD` | [Suppressing rebuilds](tools.md#suppressing-rebuilds) |
| JSON output of `ps`, `config`, `network` | [Integration](integration.md) |
