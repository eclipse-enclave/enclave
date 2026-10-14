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
persists between runs. A tool's `sandbox.statePaths` declares config-relative
runtime state that must survive config-source overlays and stay out of host
config passthrough. Codex pins the database indexing its memories there, along
with its `state_*.sqlite*` and `thread_history_*.sqlite*` thread databases. The
thread databases are rebuildable projections of the preserved `sessions/`
rollouts, pinned to avoid re-deriving them on every overlaid run. OpenCode pins
its session database and runtime data there; its separate XDG state directory is
redirected into the config store as `xdg-state/` so TUI state also survives.
Theia and Theia Next pin their backend state
(extension data and storage, workspace data, custom AI prompts, AI chat history,
model snapshots, backend settings, recent workspaces) so it survives the overlay
that host config passthrough of `deployedPlugins/` activates.

**Ephemeral mode** (`--ephemeral`): A fresh store directory is created with a
unique suffix key for each session and removed after the container exits. The
`default` store, if one exists, is left untouched.

**Layout**: Before the container starts, Enclave creates the parents of the
settings target and auth files plus the `skillsDir` and `memoryDir` mount
targets inside the store as the host user. It recreates them after applying a
generated config overlay, which may replace the store contents. Existing
ownership is not changed; Enclave warns with a `chown` command when a layout
component belongs to another user.

**Config-source overlay**: When a patch, override, or host-config passthrough
activates the overlay, Enclave rebuilds the store from generated config on each
launch. For `sandbox.settingsTarget`, it carries in-tool key edits forward by
comparing the store file with a snapshot of the previous generated file and
merging those edits into the current generated file. New patch or override
edits win conflicts. Snapshots live outside the mounted store in
`config-store-baselines/<key>`, follow the same per-project/worktree/session
scope, and are cleaned up with their stores. Missing or malformed store files,
or a missing snapshot, use the generated settings unchanged. Merging can drop
comments and formatting from the tool's copy. A launch without an overlay
invalidates any snapshot for that store key. The first overlay launch after
upgrading from a version without snapshots also uses generated settings once;
pre-upgrade in-tool edits cannot be distinguished from old generated values.

Source: [`internal/runtime/volume_manager.go`](../../internal/runtime/volume_manager.go) `BuildPrep` (intent), [`internal/backend/hoststore/settings_carryover.go`](../../internal/backend/hoststore/settings_carryover.go) (snapshot and merge), [`internal/backend/docker/prepare.go`](../../internal/backend/docker/prepare.go) (Docker store preparation)

## Agent Memory

Tools declare their native memory directory with `sandbox.memoryDir`. Host memory
lives under `~/.local/state/enclave/projects/<hash>/<tool>/memory/`:

| Scope | Host layout | Bundled tool |
|-------|-------------|--------------|
| `project` (default) | `memory/` | Claude (`~/.claude/memory`) |
| `session` | `memory/<config-store-key>/` | Codex (`~/.codex/memories`) |

Session scope keeps each memory repository with the config-store database that
coordinates its writers. Reusing a named session reuses its memory; changing the
name starts with a separate store. Worktrees and concurrent-session suffixes
also have separate stores. This deliberately favors writer isolation over
sharing learned context. An ad-hoc concurrent store may never be reused and
remains until cleanup.

Because the key is the config-store key, memory follows the config store
wherever it goes. In a linked worktree that means the key is `default` until the
project has a config override or patch, and the worktree's own key afterwards:
adding the first override moves the worktree's memory (and its conversation
history) to a fresh store, so the agent starts over. Nothing is deleted. The
previous store stays until cleanup.

Enabling `memoryScope` on a tool that already ran without one does not migrate
anything. Memory the tool previously wrote inside its config store stays there,
shadowed by the new mount at the same path; remove it by hand, or run
`enclave cleanup` for the project, if the split state is a problem.

