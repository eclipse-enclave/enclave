# CLI Reference

```
enclave [FLAGS] [COMMAND]
```

With no command, starts a new agent session (`run` is implicit).

On Windows, `enclave.exe` forwards every argument to the Linux binary inside a
WSL2 distribution and parses none of them itself, so everything below applies
unchanged. See [windows.md](windows.md).

## Commands

### Session

| Command | Description |
|---------|-------------|
| `enclave` | Start a new session with the selected tool |
| `enclave run` | Explicit alias for the above |
| `enclave continue` | Continue the latest session for the selected tool |
| `enclave resume` | Session picker/list when supported (falls back to `continue`) |
| `enclave ps` | List enclave containers (`--all` includes stopped, `--json` emits structured output) |
| `enclave status` | Show terminal snapshots of running sessions |
| `enclave attach [name]` | Attach to a background session by session name or container name |
| `enclave exec` | Attach to a running container (`--name` selects one by session or container name) |
| `enclave exec --admin` | Attach with limited sudo (apt/dpkg) |
| `enclave shell` | Open an interactive shell in the container |
| `enclave shell --admin` | Shell with limited sudo |
| `enclave stop [name]` | Stop background containers, or one session by name |

`enclave ps` prints one row per container with its `NAME` (container name) and `SESSION` (the `--name` session name, or the auto-assigned `1`, `2`, … for extra sessions; `-` for the project's default container); either can be passed to `attach`, `exec --name`, `stop`, and `theia`. Flags: `--all` (include stopped containers, not just running ones), `--json` (emit a JSON array instead of the table). The flags compose (`ps --all --json`). Each JSON object has the fields `name`, `tool`, `projectDir` (absolute, resolved project path), `projectHash`, `worktree`, `status`, `createdAt` (RFC 3339, empty if unknown), `sessionName`, `background`, `ports` (array of `{containerPort, hostPort, hostIP, protocol}` bindings), and `network` (`{name, subnet}` for the per-session bridge under Docker and podman, `null` for other backends and sessions started by earlier releases).

Session provenance: each JSON object also carries `options`, the options that shape the session, keyed and typed like `options.<name>.value` of [`enclave config --json`](configuration.md#json-output-schemaversion-1), as validated for the run.

- `options.features` holds the sorted enabled feature names; `pass_env` holds variable names, never values.
- `backend` and `image_name` are the configured values (for example `auto`), not the detected engine or the derived image tag.
- Host-side and one-shot options (for example `progress`, `verbose`, `reset_auth`, `session_tint`, `session_name`, and build-cache options) are not recorded.
- `options` is omitted when unknown: sessions started by earlier releases, features that could not be resolved at start, or unreadable recorded provenance. qemu sessions are not listed by `ps`, so they have none.

To compare a session with the current configuration, see [Detecting session drift](configuration.md#detecting-session-drift).

`--backend` selects one engine for `ps`, `status`, `stop`, `attach`, and `exec` without changing the saved backend. For example, after `enclave run --backend podman`, use `enclave ps --backend podman` to find that session and `enclave attach --backend podman <name>` to attach. `ps` does not merge sessions from multiple engines.

`status` reports sessions of the current project (like `exec` without `--name`); `--all` widens it to every project. Flags: `--tool` and `--name` filter sessions; `--json` emits one machine-readable snapshot object per session (screen text and OSC title for external state detection). Each snapshot captures the trailing 24 screen rows. See [Session status snapshots](session-status.md).

`enclave attach` flags: `--detach-keys <sequence>` overrides the key sequence for detaching from the session (default `ctrl-\`); `--tool` disambiguates a session name used by more than one tool.

`attach`, `exec --name`, `stop <name>`, and `theia`/`theia-next` accept the
container name from `enclave ps`, a container ID, or the session name passed to
`--name` (`enclave --background --name my-task` → `enclave attach my-task`).
Session names are matched in sanitized form on both sides (lowercased,
non-alphanumerics collapsed to `-`, truncated to 32 characters), so a session
started as `--name "My Task"` is reachable as either `my-task` or `"My Task"`.
The same holds for the `--name` filter of `ps`, `status`, and `stop`; a `--name`
that is blank or sanitizes to nothing matches no session rather than all of
them.

Session names are resolved against the current project first (same worktree,
then same project); they are not required to be unique, so when one matches
several containers the candidates are listed and a container name must be passed
instead. `--tool` narrows the candidates. Passing a container name or ID always
resolves verbatim, independently of the working directory and `--tool`; an
ambiguous container-ID prefix is reported like an ambiguous name.

`attach`, `exec --name`, and `theia` widen the search to all projects when the
current one has no match. `stop` never does, neither for `stop <name>` nor for
`stop --name`: removal is destructive and the auto-assigned names `1`, `2`, …
collide across projects by construction, so another project's session has to be
named by its container name or ID.

`stop <name>` and `stop --name <name>` still select differently. The positional
form removes exactly the one session it resolves, of any tool unless `--tool` is
passed. `--name` filters the batch form instead: it removes every *background*
session of the current project whose name matches, for the single tool that the
options resolve to (so a profile or config `tool` applies as well). Both include
containers that have already exited.

With no argument, `attach` picks the single detached session of the current
project (a foreground session is entered with `exec` instead), and `theia` picks
the single running container of the current project. An argument that is present
but blank (`enclave stop "$SESSION"` with an unset variable) is rejected instead
of being treated as "no argument".

Ctrl-C or SIGTERM while a session is still starting aborts the start with exit code 130 and removes what it created, including the gateway sidecar. A sidecar that an interrupted start nevertheless left behind (for example after a `kill -9`) is removed automatically the next time a session of the same name starts, provided it belongs to that session and no session container exists. Removal is immediate regardless of the sidecar's age or running state: starts of the same session name are serialized until the session container is running. Once the session is attached, Ctrl-C goes to the tool. SIGINT or SIGTERM sent to the enclave process itself (for example by a supervisor) is forwarded to the engine, which delivers it to the tool; the session then exits with the tool's status and the usual cleanup runs.

### Inspect

| Command | Description |
|---------|-------------|
| `enclave info` | Show configuration and image details |
| `enclave version`, `enclave --version` | Show the binary version, source commit, and commit date |
| `enclave self-update` | Replace the host binary with the latest build of its release channel |
| `enclave config` | Show configuration values |
| `enclave tools` | List available tool profiles (`list\|add\|remove\|update` manage installed tool extensions; see below) |
| `enclave features` | List available feature extensions (`list\|add\|remove\|update` manage installed feature extensions; see below) |
| `enclave completion <shell>` | Generate shell completion |

`enclave version` prints one line suitable for bug reports; `--json` emits the version, commit, and date as an object. Builds from modified source append `-dirty` to the commit; unavailable values are reported as `unknown`.

`enclave self-update` downloads the `linux` or `darwin` binary matching the
host architecture from the configured release channel, verifies it against the
release's `checksums.txt`, and atomically replaces the running executable. The
checksums come from the same release as the binary, so they detect corrupted or
truncated downloads, not a compromised release. If the executable already
matches the release's checksum, for example because a concurrent update
installed it, nothing is downloaded. A download that receives no data for 30
seconds fails; Ctrl-C or SIGTERM aborts it with exit code 130 and removes the
partial file.

The updater never elevates privileges. When the executable's directory is not
writable, as after a `sudo install` into `/usr/local/bin`, `self-update` exits
with an error and both it and the periodic check print the download URLs of
the binary and `checksums.txt` and the command that verifies the download and
reinstalls it with `sudo install`. A replacement the system denies only at the
end, as in a sticky directory holding another user's binary, is reported the
same way.

Release channels are `rolling`, rebuilt from every push to `main` and published
on GitHub, and `stable`, for official releases. A binary follows the channel it
was published on unless the global config sets `update_channel`; switching
channels installs the other channel's latest build even if it is older. The
`stable` channel has no download source yet: stable releases are expected on
the Eclipse Foundation download server, and until that is wired up, checks on
`stable` fail: `self-update` exits with an error and the periodic check warns
once per interval. `enclave config` shows the effective channel.

Only standalone release binaries update themselves: package installs, source
builds, and binaries that run against a runtime asset tree beside them or in
`ENCLAVE_HOME` are refused and never checked, because replacing the binary
alone would leave their assets behind. Upgrade those with the package manager
or by rebuilding.

Other interactive invocations, with stdin, stdout, and stderr all terminals,
check the channel at most once every 24 hours, recorded in
`~/.local/state/enclave/self-update-check`, and notify when a different build
is available. Set `"auto_update": true` in the global config to install it
during that check; Ctrl-C during that install exits with code 130 without
running the command. All update-check output, including `--verbose` diagnostics,
goes to stderr. Checks are skipped for scripts and other non-interactive
callers, shell completion, structured output, and user commands; a failed check
waits for the next interval.

`enclave config` flags: `--view <mode>` selects the output view — `matrix` (default), `source` (where each value comes from), `diff` (values overridden by higher precedence), or `effective` (effective values only); `--json` emits a versioned JSON envelope (`schemaVersion: "1"`, typed values, structured warnings, and validation status; see [Inspecting Resolved Config](configuration.md#inspecting-resolved-config)).

### Extensions

`enclave tools` and `enclave features` (bare) are aliases for their `list` subcommand. Every verb below exists for both `enclave tools <verb>` and `enclave features <verb>`; see [Installing Extensions](extensions/installing.md) for the full behavior.

| Command | Flags |
|---------|-------|
| `list` | `--json` (machine-readable output); `features` (bare or `list`) also takes `--slim`, `--features`, and `--tool`, so `enabled` matches a run with those options, including `tool_overrides.<tool>.features` |
| `add <source>` | `--path <dir>`, `--ref <ref>`, `--name <name>` (repeatable), `--all`, `--yes`/`-y`, `--force`, `--dry-run`, `--json` (requires `--yes`) |
| `update [<name\|source>...]` | `--ref <ref>` (single extension only), `--yes`/`-y`, `--force`, `--dry-run`, `--json` (requires `--yes`) |
| `remove <name>...` | `--yes`/`-y`, `--force`, `--json` (requires `--yes`) |

`enclave features update`/`enclave tools update` refresh installed extension **sources**; they are unrelated to the top-level `enclave update`, which rebuilds container **images**.

### IDE

| Command | Description |
|---------|-------------|
| `enclave theia [container]` | Attach the host Theia IDE to a running container |
| `enclave theia-next [container]` | Attach the host Theia Next preview IDE to a running container |

These are host-side attach commands, not tool selections: they launch the
host-installed IDE against an already-running container of any tool. With
exactly one running container, the name may be omitted.

To start a new session container that auto-launches the IDE on top, use the
corresponding tool profile instead (`enclave --tool theia`); see
[Tools](tools.md). The IDE tool profiles run detached automatically: a bare
`enclave --tool theia` starts the container in the background and opens the IDE
in one step, printing the container name so you can reattach later with
`enclave theia <container>`.

### Network

| Command | Description |
|---------|-------------|
| `enclave network status` | Show network policy status |
| `enclave network print` | Print effective dnsmasq config (`--json` emits the allowlist as a versioned envelope; see [JSON output](networking.md#json-output-schemaversion-1)) |
| `enclave network diff` | Show changes from built-in defaults |
| `enclave network add-domain <domain> --global` | Allow a domain |
| `enclave network remove-domain <domain> --global` | Remove a domain |
| `enclave network set-mode restricted\|unrestricted --global` | Set network mode |
| `enclave network apply` | Apply policy to running gateways |
| `enclave network log` | Show gateway network audit events |

`network status`, `print`, and `diff` reflect the run options that shape the allowlist and accept their flags: `--tool`, `--features`, `--slim`, `--playwright-mcp`, `--allow-domain`, and `--allow-all-network` (`status` also takes `--backend`). See [Networking](networking.md#managing-the-network-policy).

Network mutation commands are global-only today. `--project` scope is planned but not yet supported.

`network log` reads the gateway audit log of the current project and tool, including sessions that have already exited:

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

`--follow` and `--summary` are mutually exclusive, and `--since session` needs a scope covering exactly one session, so it cannot be combined with `--all-running`. See [Networking](networking.md#reading-the-network-log) for the output format and [Coverage and granularity](networking.md#coverage-and-granularity) for what the log does and does not record.

Mutation commands (`add-domain`, `remove-domain`, `set-mode`) apply the new policy to running gateways by default. The runtime apply targets the current project's gateways for every tool, because these commands edit the global policy. Use `--no-apply` to persist only, or `--all-running` to target every running gateway on the host. `network apply` targets the selected tool's gateway by default and accepts `--tool` and `--all-running`.

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
| `enclave devcontainer shell` | Interactive shell from `devcontainer.json` |
| `enclave devcontainer generate` | Generate `.devcontainer/devcontainer.json` |

### Build

| Command | Description |
|---------|-------------|
| `enclave update [tool...]` | Rebuild tool image(s) with the latest agent CLI, then exit (no session). Defaults to the selected tool; accepts the same build flags as a run and `--backend` to select the engine for the image. |

### Cleanup

| Command | Description |
|---------|-------------|
| `enclave cleanup` | Remove persistent stores and caches for current tool/project |
| `enclave cleanup --all` | All projects and tools |
| `enclave cleanup --ephemeral` | Remove stopped containers together with their per-session networks, stale per-session networks of the same tool and project (no container, no endpoints, older than an hour), and ephemeral session stores |
| `enclave cleanup --dry-run` | Preview what would be removed |
| `enclave cleanup --keep cache,history,auth,memory` | Preserve the listed stores (comma-separated or repeated `--keep`): `cache` (package caches), `history` (shell history and the config store, including conversation history), `auth` (auth stores, with `--all`), `memory` (per-project agent memory, no selective effect with `--all`) |
| `enclave cleanup --build-cache` | Prune Docker build cache (requires confirmation) |

For a tool with session-scoped memory (Codex), `history` and `memory` name one
unit: `--keep memory` also preserves the config store and `--keep history` also
preserves memory. With `--ephemeral`, `--keep memory` preserves each session
store that holds memory; the other `--keep` kinds do not apply there. See
[Agent Memory](runtime/stores.md#agent-memory).

---

## Flags

Boolean flags accept an explicit value (e.g. `--no-cache=false`) in any form Go's `strconv.ParseBool` accepts: `true`/`false`, `1`/`0`, `t`/`f`, and their upper- and title-case variants; other values are rejected. `--no-yolo=false` is equivalent to `--yolo`.

### Tool & Session

| Flag | Description |
|------|-------------|
| `--tool <tool>` | Tool profile to use (asked once on the first interactive session, then the saved choice; run `enclave tools` for the installed list) |
| `--backend <backend>` | Isolation backend: `auto` (default: docker or podman, whichever is installed), `docker`, `podman`, or experimental `qemu`. Also accepted by engine-using management commands; affects only this invocation. |
| `--name <name>` | Named persistent session |
| `--background` | Detached background session |
| `-p <port>` | Publish a container port to the host (container → host, e.g. `-p 3002`). A host port of `0` (e.g. `-p 0:3000`) lets the daemon pick a free host port (Docker only); read it back with `enclave ps --json`. |
| `--bridge-port <port>` | Forward a host port into the container (host → container, repeatable, comma-separated) |
| `--add-dir <path>` | Mount an additional host directory |
| `--add-readonly-dir <path>` | Mount an additional host directory read-only |
| `--allow-sensitive-mounts` | Mount the project or additional directories even when they expose sensitive host data (CLI-only). See [Sensitive mounts](#sensitive-mounts) |
| `--project-mount <writable\|readonly>` | Mount the project/worktree read-write (default) or read-only |
| `--worktree-metadata <follow\|readonly\|none>` | Linked-worktree git metadata mounts: follow the project mount (default), force read-only, or skip |
| `--yolo` | Enable YOLO mode explicitly |
| `--no-yolo` | Disable YOLO mode (agents will prompt for confirmation) |
| `--host-config <none\|passthrough>` | Reuse reviewed paths from the host tool config |
| `--skills-validation <strict\|agent>` | Validate shared skill frontmatter strictly (default) or leave metadata interpretation to the agent; values are case-insensitive |
| `--session-monitor` | Run the agent under the managed tmux session (enables `status` snapshots) |
| `--verbose` | Verbose logging |
| `--allow-root` | Skip the root check (accepted by every command; CLI-only). See [Running as root](#running-as-root) |
| `--playwright-mcp` | Enable Playwright MCP server for browser automation (Claude only) |

### Image & Build

| Flag | Description |
|------|-------------|
| `--rebuild` | Force image rebuild |
| `--no-rebuild` | Use existing images and suppress all image builds; fail if a required image is missing |
| `--base-image <image>` | Override Docker base image |
| `--use-remote-user` | Honor devcontainer `remoteUser` for agent sessions |
| `--slim` | Build without features (tools only) |
| `--image-name <name>` | Override image name/tag |
| `--features <list\|default\|all\|none>` | Enable selected feature extensions (comma-separated), or use `default`, `all`, or `none` |
| `--cache-from <image>` | Reuse inline build cache from an image |
| `--build-uid <uid>` | UID to bake into the runtime image instead of the host UID |
| `--build-gid <gid>` | GID to bake into the runtime image instead of the host GID |
| `--runtime-uid-remap` | Start as root and remap the container user to the host UID/GID before running |
| `--buildx-cache-dir <path>` | Import/export a local buildx `mode=max` cache directory |
| `--buildx-cache-from <spec>` | Raw buildx cache import spec (repeatable) |
| `--buildx-cache-to <spec>` | Raw buildx cache export spec (repeatable) |
| `--progress <quiet\|compact\|verbose>` | Build output style |
| `--force-base-image` | Bypass devcontainer base image compatibility checks |

For terminal-wide rebuild suppression, use `export ENCLAVE_NO_REBUILD=1`.
Session commands (`run`, `shell`, `continue`, `resume`, devcontainer run/shell,
and user-defined session commands) then reuse local runtime and gateway images
or QEMU bundles, skip automatic agent update probes, and fail if required assets
are missing. Non-building commands ignore it, but `enclave config` reports it.
`--rebuild` and top-level `enclave update` are rejected while it is enabled;
extension-source updates (`tools update` / `features update`) are unaffected.

Use `unset ENCLAVE_NO_REBUILD` or `export ENCLAVE_NO_REBUILD=0` to restore normal
behavior, or `ENCLAVE_NO_REBUILD=0 enclave update` for a single invocation.
An explicit `--no-rebuild=false` also overrides it for one invocation.
There is no config-file key.

### Auth & Secrets

| Flag | Description |
|------|-------------|
| `--ephemeral` | Run without persistent auth/env stores (isolated session) |
| `--reset-auth` | Clear auth files and persisted keys, then inject fresh |
| `--no-api-key` | Disable API key injection |
| `--pass-api-key` | Allow API key injection in `--ephemeral` mode |
| `--pass-env <KEY1,KEY2>` | Forward specific host env vars into the container |
| `--auth-scope <scope>` | `shared` (default) or `project` |
| `--auth-name <slug>` | Select a named per-tool shared auth store; ignored under `--auth-scope=project` |
| `--secrets-scope <scope>` | `both` (default), `global`, or `project` |

### Networking

| Flag | Description |
|------|-------------|
| `--allow-all-network` | Disable network restrictions |
| `--allow-domain <domain>` | Add a domain to the gateway allowlist for this run only (repeatable, no persistence) |
| `--network-log <coarse\|requests>` | Network audit mode. `coarse` (default) logs one pass/deny event per TLS connection, plus per-request events for plaintext HTTP and for hosts the gateway already MITMs for secret release; `requests` forces HTTPS MITM for all allowlisted hosts and emits an audit event per HTTP/HTTPS request. See [Coverage and granularity](networking.md#coverage-and-granularity) |

### Persistence

| Flag | Description |
|------|-------------|
| `--no-cache` | Disable package caches |
| `--no-history` | Disable shell history |
| `--no-memory` | Disable per-project agent memory; see [memory controls](runtime/stores.md#agent-memory) |

---

## Defaults

| Setting | Default |
|---------|---------|
| Tool | Asked once on the first interactive session, then the saved choice; an error without a terminal until `--tool` or the `tool` key is set |
| Network | Restricted (allowlisted domains only) |
| Persistence | Enabled (auth, env, history host-directory stores) |
| YOLO mode | Enabled |
| Auth scope | `shared` |
| Secrets scope | `both` |

Persistent defaults can be set in `~/.config/enclave/config.json` (global) or `~/.config/enclave/projects/<hash>/config.json` (per-project). See [Configuration](configuration.md).

## Tool selection

The tool is unset by default. The first command that needs one asks once which coding agent to use and saves the answer as `"tool"` in `~/.config/enclave/config.json`; later runs read it from there and never ask again. A command needs a tool when it starts a session or builds its image (`run`, `shell`, `continue`, `resume`, `exec`, and `update` without explicit tool arguments) or scopes policy, stores, or containers by tool (`info`, `auth import`, `auth export`, `network …`, `cleanup` without `--all`, and `stop` without a session argument). The question is only asked when stdin, stdout, and stderr are terminals and no `--json` or `--yes` was given; without a terminal, or when the question goes unanswered, the command fails with a message naming `--tool` and the `tool` key instead of guessing an agent. Commands that never read the default tool (`ps`, `status`, `attach`, `tools`, `features`, `config`, `review-target`, `theia`, …) neither ask nor fail; they take `--tool` at most as an explicit filter.

The menu lists every installed agent. The IDE profiles (`theia`, `theia-next`) are not offered: they attach a host IDE to a container rather than running an agent in the terminal. Pick them with `--tool theia` or the `tool` key.

`--tool` overrides the saved choice for a single run, and a configured `tool` disables the question. Set `"tool": "auto"` to be asked again.

## Running as root

enclave refuses to run as root, including through `sudo`. The agent's container user takes the host UID, so as root the agent would run as UID 0, which is host root on bind-mounted directories under rootful Docker. Files enclave and the agent write, both in the project and in the stores under the config, state, and cache roots, would become root-owned, and later runs as the regular user fail on them (with `sudo -E`, those roots are in the regular user's home). Run enclave as a regular user with access to the Docker socket (see the [requirements](../README.md#requirements)), or use rootless podman with `--backend podman`.

To skip this check, for example in a CI job container, pass `--allow-root` or set `ENCLAVE_ALLOW_ROOT=1`; each such run prints a warning. The opt-in only skips the check: running as root stays unsupported, and the runtime image cannot be built for UID 0 yet. The opt-in has no config key, so neither global nor project config can grant it. Help, version, and shell completion work without it and write nothing.

## Sensitive mounts

Commands that start a session (`run`, `shell`, `continue`, `resume`, the `devcontainer` variants, and session user commands) refuse to mount a project directory or an `--add-dir`/`--add-readonly-dir` entry, including `add_dirs` from config, that would expose sensitive host data. Such a directory is sensitive when it:

- is your home directory or one of its parents, such as `/` or `/home`; running enclave from `~` is refused
- is, lies inside, or contains a hidden entry of your home directory, such as `~/.ssh`, `~/.aws`, `~/.config`, or `~/.bashrc`; a hidden entry that is a symlink counts where it points, so a stow-managed dotfiles directory is sensitive too
- is, lies inside, or contains a per-user application data directory: the XDG config, data, state, cache, and runtime directories on Linux (honoring `XDG_*` overrides), or the entries of `~/Library` on macOS except `CloudStorage` and `Mobile Documents`, which hold your own synced files
- is, lies inside, or contains enclave's own config, state, or cache root
- contains the SSH agent socket named by `SSH_AUTH_SOCK`

Read-only mounts get no exemption: reading is enough to leak credentials. The check runs before any image build and on every session start, including `continue` and `resume`. `exec` and `attach` enter running containers and are not checked.

To mount such a directory anyway, pass `--allow-sensitive-mounts` or set `ENCLAVE_ALLOW_SENSITIVE_MOUNTS=1`; each sensitive mount then prints a warning. An explicit `--allow-sensitive-mounts=false` overrides the environment variable. The opt-in has no config key, so neither global nor project config can grant it. Mounts from the repository stay blocked even with the opt-in: devcontainer bind sources must lie inside the project, and linked-worktree git metadata outside the project is skipped with a warning when it is sensitive.

The mounts enclave adds itself, such as `~/.gitconfig`, `~/.claude/ide` for the IDE bridge, direnv approvals, and the session stores, come from protected locations by design and are not checked.

## Backend detection

The default backend is `auto`: enclave uses docker when its CLI is on `PATH`, otherwise podman. A `docker` command that is really the `podman-docker` shim counts as podman, and an explicit `docker`, from `--backend` or the `backend` key, that turns out to be the shim is driven as podman as well, with a notice. When both engines are installed, a command that uses an engine asks once which one to use and saves the answer as `"backend"` in `~/.config/enclave/config.json`. The question is only asked when stdin, stdout, and stderr are terminals and no `--json` or `--yes` was given; otherwise (scripts, captured output, JSON consumers) docker is used and a notice points at that key. Commands that never touch an engine (`tools`, `features`, `config`, `review-target`, `network print`, `network diff`, `devcontainer generate`) neither detect nor ask. When neither engine is found, the engine check reports it. An explicit `--backend` or a configured `backend` disables detection.

## Podman backend

`--backend podman` drives the Docker backend through podman's Docker-compatible CLI (`podman` on `PATH`; rootless is the tested configuration). Everything the Docker backend does applies unchanged — image builds via buildah, the gateway sidecar with restricted egress, detached sessions, `exec`/`attach`, persistent stores — with a few engine-specific adjustments: the gateway sidecar runs as root inside a `--userns=keep-id` user namespace, auth-reconcile containers run with `--userns=keep-id`, and the session container joins the gateway's user namespace (`--userns=container:<gateway>`) alongside its network namespace, so the container user keeps the host user's UID/GID on bind-mounted stores under rootless podman while sharing one user namespace with the network stack it joins (a separate keep-id namespace cannot mount `/sys` in a network namespace it does not own, which fails container creation under runc; crun masks this by bind-mounting the host `/sys`); image builds rely on podman's local layer cache instead of Docker's `--cache-from`/inline-cache flags, and `--buildx-cache-dir`, `--buildx-cache-from`, and `--buildx-cache-to` are ignored with a warning because podman has no buildx cache import or export; and the rendered Dockerfile drops the npm/Go/uv build caches mounted under the agent home, because buildah commits the parent directories of such cache mounts as root-owned, which would leave the agent unable to write its home (apt caches and layer caching still apply, so only cold rebuilds of feature and tool installs are slower). Per-session networks require netavark and are created with `--opt isolate=strict`, because netavark only blocks traffic between bridge networks when asked (netavark 2 / podman 6 made `strict` the default); a netavark that rejects `strict` gets `isolate=true`, which isolates sessions from each other but not from containers on non-isolated podman networks, and enclave warns when it falls back. Devcontainer mode is only verified with Docker and stays rejected for `podman`. Images carry the same tags as under Docker (podman stores them as `localhost/enclave-<tool>:...`). Container inspection accepts both podman 4.x output, which renders `Entrypoint` as one string, and the Docker-shaped arrays of podman 5. The gateway base images are fully qualified (`docker.io/library/...`) and digest-pinned, and the digest-pinned `debian` and `node` base images of the tool image bypass short-name resolution, so hosts without `unqualified-search-registries` or registry aliases in `registries.conf` build both images; a custom `--base-image` given as a bare short name still needs an alias or search registry there.

Rootless podman without idmapped-mount support has to copy an image into a layer for the keep-id mapping the first time that image starts, and again after every image rebuild. enclave triggers this copy before the gateway starts and prints a notice when it takes longer than two seconds; for multi-gigabyte tool images it takes minutes, during which podman blocks every other podman command, including `enclave ps`.

Commands that use an engine accept `--backend`, including session management, `update`, `theia`/`theia-next`, `info`, `cleanup`, `auth import`/`export`, and the engine-using `network` subcommands (`status`, `log`, `apply`, and policy mutations). Without the flag they use the configured backend or detection. Engine-independent commands such as `network print` and `network diff` do not accept it; `config` accepts it to preview option resolution. `cleanup --build-cache` reports nothing to reclaim under podman, which keeps no separate build cache.

## Experimental QEMU backend

`--backend qemu` runs a foreground session in a minimal Alpine microVM bundle. It implies `--allow-all-network` and `--slim` automatically (and prints a notice that network isolation is unavailable), so you don't have to pass them; requesting something it can't honor — `--features`/`--playwright-mcp` or `--allow-domain` — is rejected. Detached sessions, `exec`, `attach`, restricted egress, HTTP secret release, devcontainers, and non-default feature stacks are not supported yet. The bundle builder uses Docker as a packaging helper; the session itself runs under QEMU. A prebuilt bundle can be used without Docker via `--no-rebuild --image-name /path/to/bundle`. Tool installers that assume Debian/glibc may fail until they get dedicated microVM support.

The backend is x86-64 only: it requires `qemu-system-x86_64` on the host and the bundle builder produces an x86-64 Alpine rootfs (`--platform linux/amd64`, `apk --arch x86_64`) regardless of host architecture. QEMU is launched with `-machine microvm,accel=kvm:tcg`, so only x86-64 Linux hosts get KVM acceleration; on arm64 hosts and on macOS the guest runs under TCG emulation and is correspondingly slow. There is no arm64 guest bundle. Use the default `docker` backend on those hosts.

The guest has no disk: its root filesystem is the unpacked initramfs, held in RAM for the whole session. Guest memory is therefore sized per bundle at launch, at least 4096 MiB (or the tool's `qemuMinMemoryMiB`) and more for large bundles. The host must have that much memory available; the size is not capped to host RAM.

On Linux, Enclave warns before launch if the guest allocation exceeds the host's available-memory estimate (`MemAvailable` in `/proc/meminfo`), reporting both sizes. This is advisory: startup continues without reducing guest memory. The check is skipped on other platforms or when the estimate cannot be read. It does not account for cgroup limits or reserve memory, so the absence of a warning does not guarantee the session will fit.

QEMU sessions use the same persistent stores as Docker sessions (host directories under `~/.local/state/enclave/`): auth credentials, tool config, and persisted env are shared per tool/project, so you can switch between `--backend docker` and `--backend qemu` without re-authenticating.
