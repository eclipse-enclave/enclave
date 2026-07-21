# CLI Reference

```
enclave [FLAGS] [COMMAND]
```

With no command, starts a new agent session (`run` is implicit).

## Commands

### Session

| Command | Description |
|---------|-------------|
| `enclave` | Start a new session (default tool: claude) |
| `enclave run` | Explicit alias for the above |
| `enclave continue` | Continue the latest session for the selected tool |
| `enclave resume` | Session picker/list when supported (falls back to `continue`) |
| `enclave ps` | List enclave containers (`--all` includes stopped, `--json` emits structured output) |
| `enclave status` | Show terminal snapshots of running sessions |
| `enclave attach <container>` | Attach to a named background session |
| `enclave exec` | Attach to a running container |
| `enclave exec --admin` | Attach with limited sudo (apt/dpkg) |
| `enclave shell` | Open an interactive shell in the container |
| `enclave shell --admin` | Shell with limited sudo |
| `enclave stop` | Stop background containers |

`enclave ps` flags: `--all` (include stopped containers, not just running ones), `--json` (emit a JSON array instead of the table). The flags compose (`ps --all --json`). Each JSON object has the fields `name`, `tool`, `projectDir` (absolute, resolved project path), `projectHash`, `worktree`, `status`, `createdAt` (RFC 3339, empty if unknown), `sessionName`, `background`, and `ports` (array of `{containerPort, hostPort, hostIP, protocol}` bindings).

`status` reports sessions of the current project (like `exec`); `--all` widens it to every project. Flags: `--tool` and `--name` filter sessions; `--json` emits one machine-readable snapshot object per session (screen text and OSC title for external state detection). Each snapshot captures the trailing 24 screen rows. See [Session status snapshots](session-status.md).

