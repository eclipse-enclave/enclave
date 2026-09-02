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

If a container name is already in use, a new session starts with a unique name. Use `exec` to attach to the default container name.

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

Per-project data is stored on the host and reused across sessions. The project
hash is derived from the canonical directory unless a host-owned project tag
selects a shared namespace. Every exact member of a tag uses the same paths
below.

| Data | Location |
|------|----------|
| Package caches | `~/.cache/enclave/<tool>/<project-hash>/` |
| Shell history | `~/.local/state/enclave/projects/<project-hash>/<tool>/history/` |
| Agent memory | `~/.local/state/enclave/projects/<project-hash>/<tool>/memory/` (Claude) |
| Config/env/auth stores | Host directories under `~/.local/state/enclave/` (bind-mounted; no Docker volumes) |
| Embedded runtime assets | `~/.cache/enclave/assets/<content-hash>/` |

Extracted runtime asset entries are reproducible cache data. Deleting them is
safe, and Enclave extracts the current entry again on the next run. Enclave does
not garbage collect entries for older binaries yet.

The paths above use the Linux (XDG) layout. On macOS the same data lives under
the standard Apple locations, in a reverse-DNS application directory: config
and state under `~/Library/Application Support/org.eclipse.enclave/`
(`config/`, `state/`) and caches under `~/Library/Caches/org.eclipse.enclave/`.

Agent memory is skipped for `--ephemeral` sessions: memory written during them
is discarded with the session's config store.

Disable specific persistence:

```bash
enclave --no-cache      # Disable package caches
enclave --no-history    # Disable shell history
enclave --no-memory     # Disable per-project agent memory
enclave --ephemeral     # No persistent stores at all (fresh isolated session)
```

## Cleanup

Remove persistent stores and cached data for the current tool and effective
project namespace. For a tag with multiple members, cleanup reports the members
because the operation affects their shared scope.

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

Your host `~/.gitconfig` is copied into the container at startup so that user name, email, and other preferences carry over automatically.

Git commit and tag signing (`commit.gpgsign`, `tag.gpgsign`) are unconditionally disabled inside the container. Host signing keys (GPG or SSH) are not available in the container, so signed commits would always fail.

## SSH Keys

Giving an agent an SSH key is not recommended; see [Authentication & Secrets](auth.md#ssh-keys) for what the key grants and how to scope it. The SSH directory is always mounted read-only. That stops the agent from changing the key files; it does not limit what the key can do at your Git provider.

## Host Hardening

For host-level security hardening, including user namespace remapping, see [docs/security/host-hardening.md](security/host-hardening.md). Rootless Docker is not supported.
