# Sessions & Persistence

## Named Sessions and Background Mode

By default, each `enclave` invocation starts or resumes a session for the current project and tool. Use `--name` to run multiple sessions in parallel:

```bash
enclave --name my-task           # Named persistent session
enclave --background             # Detached background session
enclave attach my-task           # Attach by session name (or container name)
enclave continue                 # Continue the latest session
enclave resume                   # Session picker (falls back to continue)
```

If the default container name is already in use, an unnamed invocation starts a new session with a unique name. An explicit `--name` that is already running is rejected. Use `exec` to attach to the default container name.

Concurrent starts for the same tool and project, including `--name` and `--background`, wait for each other until the container is running. This coordinates name and config-store allocation and protects the shared gateway configuration during startup. Once started, sessions run concurrently.

Containers are named `enclave-<tool>-<project-hash>-<session>`, so the same session name can be used in several projects. `attach`, `stop <name>`, and `theia` resolve a session name within the current project first; when a name matches containers in more than one project, the candidates are listed and a full container name must be passed. `attach` and `theia` also accept a name that only exists in another project; `stop` does not — neither by argument nor by `--name` — since removing a container is destructive: pass its container name instead.

## Managing Running Containers

```bash
enclave exec                     # Attach to running container
enclave exec --admin             # Attach with limited sudo (apt/dpkg)
enclave shell                    # Interactive shell in container
enclave shell --admin            # Shell with limited sudo
enclave stop                     # Stop background containers
enclave stop my-task             # Stop one session by name
```

Sudo is disabled by default. The `--admin` flag grants limited package-management sudo (apt/dpkg only). Security settings are fixed at container start — `exec` attaches to the existing container as-is.

## Port Forwarding and Extra Mounts

```bash
enclave -p 3002                  # Forward a port from container to host
enclave --add-dir ~/other-proj   # Mount an additional host directory
enclave --add-readonly-dir ~/sdk # Mount an additional host directory read-only
```

## Data Persistence

Per-project data is stored on the host and reused across sessions:

| Data | Location |
|------|----------|
| Package caches | `~/.cache/enclave/<tool>/<project-hash>/` |
| Shell history | `~/.local/state/enclave/projects/<project-hash>/<tool>/history/` |
| Agent memory | `~/.local/state/enclave/projects/<project-hash>/<tool>/memory/` (Claude); `memory/<key>/` (Codex, matching the config-store key) |
| Config/env/auth stores | Host directories under `~/.local/state/enclave/` (bind-mounted; no Docker volumes) |
| Embedded runtime assets | `~/.cache/enclave/assets/<content-hash>/` |

Extracted runtime asset entries are reproducible cache data. Deleting them is
safe, and Enclave extracts the current entry again on the next run. Enclave does
not garbage collect entries for older binaries yet.

Everything under the cache root is disposable: deleting it only costs
performance. Package caches are recreated empty on the next session start, as
ordinary host bind mounts that the container backend may create when the
source is missing from its view (relevant on Docker Desktop for macOS, whose
VM can briefly report a freshly recreated cache directory as nonexistent). No
required runtime file is bind-mounted from the cache tree.

The paths above use the Linux (XDG) layout. On macOS the same data lives under
the standard Apple locations, in a reverse-DNS application directory: config
and state under `~/Library/Application Support/org.eclipse.enclave/`
(`config/`, `state/`) and caches under `~/Library/Caches/org.eclipse.enclave/`.

See [Agent Memory](runtime/stores.md#agent-memory) for memory isolation,
`--no-memory`, ephemeral runs, and cleanup retention rules.

Disable specific persistence:

```bash
enclave --no-cache      # Disable package caches
enclave --no-history    # Disable shell history
enclave --no-memory     # Disable per-project agent memory
enclave --ephemeral     # No persistent stores at all (fresh isolated session)
```

## Cleanup

Remove persistent stores and cached data for the current tool and project:

```bash
enclave cleanup
```

Options:

| Flag | Effect |
|------|--------|
| `--all` | Remove stores and caches for all projects and tools |
| `--ephemeral` | Remove stopped containers and ephemeral session stores |
| `--keep <kinds>` | Preserve the listed stores (comma-separated or repeated): `cache` (package caches), `history` (shell history), `memory` (per-project agent memory, removed by default; no selective effect with `--all`), `auth` (auth stores, with `--all`) |
| `--build-cache` | Prune Docker build cache (requires confirmation) |
| `--dry-run` | Preview what would be removed |

Examples:

```bash
enclave cleanup --dry-run
enclave cleanup --keep cache
enclave cleanup --keep cache,history
enclave cleanup --ephemeral
enclave cleanup --all
```

## Git

At session start, enclave reads the host's global `user.name` and `user.email` and writes the configured values to the container's Git config. Git reads `~/.gitconfig` and `$XDG_CONFIG_HOME/git/config` (default `~/.config/git/config`); when `$GIT_CONFIG_GLOBAL` is set, it uses that file instead. Included config files are resolved on the host, including `includeIf gitdir` rules for the project. The host `~/.gitconfig`, if present, is also copied for aliases and other preferences.

Before starting a session, enclave requires a complete author and committer identity from the host configuration, the project's Git config, or session `GIT_AUTHOR_*` and `GIT_COMMITTER_*` variables. Supply session variables through the project's `.env`, devcontainer `containerEnv`, or tool/feature environment variables; exporting them in the host shell does not forward them. Devcontainer `runArgs` `--env`/`-e` and `--env-file` values are not considered by this preflight. Host system Git settings can supply missing global values. Repository-local and worktree settings still take precedence. If the identity is incomplete or host Git config cannot be read, startup fails with an error; enclave never invents a name or email. Inside the container, `user.useConfigOnly` prevents Git from guessing an identity if configuration changes later. Enclave does not write identity settings to the project's `.git/config`.

Git commit and tag signing (`commit.gpgsign`, `tag.gpgsign`) are unconditionally disabled inside the container. Host signing keys (GPG or SSH) are not available in the container, so signed commits would always fail.

## SSH Keys

Giving an agent an SSH key is not recommended; see [Authentication & Secrets](auth.md#ssh-keys) for what the key grants and how to scope it. The SSH directory is always mounted read-only. That stops the agent from changing the key files; it does not limit what the key can do at your Git provider.

## Host Hardening

For host-level security hardening, including user namespace remapping, see [docs/security/host-hardening.md](security/host-hardening.md). Rootless Docker is not supported.
