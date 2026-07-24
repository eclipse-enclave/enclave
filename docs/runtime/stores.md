# Persistent Stores and Auth Persistence

Enclave persists tool configuration and authentication data across container
sessions using **host directories** under `~/.local/state/enclave/` (honoring
`$XDG_STATE_HOME`). These directories are bind-mounted into the container; there
are no Docker volumes. This document covers all store types, their host paths,
how auth files are synchronized, and how CLI flags affect store behavior.

## Store Types

| Type | Host Path | Scope | Purpose |
|------|-----------|-------|---------|
| Config | `~/.local/state/enclave/projects/<hash>/<tool>/config-store/<key>/` | Per-tool, per-project | Tool CLI configuration (e.g. `.claude`, `.codex`) |
| Env | `~/.local/state/enclave/projects/<hash>/<tool>/env/` | Per-tool, per-project | Persisted declared secret and `--pass-env` values |
| Shared auth | `~/.local/state/enclave/tools/<tool>/auth/<identity>/` | Per-tool, all projects | OAuth/session files shared across projects; `<identity>` is `default` or the `--auth-name` slug |
| Feature auth | `~/.local/state/enclave/features/<feature>/auth/` | Per-feature, all tools/projects | Feature-specific auth (e.g. `github-cli`) |

The `<hash>` segment is a 12-character hex string derived from the project
directory path, making per-project stores unique per project. The config store
`<key>` is `default` for the persistent store or a session/worktree-specific
suffix for isolated/ephemeral sessions.

## Config Store

The config store holds the tool's configuration directory (the path defined by
`ConfigDir` in the tool profile, e.g. `.claude` or `.codex`). It is bind-mounted
at `$HOME/<ConfigDir>` inside the container.

**Persistent mode** (default): The default foreground session uses
the `config-store/default` directory. Additional concurrent sessions that would
otherwise clobber the same writable config instead get a suffixed store keyed by
session or worktree identity, while shared auth remains in the tool-global auth
store. Tool state such as conversation history, settings, and cached tokens
persists between runs.

**Ephemeral mode** (`--ephemeral`): A fresh store directory is created with a
unique suffix key for each session and removed after the container exits. The
`default` store, if one exists, is left untouched.

Source: [`internal/runtime/volume_manager.go`](../../internal/runtime/volume_manager.go) `BuildPrep` (intent), [`internal/backend/docker/prepare.go`](../../internal/backend/docker/prepare.go) `prepareConfigStore` (mechanics)

## Managed Skills

Tools opt into managed skills with `sandbox.skillsDir` in
`extensions/tools/<tool>/spec.yaml`. Enclave composes shared global/project
skills with tool-specific skills from the canonical config override trees:

- Shared global: `~/.config/enclave/skills/<skill>/`
- Tool-specific global: `~/.config/enclave/tools/<tool>/<relative-skills-path>/<skill>/`
- Shared project: `~/.config/enclave/projects/<hash>/skills/<skill>/`
- Tool-specific project: `~/.config/enclave/projects/<hash>/<tool>/config/<relative-skills-path>/<skill>/`

Extension skills, skills shipped by enabled features, and allow-listed host
passthrough precede those sources. A same-named skill at a higher-precedence
layer replaces the complete lower layer; project shared skills override global
tool-specific skills, and project tool-specific skills have highest precedence.

When the config-source path handles skills, sources are composed directly into
the generated config tree. Otherwise the fallback materializes the effective
directory at `~/.local/state/enclave/projects/<hash>/<tool>/skills-generated/<key>/`
and mounts it read-only at `skillsDir` inside the container. The `<key>` matches
the config-store suffix, so concurrent sessions with different feature selections
compose into separate directories.

