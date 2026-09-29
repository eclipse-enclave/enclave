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

At session start, enclave reads the host's global `user.name` and `user.email` and writes the configured values to the container's Git config. Git reads `~/.gitconfig` and `$XDG_CONFIG_HOME/git/config` (default `~/.config/git/config`); when `$GIT_CONFIG_GLOBAL` is set, it uses that file instead. Included config files are resolved on the host, including `includeIf gitdir` rules for the project. The host `~/.gitconfig`, if present, is mounted read-only and copied into the container for aliases and other preferences. Changes to the container copy do not modify the host file.

Existing config files for the current repository are protected with read-only file mounts even when the project is writable: `.git/config`, `config.worktree` (including the main and sibling linked worktrees), and config files discovered through Git's includes. Exposed global and system config files discovered through Git are protected too, including empty global files and empty files selected by `GIT_CONFIG_SYSTEM`. Empty system files at Git's compiled-in default path are not discovered. Linked-worktree pointer files are also read-only, and containing directories are anchored with writable bind mounts to prevent renaming a parent to bypass protection. Protection covers exposed bind-mount aliases without exposing otherwise unmounted host files. The rest of the Git metadata remains writable, so staging and committing still work.

A project-root `.gitconfig` is only protected if Git reads or includes it; its filename alone does not make it active configuration.

Protection is always enabled. Commands that write repository config, such as `git remote add`, `git submodule update --init`, upstream setup, and branch renames, can fail or complete only part of their work. In particular, `git push -u` can push successfully without saving upstream tracking, and `git branch -m` can rename a branch before reporting a config-write failure. Make these configuration changes on the host.

Each protected file and otherwise-unmounted parent directory adds a mount. Repositories with many worktrees or deeply nested includes can therefore need many mounts; each directory mount is also a separate 9p device on QEMU. Anchored directories cannot be removed or renamed inside the session, so `rm -rf`, checkouts, and worktree cleanup can fail after making partial changes.

Make changes to protected config files on the host and restart the session; in-container `git config --local` / `--worktree` writes to those existing files will fail. A checkout that would replace a protected tracked config file can report `unable to unlink old`, leave the old contents in place as a modified file, and still switch branches with exit status 0. This is startup protection for discovered, existing files, not a read-only policy for all Git metadata, nested repositories, or config files created later. Missing include targets and missing `config.worktree` files remain creatable, even when the existing configuration already references them.

This does not prevent host code execution through writable Git metadata. In particular, an agent can install hooks in `$GIT_COMMON_DIR/hooks/` or create a previously absent `.git/commondir` to redirect Git to an unprotected config. These changes can affect subsequent host Git commands. Use `--project-mount readonly`, without writable aliases, when Git metadata must not change; linked-worktree metadata also follows the read-only project mount.

Before starting a session, enclave requires a complete author and committer identity from the host configuration, the project's Git config, or session `GIT_AUTHOR_*` and `GIT_COMMITTER_*` variables. Supply session variables through the project's `.env`, devcontainer `containerEnv`, or tool/feature environment variables; exporting them in the host shell does not forward them. Devcontainer `runArgs` `--env`/`-e` and `--env-file` values are not considered by this preflight. Host system Git settings can supply missing global values. Repository-local and worktree settings still take precedence. If the identity is incomplete or host Git config cannot be read, startup fails with an error; enclave never invents a name or email. Inside the container, `user.useConfigOnly` prevents Git from guessing an identity if configuration changes later. Enclave does not write identity settings to the project's `.git/config`.

Git commit and tag signing (`commit.gpgsign`, `tag.gpgsign`) are unconditionally disabled inside the container. Host signing keys (GPG or SSH) are not available in the container, so signed commits would always fail.

## SSH Keys

Giving an agent an SSH key is not recommended; see [Authentication & Secrets](auth.md#ssh-keys) for what the key grants and how to scope it. The SSH directory is always mounted read-only. That stops the agent from changing the key files; it does not limit what the key can do at your Git provider.

## Host Hardening

For host-level security hardening, including user namespace remapping, see [docs/security/host-hardening.md](security/host-hardening.md). Rootless Docker is not supported.