`--no-memory` omits the memory mount and applies the tool's `sandbox.noMemoryArgs`
at launch. Codex declares `-c features.memories=false` and Claude
`--settings '{"autoMemoryEnabled": false}'`, disabling memory use and generation
without changing saved settings or deleting existing memories. `--ephemeral`
applies the same launch arguments and discards the config store. Tools without
`noMemoryArgs`, or tools started manually from a shell, retain their native
behavior: without the mount their writes land in the config store, which
persists unless the session is ephemeral.

For session-scoped memory, project cleanup treats memory and the entire config
store as a unit: `--keep memory` also retains the config store (including
conversation history), and `--keep history` also retains memory. Default cleanup
removes both. With `cleanup --all`, the existing whole-project-tree behavior
applies: `--keep memory` has no selective effect.

`cleanup --ephemeral` removes memory alongside each selected non-default config
store, which includes the store of a named session even though its container is
left alone. The same pairing applies: `--keep memory` retains each session store
that holds memory, together with that memory. Stores with no memory to keep are
removed either way: every store of a project-scoped tool, and the throwaway
stores of `--ephemeral` runs, which never get a memory mount. The other
`--keep` kinds have no effect on an ephemeral cleanup.

Selective cleanup reads the scope from the tool spec. A tool with no spec,
meaning state left behind by an extension that has since been removed, cleans up
under the default scope rather than failing. A spec that exists but does not load
aborts a project cleanup before anything is deleted, but only when `--keep memory`
or `--keep history` couples memory to the config store. The ephemeral sweep
handles such a tool by itself instead of aborting for every other tool: with
`--keep memory` the tool's stores are skipped, since the scope decides what would
be kept; otherwise they are removed with a warning and its session memory stays
behind until the spec loads again. Every other plan warns and proceeds under the
default scope, which is the plan a working spec would have produced anyway, so a
broken extension never blocks the removal of its own state.

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

Shared skills use strict portable frontmatter by default; `skills_validation`
can delegate metadata interpretation to the selected agent. Invalid skills are
skipped with a warning. Tool-specific skills may use harness metadata. See
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