Shared skills use strict portable frontmatter and are skipped with a warning
when invalid. Tool-specific skills may use harness metadata. See
[Configuration](../configuration.md#managed-skills) for the complete source
and portability contract.

Source: [`internal/runtime/skills_mount.go`](../../internal/runtime/skills_mount.go),
[`internal/runtime/tool_config_source.go`](../../internal/runtime/tool_config_source.go)

## Env Store

The env store persists resolved declared secret and `--pass-env` values so they
survive across sessions without requiring the user to re-export them each time.

The env store is **never mounted into the running container**. Instead, the
host-side Go code reads and writes the `env` file directly on the host
filesystem through the backend's store manager:

1. **Before start**: The backend checks whether a persisted `env` file exists in
   the env store directory (`os.Stat`).
2. **At auth injection**: If persisted values exist, they are read and used as
   fallbacks when the corresponding environment variable is not set on the host.
3. **After auth injection**: Current declared secret and `--pass-env` values are
   written back to the env file (atomic temp-file + rename, mode `0600`) under a
   store lock.

Secret injection does not change this persistence contract. The env store keeps
resolved real values on the host side, while declared secrets with `release.http`
are exposed to the running tool container only as placeholder values. Suppressed
provider API key secrets are skipped before resolution, so they are not
persisted.

Because the env store is not mounted, the running container cannot read secrets
directly from its filesystem.

The env store is only created when persistence is enabled (the default; disable
it with `--ephemeral`).

Source: [`internal/runtime/volume_manager.go`](../../internal/runtime/volume_manager.go) `BuildPrep` (intent), [`internal/backend/docker/prepare.go`](../../internal/backend/docker/prepare.go) (mechanics),
[`internal/runtime/auth_manager.go`](../../internal/runtime/auth_manager.go) `readPersistedEnv`, `persistEnvToStore`

## Shared Auth Store

The shared auth store holds OAuth tokens and session files that should be
available across all projects for a given tool. It is created when all of the
following are true:

- The tool profile has a non-empty `ConfigDir`
- One or more providers define `authFiles`
- The session is not ephemeral
- Auth scope is `shared` (the default, `--auth-scope=shared`)

The selected store directory
`~/.local/state/enclave/tools/<tool>/auth/<identity>/` (no project hash, making
it tool-global) is bind-mounted at `~/.enclave-auth/` inside the container.

### Entrypoint Symlink Setup

During container startup, `entrypoint.sh` links auth files from the shared auth
store into the tool's config directory:

1. For each file listed in providers' `authFiles` (deduped):
   - If the config path has a **real file** (not a symlink) and the auth path is
     empty, **seed** the auth store by copying the config file into it.
   - For Claude `.credentials.json`, if both config and auth files exist,
     compare `claudeAiOauth.expiresAt` and keep the fresher credential before
     linking.
   - If the config path is a **stale symlink** and the auth path is empty,
     remove the stale link.
   - If the auth path has content, create a symlink: `config path → auth path`.

This means the tool writes to its normal config location, but the symlink
redirects writes to the shared auth store.

For Claude the primary path is different. enclave only redirects Claude's
secure-storage directory: the `securestorageDirEnv`
(`CLAUDE_SECURESTORAGE_CONFIG_DIR`) points at the shared auth store. Claude
itself writes `.credentials.json` and `.oauth_refresh.lock` there and performs
its native token-refresh coordination across concurrent sessions. The
symlink-into-config plus the startup/finalization reconcile remain as a
fallback/migration path: if the variable is unsupported, or a tool atomically
replaces the symlink with a real file, that drift is repaired for known Claude
credentials.

Environment variables consumed by the entrypoint:

- `ENCLAVE_AUTH_DIR` — mount path of the shared auth store
- `ENCLAVE_AUTH_FILES` — comma-separated list of auth file relative paths
- `ENCLAVE_TOOL_CONFIG_DIR` — mount path of the tool config directory

### Post-Exit Sync

After the container exits, the host-side Go code reconciles auth files from the
config store to the shared auth store. The reconcile runs `auth-reconcile.sh`
inside a short-lived helper container (sharing semantics with the entrypoint),
with the config and auth store **directories bind-mounted**. Most tools remain
**additive only**: files are copied when the shared auth destination is missing.
Claude `.credentials.json` uses `claudeAiOauth.expiresAt` so a token refresh
stranded as a real config-store file can replace stale shared auth.

This makes new credentials (e.g. a fresh OAuth token obtained during the
session) available to other projects on the next run. Background and GUI sessions
run the same finalization when stopped or removed.

Source: [`internal/backend/docker/authsync.go`](../../internal/backend/docker/authsync.go) `syncSharedAuthStores`,
[`runtime-assets/auth-reconcile.sh`](../../runtime-assets/auth-reconcile.sh), [`entrypoint.sh`](../../entrypoint.sh)

## Feature Auth Store

Feature auth stores follow the same pattern as shared auth stores but are
scoped per-feature instead of per-tool. They allow features that provide their
own authentication (e.g. `github-cli` with `gh auth`) to share credentials
across all tools and projects.

The store directory `~/.local/state/enclave/features/<feature>/auth/` is
bind-mounted at `~/.enclave-feature-auth/<feature>/` inside the container.

A feature auth store is created when the feature extension defines both
`ConfigDir` and `AuthFiles`, persistence is enabled, and auth scope is `shared`.

### ENCLAVE_FEATURE_AUTH_MAP

The entrypoint receives feature auth configuration through the
`ENCLAVE_FEATURE_AUTH_MAP` environment variable. The format is:

```
<feature>:<config_dir>:<file1>,<file2>|<feature2>:<config_dir2>:<file3>,<file4>
```

For example, `github-cli` with config dir `.config/gh` and auth file
`hosts.yml`:

```
github-cli:.config/gh:hosts.yml
```

### Entrypoint Symlink Setup (Features)

The entrypoint processes each feature entry:

1. Creates `~/.enclave-feature-auth/<feature>/` and `~/<config_dir>/`.
2. For each auth file:
   - Seeds auth from config if config has a real file and auth is empty.
   - Removes stale symlinks when the auth path does not exist at all.
   - Touches an empty auth file if one does not exist (ensures the symlink
     target is always valid).
   - Always creates the symlink: `~/<config_dir>/<file> → ~/.enclave-feature-auth/<feature>/<file>`.

### Post-Exit Sync (Features)

After exit, the Go code copies auth files from within the config store
directory (at `<config_dir>/<file>` relative to the store root) to the feature
auth store. Like shared auth sync, it only copies files missing from the
destination.

Source: [`internal/runtime/volume_manager.go`](../../internal/runtime/volume_manager.go) `BuildPrep`/`authSyncSpec` (intent), [`internal/backend/docker/authsync.go`](../../internal/backend/docker/authsync.go) `syncFeatureAuthStore` (mechanics),
[`entrypoint.sh`](../../entrypoint.sh)

## Lifecycle

```
                    Host (Go CLI)                          Container
                    ─────────────                          ─────────
1. Prepare stores (host directories)
   ├─ Config store: create or reuse (recreate if --ephemeral)
   ├─ Env store: create or reuse
   ├─ Shared auth store: create or reuse
   ├─ Feature auth stores: create or reuse
   │  (created as the invoking user; no chown needed)
   └─ Reset auth files if --reset-auth
                         │
2. Config prepopulation  │
   └─ If config-source is active, host-side overlay replaces generated config
      files in the config store while preserving runtime-state paths
                         │
3. Auth injection        │
   ├─ Read persisted env (host filesystem read)
   ├─ Resolve declared secrets (env, secrets files, persisted)
   ├─ Inject --pass-env values
   └─ Write env back to env store (host filesystem write)
                         │
4. Bind-mount stores     │
   ├─ Config store   → $HOME/<ConfigDir>
   ├─ Auth store     → $HOME/.enclave-auth/
   └─ Feature stores → $HOME/.enclave-feature-auth/<feature>/
                         │
5. Start container ──────┼──────────────────────────────────┐
                         │                                  │
                         │              6. Entrypoint runs   │
                         │                 ├─ Symlink shared auth files
                         │                 │  (config path → auth path)
                         │                 ├─ Symlink feature auth files
                         │                 │  (config path → feature auth path)
                         │                 └─ Apply settings template
                         │                                  │
                         │              7. Tool executes     │
                         │                 (reads/writes go  │
                         │                  through symlinks │
                         │                  to auth stores)  │
                         │                                  │
8. Container exits ──────┼──────────────────────────────────┘
                         │
9. Post-exit sync (auth-reconcile.sh helper container, stores bind-mounted)
   ├─ Sync shared auth: config store → auth store
   │  (additive for most tools; Claude credentials use expiresAt)
   └─ Sync feature auth: config store → feature auth stores
      (additive-only logic)
```

## Auth Scoping

### `--auth-scope=shared` (default)

- Shared auth store is created
  (`~/.local/state/enclave/tools/<tool>/auth/<identity>/`, where `<identity>`
  is `default` or the `--auth-name` slug — see
  [Named auth identities](../auth.md#named-auth-identities)).
- Feature auth stores are created for qualifying features.
- Auth files are symlinked via the entrypoint.
- Foreground exit, exec exit, background stop, and GUI stop/remove finalize provider credentials to shared auth stores.
- Logging into a tool in one project makes the session available to all projects.

### `--auth-scope=project`

- **No** shared auth store is created.
- **No** feature auth stores are created.
- Auth files live only in the per-project config store.
- No cross-project credential sharing.
- `--auth-name` has no effect here (there is no shared auth store to select) and
  is ignored with a warning.

## CLI Flags

| Flag | Default | Effect on Stores |
|------|---------|------------------|
| `--ephemeral` | No | Config store gets a unique suffix key; removed on exit. Env and auth stores are not created. |
| `--auth-scope=shared` | Yes (default) | Creates shared auth and feature auth stores; enables symlinks and post-exit sync. |
| `--auth-scope=project` | No | No shared/feature auth stores; auth stays in the project config store only. |
| `--reset-auth` | No | Deletes auth files from the shared auth and current config stores when scope is shared, or from the config store when scope is project. Also deletes persisted env. |

## Cleanup

`enclave cleanup` removes persistent stores and other host-side data.
`--ephemeral` cleanup removes ephemeral config-store directories (every
`config-store/<key>` other than `default`).

## Source Files

- [`internal/config/host_paths.go`](../../internal/config/host_paths.go) — Store path helpers (`HostStoreConfigDir`, `HostStoreEnvDir`, `HostStoreAuthDir`, `HostStoreFeatureAuthDir`)
- [`internal/backend/hoststore/hoststore.go`](../../internal/backend/hoststore/hoststore.go) — Neutral store-identity → host-directory mapping and cross-process store lock, shared by the Docker and QEMU backends (stores are backend-independent: sessions share auth/config/env state across backends)
- [`internal/backend/docker/storage.go`](../../internal/backend/docker/storage.go) — Host-directory store manager (create, read/write, seed, remove, locking, path-traversal validation)
- [`internal/runtime/volume_manager.go`](../../internal/runtime/volume_manager.go) — Store intent (which stores exist, reset flags, sync spec)
- [`internal/backend/docker/prepare.go`](../../internal/backend/docker/prepare.go), [`internal/backend/docker/authsync.go`](../../internal/backend/docker/authsync.go) — Store mechanics: preparation, auth reset, overlay, post-exit sync, stop/remove finalize
- [`internal/runtime/skills_mount.go`](../../internal/runtime/skills_mount.go) — Managed skill overlay and bind mount assembly
- [`internal/runtime/auth_manager.go`](../../internal/runtime/auth_manager.go) — Declared secret injection, env persistence, session checks
- [`internal/runtime/runtime.go`](../../internal/runtime/runtime.go) — Mount assembly (`addConfigMount`, `addAuthMount`, `addFeatureAuthMounts`), execution flow
- [`entrypoint.sh`](../../entrypoint.sh), [`runtime-assets/auth-reconcile.sh`](../../runtime-assets/auth-reconcile.sh) — In-container and helper-container symlink/reconcile logic for shared and feature auth