`enclave attach` flags: `--detach-keys <sequence>` overrides the key sequence for detaching from the session (default `ctrl-\`).

### Inspect

| Command | Description |
|---------|-------------|
| `enclave info` | Show configuration and image details |
| `enclave config` | Show configuration values |
| `enclave tools` | List available tool profiles |
| `enclave features` | List available feature extensions |
| `enclave completion <shell>` | Generate shell completion |

`enclave config` flags: `--view <mode>` selects the output view — `matrix` (default), `source` (where each value comes from), `diff` (values overridden by higher precedence), or `effective` (effective values only); `--json` emits JSON output.

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
| `enclave network print` | Print effective dnsmasq config |
| `enclave network diff` | Show changes from built-in defaults |
| `enclave network add-domain <domain> --global` | Allow a domain |
| `enclave network remove-domain <domain> --global` | Remove a domain |
| `enclave network set-mode restricted\|unrestricted --global` | Set network mode |
| `enclave network apply` | Apply policy to running gateways |

Network mutation commands are global-only today. `--project` scope is planned but not yet supported.

Mutation commands (`add-domain`, `remove-domain`, `set-mode`) apply the new policy to running gateways by default. Use `--no-apply` to persist only, or `--all-running` to target every running gateway on the host (rather than just the current project/tool). By default the runtime apply targets the selected tool's gateway; pass `--tool <tool>` to target a different tool. `network apply` accepts `--tool` and `--all-running`.

### Auth

| Command | Description |
|---------|-------------|
| `enclave auth import --tool <tool>` | Copy host auth files into the auth store |
| `enclave auth export --tool <tool>` | Copy auth store files back to the host |
| `enclave ssh-init` | Initialize isolated SSH keys at `~/.cache/enclave/ssh/` |

### Images

| Command | Description |
|---------|-------------|
| `enclave img import` | Import a host clipboard image into the shared read-only inbox (`/mnt/host-images`) |

`img import` flags: `--screenshot` (capture a region screenshot instead of the clipboard), `--no-copy` (do not copy the resulting container path to the host clipboard). Imported images are capped at 10 MiB. The inbox is global — the image is visible to every `--image-inbox` session. See [Host image inbox](host-image-inbox.md).

### Devcontainer

| Command | Description |
|---------|-------------|
| `enclave devcontainer run` | Start from `devcontainer.json` |
| `enclave devcontainer shell` | Interactive shell from `devcontainer.json` |
| `enclave devcontainer generate` | Generate `.devcontainer/devcontainer.json` |

### Build

| Command | Description |
|---------|-------------|
| `enclave update [tool...]` | Rebuild tool image(s) with the latest agent CLI, then exit (no session). Defaults to the selected tool; accepts the same build flags as a run. |

### Cleanup

| Command | Description |
|---------|-------------|
| `enclave cleanup` | Remove persistent stores and caches for current tool/project |
| `enclave cleanup --all` | All projects and tools |
| `enclave cleanup --ephemeral` | Remove stopped containers and ephemeral session stores |
| `enclave cleanup --dry-run` | Preview what would be removed |
| `enclave cleanup --keep cache,history,auth,memory` | Preserve the listed stores (comma-separated or repeated `--keep`): `cache` (package caches), `history` (shell history), `auth` (auth stores, with `--all`), `memory` (per-project agent memory, no selective effect with `--all`) |
| `enclave cleanup --build-cache` | Prune Docker build cache (requires confirmation) |

---

## Flags

### Tool & Session

| Flag | Description |
|------|-------------|
| `--tool <tool>` | Tool profile to use (`claude` by default; run `enclave tools` for the installed list) |
| `--backend <backend>` | Isolation backend: `docker` (default) or experimental `qemu` |
| `--name <name>` | Named persistent session |
| `--background` | Detached background session |
| `-p <port>` | Publish a container port to the host (container → host, e.g. `-p 3002`) |
| `--bridge-port <port>` | Forward a host port into the container (host → container, repeatable, comma-separated) |
| `--add-dir <path>` | Mount an additional host directory |
| `--add-readonly-dir <path>` | Mount an additional host directory read-only |
| `--project-mount <writable\|readonly>` | Mount the project/worktree read-write (default) or read-only |
| `--worktree-metadata <follow\|readonly\|none>` | Linked-worktree git metadata mounts: follow the project mount (default), force read-only, or skip |
| `--yolo` | Enable YOLO mode explicitly |
| `--no-yolo` | Disable YOLO mode (agents will prompt for confirmation) |
| `--host-config <none\|passthrough>` | Reuse reviewed paths from the host tool config |
| `--session-monitor` | Run the agent under the managed tmux session (enables `status` snapshots) |
| `--verbose` | Verbose logging |
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
| `--image-inbox` | Mount the shared read-only host image inbox at `/mnt/host-images`; feed it with `enclave img import`. See [Host image inbox](host-image-inbox.md). |

### Networking

| Flag | Description |
|------|-------------|
| `--allow-all-network` | Disable network restrictions |
| `--allow-domain <domain>` | Add a domain to the gateway allowlist for this run only (repeatable, no persistence) |
| `--network-log <coarse\|requests>` | Network audit mode. `coarse` (default) logs pass/deny events; `requests` forces HTTPS MITM for allowlisted hosts and emits request-level HTTP/HTTPS audit events |

### Persistence

| Flag | Description |
|------|-------------|
| `--no-cache` | Disable package caches |
| `--no-history` | Disable shell history |
| `--no-memory` | Disable per-project agent memory |

---

## Defaults

| Setting | Default |
|---------|---------|
| Tool | `claude` |
| Network | Restricted (allowlisted domains only) |
| Persistence | Enabled (auth, env, history host-directory stores) |
| YOLO mode | Enabled |
| Auth scope | `shared` |
| Secrets scope | `both` |

Persistent defaults can be set in `~/.config/enclave/config.json` (global) or `~/.config/enclave/projects/<hash>/config.json` (per-project). See [Configuration](configuration.md).

## Experimental QEMU backend

`--backend qemu` runs a foreground session in a minimal Alpine microVM bundle. It implies `--allow-all-network` and `--slim` automatically (and prints a notice that network isolation is unavailable), so you don't have to pass them; requesting something it can't honor — `--features`/`--playwright-mcp` or `--allow-domain` — is rejected. Detached sessions, `exec`, `attach`, restricted egress, HTTP secret release, devcontainers, and non-default feature stacks are not supported yet. The bundle builder uses Docker as a packaging helper; the session itself runs under QEMU. A prebuilt bundle can be used without Docker via `--no-rebuild --image-name /path/to/bundle`. Tool installers that assume Debian/glibc may fail until they get dedicated microVM support.

QEMU sessions use the same persistent stores as Docker sessions (host directories under `~/.local/state/enclave/`): auth credentials, tool config, and persisted env are shared per tool/project, so you can switch between `--backend docker` and `--backend qemu` without re-authenticating.
