# Sessions and persistence

How sessions are started, named, attached, and stopped, what Enclave keeps on
the host between sessions, and how to remove it. See also [Mounts](mounts.md),
[Git](git.md), and [SSH keys](auth.md#ssh-keys).

## Sessions

### Common commands

```bash
enclave                          # Start a session for this project and tool
enclave --name my-task           # Named persistent session, runs next to others
enclave --background             # Detached background session
enclave attach my-task           # Attach by session name (or container name)
enclave continue                 # Continue the latest session
enclave resume                   # Session picker (falls back to continue)
enclave exec                     # Attach to the running container
enclave exec --admin             # Attach with limited sudo (apt/dpkg)
enclave shell                    # Interactive shell in the container
enclave shell --admin            # Shell with limited sudo
enclave stop                     # Stop background containers
enclave stop my-task             # Stop one session by name
```

Sudo is off by default; `--admin` allows apt/dpkg only. Security settings are
fixed at container start, so `exec` enters the container as it is.

A foreground session ends with its terminal (see
[Interrupts and signals](#interrupts-and-signals)). Use `--background` for a
session you can detach from and reattach to.

### Session names

The default container is named `enclave-<tool>-<project-hash>`; other sessions
append `-<session>`, so the same session name can be used in several projects.

- If the project's default name is taken, an unnamed start gets a unique
  name (`1`, `2`, …); use `exec` for the default container. A running
  `--name` is rejected; a stopped one is removed and replaced.
- Concurrent starts for the same tool and project wait for each other until
  the container runs (protecting name, config-store, and gateway setup), then
  run concurrently.

`attach`, `exec --name`, `stop <name>`, and `theia`/`theia-next` accept a
container name from `enclave ps`, a container ID, or a `--name` session name:

| Rule | Behavior |
|------|----------|
| Sanitizing | Both sides are lowercased, non-alphanumerics collapsed to `-`, truncated to 32 characters, so `--name "My Task"` matches `my-task`. Also applies to the `--name` filter of `ps` and `stop` |
| Empty filter | A `--name` that is blank or sanitizes to nothing matches nothing, not everything |
| Resolution order | Current worktree, then current project. Several matches are listed and need a container name; `--tool` narrows them |
| Container name or ID | Resolves verbatim, regardless of directory and `--tool`; an ambiguous ID prefix is reported like an ambiguous name |
| Other projects | `attach`, `exec --name`, and `theia` search all projects if the current one has no match. `stop` never does: removal is destructive and auto-assigned names collide across projects, so use the container name or ID |
| No argument | `attach` picks the project's single detached session (enter a foreground one with `exec`); `theia` its single running container |
| Blank argument | Rejected (`enclave stop "$SESSION"` with an unset variable), not treated as no argument |

`stop <name>` and `stop --name <name>` select differently:

| Form | Removes |
|------|---------|
| `stop <name>` | Exactly the one session it resolves, of any tool unless `--tool` is passed |
| `stop --name <name>` | Every background session of the current project whose name matches, for the single tool the options resolve to (a profile or config `tool` applies too) |

Both forms include containers that have already exited.

### Interrupts and signals

| Event | Effect |
|-------|--------|
| Ctrl-C, SIGTERM, or SIGHUP during startup | Aborts with exit code 130 and removes what was created, including the gateway |
| Ctrl-C once attached | Goes to the tool |
| SIGINT, SIGTERM, or SIGHUP to the enclave process (supervisor, closed terminal) | Forwarded to the tool; the session exits with its status and normal cleanup runs |
| `Ctrl-\` in a foreground session, or the engine client exiting while the container runs | Container stopped (SIGTERM plus grace period) and removed with its gateway; exit code 130. A foreground session has no detach |

`Ctrl-\` replaces the engine's `ctrl-p,ctrl-q`, so `Ctrl-P` reaches the tool.

Gateways left by an interrupted start (e.g. `kill -9`) are removed:

- immediately by the next start of the same session name, if they belong to it
  and no session container exists (same-name starts are serialized)
- by other starts and `cleanup --ephemeral` once stopped, owned, sessionless,
  and exited at least a minute ago; recent or unknown exits are kept, running
  orphans wait for a same-name start

## Data on the host

Per-project data is stored on the host and reused across sessions:

| Data | Location |
|------|----------|
| Package caches | `~/.cache/enclave/<tool>/<project-hash>/` (built-in plus [extension-declared](extensions/spec-reference.md#project-caches-caches) caches) |
| Shell history | `~/.local/state/enclave/projects/<project-hash>/<tool>/history/` |
| Agent memory | `~/.local/state/enclave/projects/<project-hash>/<tool>/memory/` (Claude); `memory/<key>/` (Codex, matching the config-store key) |
| Config/env/auth stores | Host directories under `~/.local/state/enclave/` (bind-mounted; no Docker volumes) |
| Network log and captures | `~/.local/state/enclave/projects/<project-hash>/<tool>/logs/` (`network.log`; `capture/<session>/` with [full capture](networking.md#full-capture)) |
| Embedded runtime assets | `~/.cache/enclave/assets/<content-hash>/` |

macOS paths: [Host directories](configuration.md#host-directories). Store
lifecycle: [Persistent stores](runtime/stores.md); memory rules:
[Agent memory](runtime/stores.md#agent-memory).

The cache root is disposable; deleting it only costs performance. Package
caches are recreated empty (Docker Desktop for macOS can briefly report a
recreated one as missing), asset entries are re-extracted on the next run and
pruned after 30 days unused, and no required runtime file is mounted from it.

### Disabling persistence

```bash
enclave --no-cache      # Disable package caches
enclave --no-history    # Disable shell history
enclave --no-memory     # Disable per-project agent memory
enclave --ephemeral     # No persistent stores at all (fresh isolated session)
```

## Cleanup

```bash
enclave cleanup --dry-run
enclave cleanup --keep cache
enclave cleanup --keep cache,history
enclave cleanup --ephemeral
enclave cleanup --all
```

| Option | Effect |
|--------|--------|
| (none) | Remove package caches, shell history, the config and env stores, generated config and skills, agent memory, and full network captures of the current tool and project |
| `--all` | Remove stores and caches for all projects and tools, including shared auth stores |
| `--ephemeral` | Stop the gateways of exited background sessions; remove owned, stopped gateway orphans with no session and an exit at least one minute old; remove stopped containers together with their per-session networks; remove stale per-session networks of the same tool and project (no container, no endpoints, older than an hour); remove ephemeral session stores |
| `--keep <kinds>` | Preserve the listed kinds (comma-separated or repeated `--keep`); see below |
| `--build-cache` | Prune Docker build cache (requires confirmation) |
| `--dry-run` | Preview what would be removed |

| `--keep` kind | Preserves |
|---------------|-----------|
| `cache` | Package caches |
| `history` | Shell history, full network captures, and the session stores: config store (including conversation history), env store, generated config, and generated skills |
| `memory` | Per-project agent memory (removed by default; no selective effect with `--all`) |
| `auth` | Shared auth stores (only relevant with `--all`) |

For a tool with session-scoped memory (Codex), `history` and `memory` name one
unit: `--keep memory` also preserves the config store and `--keep history`
also preserves memory. With `--ephemeral`, `--keep memory` preserves each
session store that holds memory; the other kinds do not apply there. See
[Agent memory](runtime/stores.md#agent-memory).

Under Podman, `--build-cache` reports nothing to reclaim; see
[Backends](backends.md#podman).