The following describes ordinary auth, the default. `--protect-auth` uses the
separate [protected identity layout](#protected-identity-layout) below.

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
   - For Mistral Vibe `.env` (the `mistral-vibe` tool from
     [enclave-extensions](https://github.com/eclipse-enclave/enclave-extensions)),
     which Vibe rewrites by replacing the symlink with a real file, a real
     config file newer (by mtime) than the auth file replaces it before linking.
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
with the config and auth store **directories bind-mounted**. The script itself
is read and retained when the backend is initialized, then inlined into the
helper command rather than bind-mounted. Reconciliation therefore remains
independent of the extracted asset cache for the lifetime of the process. Most
tools remain **additive only**: files are copied when the shared auth destination
is missing. Claude `.credentials.json` uses `claudeAiOauth.expiresAt` so a token
refresh stranded as a real config-store file can replace stale shared auth.
Mistral Vibe `.env` uses the file modification time, so a key or proxy setting
re-entered during a session replaces the shared copy.

This makes new credentials (e.g. a fresh OAuth token obtained during the
session) available to other projects on the next run. Background and GUI sessions
run the same finalization when stopped or removed.

Source: [`internal/backend/docker/authsync.go`](../../internal/backend/docker/authsync.go) `syncSharedAuthStores`,
[`runtime-assets/auth-reconcile.sh`](../../runtime-assets/auth-reconcile.sh), [`entrypoint.sh`](../../entrypoint.sh)

### Protected identity layout

Experimental `--protect-auth` uses this layout on Docker/Podman:

| State | Trusted owner / location | Lifetime |
|-------|--------------------------|----------|
| Grant, provider metadata, generation and refresh recovery | Selected `tools/<tool>/auth/<identity>/` directory, one grant file per login adapter (`<auth-name>`, `project.<project-hash>` or `ephemeral.<random>`); gateway-only RW mount | Survives session removal, except an ephemeral identity, which is deleted once its session ended |
| Identity protection and admission | Host-only `locks/store-<store-key-hash>.identity.json`, outside the auth directory and all workload mounts; keyed by the identity's case-folded path below the state root, so an aliased home or state root finds it | Persists across auth reset/removal; only explicit host-side downgrade clears protection |
| Tool config and history | `projects/<project-hash>/<tool>/config-store/<store-key>.protected/`, mounted at the tool's config directory, such as `~/.claude`; never used by ordinary sessions. Generated config, skills and session memory use the same `<store-key>.protected` key | Persists across protected sessions like an ordinary config store |
| Native auth view containing access/refresh handles | `oauth-sessions/<incarnation>/view/`, mounted at `~/.enclave-auth` | Rebuilt and removed per logical session |
| Handle registration, session incarnation and broker readiness attestation | `oauth-sessions/<incarnation>/runtime/`, gateway-only | Survives proxy-process reload, not session end |
| GitHub feature's native credentials and saved grant | `features/github-cli/auth/`, gateway-only when protected | Shared across tools/projects; survives session removal |
| GitHub feature handle/config view | `oauth-sessions/<incarnation>/features/github-cli/view/`, mounted at `~/.config/gh` | Rebuilt and removed per logical session |
| GitHub feature handle registration | `oauth-sessions/<incarnation>/features/github-cli/runtime/`, gateway-only | Survives proxy-process reload, not session end |
| Preparation lease for a session incarnation | Host-only `locks/auth-start-<incarnation-hash>.lock` | Removed once the session verifiably ended |

A session's transient tree and lease are removed when its reservation is
released or reconciled. A failed preparation rolls back its reservation, and
each admission retires trees and leases that no identity reserves and no
preparing session holds, such as those left by a crash during teardown.

Do not mount the real identity or a parent containing it into a workload or
auth-reconcile helper. Both inspected-mount and foreground request-driven
finalization paths omit protected provider and feature auth. Workload-written expiry/metadata
cannot update the trusted grant. `ENCLAVE_AUTH_DIR` names the view at
`~/.enclave-auth`. Claude reads it through `CLAUDE_SECURESTORAGE_CONFIG_DIR`
(`CLAUDE_CONFIG_DIR` is not set); for other tools the entrypoint links each
native credential file listed in `ENCLAVE_PROTECTED_AUTH_FILES` to it, replacing
whatever the protected config store held there. The view also carries initial account state,
which the Claude setup hook copies into a protected config store that has none.
Agent memory is separately mounted. Gateway storage writes preserve the mounted
directory owner; protected-only capabilities let its separate proxy UID write
private host-owned storage. Container ownership/SELinux behavior still needs
[engine verification](oauth-broker-verification.md).

Before workload startup, the host clears prior gateway readiness state and
requires the broker's private `runtime/oauth-ready.json` attestation to match
the requested identities, session and incarnation. The proxy publishes it only
after both native views and MITM listeners are ready. A generic readiness banner
from an older `--no-rebuild` image cannot enable protected startup. Readiness
schema 3 attests concurrent-capable brokers, which fence shared commits (since
schema 2), and names the tool whose adapters the gateway opened; older protected
sessions must be stopped and restarted with rebuilt gateways before sharing
their identities.

Protected sessions may concurrently share an identity across projects and engines.
Host metadata records each session/incarnation separately; only verified gateways
for those reservations may share exact grant/lock binds. Legacy mounts and broader
parent/control binds remain forbidden. Each gateway retains a private registry and
native view; access reads the latest shared grant and refresh exchanges serialize
under the common host identity lock. Readers durably fence shared commits after
another gateway loses a persistence acknowledgement. Detached CLI exit and proxy
reload cannot release admission; verify that the logical session ended first. A host-wide mount-admission lock also serializes
protected enrollment with final workload bind checks through confirmed startup.
All Docker/Podman workloads and QEMU guests reject protected grant/parent/alias
mounts and host-only lock/control directories, including file staging and
copy-back. QEMU inherits an individual host-only lifetime lease from preparation
through finalization; protected admission, reset and downgrade refuse while any
such lease or older QEMU process is live. Ordinary sessions remain concurrent.
Checked canonical mount paths are passed to the engine; host filesystem setup
and host administrators remain trusted. Identity protection persists independently of the
grant, including across reset. Reset removes broker-owned `.oauth-*` interrupted-write
copies without following symlinks and retains unrelated entries. It clears only the selected identity after
ending all of its sessions; `auth reset` preserves protection, and `auth downgrade`
requires an idle, empty identity. Host start leases fence startup before a
container exists; detached owners remain reserved until both workload and
gateway are verified ended. Engine inspection errors retain the reservation.
The host never trusts an in-tree protection marker written through a legacy
auth/config mount. Cleanup retains protected or corrupt identities individually,
reports them in dry-run output, and still processes unrelated identities and
requested build-cache cleanup. Stray files and symlinks in a tool's auth tree
are unlinked without following links, and empty auth trees are removed in the
same cleanup run. Each auth entry's protection check and removal share the
store lock with identity enrollment.

A refresh attempt must be durable before upstream dispatch. If its outcome is
uncertain, require reauthentication or a verified provider recovery mechanism;
never replay the prior rotating refresh token blindly. A committed grant with
a lost reply can be projected through the session's unchanged handles during
the bounded post-commit retry window. Later near-expiry requests refresh again;
a successful earlier rotation does not block proactive refresh. A proxy
reload preserves those handles; unexpected proxy death ends the affected
session, and recovery starts a new session with fresh handles.

## Feature Auth Store

Ordinary feature auth stores follow the same pattern as shared auth stores but are
scoped per-feature instead of per-tool. They allow features that provide their
own authentication (e.g. `github-cli` with `gh auth`) to share credentials
across all tools and projects.

The store directory `~/.local/state/enclave/features/<feature>/auth/` is
bind-mounted at `~/.enclave-feature-auth/<feature>/` inside the container.

A feature auth store is created when the feature extension defines both
`ConfigDir` and `AuthFiles`, persistence is enabled, and auth scope is `shared`.

### Protected GitHub feature

With `--protect-auth`, the built-in `github-cli` feature uses the protected
layout above instead of the raw feature mount and reconciliation described
below. The real `hosts.yml`/`config.yml` stay in the shared feature store; only
the projected native view is mounted at `~/.config/gh`. Workload edits cannot
replace trusted tokens. Existing compatible native files are read by the gateway,
not copied into the workload.

This is a single global feature identity, not one identity per tool
`--auth-name`. Concurrent protected sessions across projects and Docker/Podman
share its grant and identity lock, with separate per-session handles and views. Enrollment refuses existing containers exposing its real
files or trusted control state. Its host-only protection marker survives reset,
auth-directory removal and omission of `--protect-auth`; ordinary access cannot
silently reopen the files. Cleanup retains the protected or corrupt store.
Verified session teardown removes only that session's transient view/registry,
not the grant or sibling views. Reset/downgrade require every session using this
feature identity to have ended.

Use `enclave auth reset --feature github-cli` to clear the idle feature grant
and native `hosts.yml`/`config.yml` inputs, including preferences, while retaining
protection. Removing those inputs prevents reimport on the next launch. Use
`enclave auth downgrade --feature github-cli`
only when intentionally returning an empty, idle identity to ordinary handling.
These operations are separate from the selected tool identity. See
[authentication](../auth.md#github-cli-in-protected-sessions) and the
[pending verification matrix](oauth-broker-verification.md).

Other native-auth features are not supported by this adapter. GitLab's declared
env-secret flow is unchanged; it has no protected native-file store.

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

This diagram describes ordinary auth. Protected tool/feature sessions use the
gateway-only stores and projected native views described above, without native
auth reconciliation.

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
      files in the config store while preserving runtime-state paths and
      carrying settingsTarget key edits forward from this store's snapshot
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
   │  (additive for most tools; Claude credentials use expiresAt,
   │   Mistral Vibe .env uses mtime)
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
`config-store/<key>` other than `default`), stopped ephemeral containers, and
per-session networks whose session is gone (see
[networking](../networking.md#per-session-docker-networks)).

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
