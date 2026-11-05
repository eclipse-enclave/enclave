# Extension spec reference

Every field an extension's `spec.yaml` can declare, and the sibling files each
kind ships. For the overview see [Extensions](README.md); for credentials and
gateway header injection see [Credentials and providers](credentials.md).

## Format

- Each extension has exactly one `spec.yaml`, or alternatively `spec.json`.
- Field names are camelCase. Decoding is strict: an unknown field is a load
  error, and `schemaVersion` must be `"1"`.
- The authoritative shape is the `specDocument` struct in
  `internal/config/spec.go`.
- `name` and `kind` are validated against the file's location and directory
  name at load time.

Enclave follows the naming and directory structure of Docker's experimental
[sandbox kit format](https://docs.docker.com/ai/sandboxes/customize/kit-reference/)
(sbx) but implements an independent contract with its own extensions and
restrictions. Do not assume an arbitrary sbx kit behaves identically; see
[sbx compatibility](#sbx-compatibility).

## Top-level fields

```yaml
schemaVersion: "1"        # Enclave owns this version; sbx alignment is per release
kind: sandbox | mixin | command  # sandbox = tool, mixin = feature, command = host commands only
name: <kebab-case>        # must match the extension's directory name
displayName: <human name>
description: <one line>

sandbox: {...}            # kind: sandbox only
postStart: {...}          # kind: sandbox only
commands: {...}           # install, startup, initFiles
network: {...}
environment: {...}
credentials: {...}
providers: [...]          # Enclave-native
ports: [...]              # Enclave-native; tools and enabled features
caches: [...]             # Enclave-native; persistent per-project cache mounts

# kind: mixin only
priority: <int, default 100>
aptPackages: [<packages>]
needsRoot: <bool>
failOnInstallError: <bool>
defaultEnabled: <bool>
configDir: <path>
authFiles: [<files>]

# kind: sandbox only
defaultIncluded: <bool>
```

Sections: [tool fields](#tool-fields-sandbox),
[post-start](#post-start-actions-poststart), [commands](#commands-commands),
[environment](#environment-environment), [network](#network-network),
[credentials and providers](credentials.md), [ports](#ports-ports),
[caches](#project-caches-caches), [feature fields](#feature-fields),
[build selection](#build-selection).

A `kind: command` spec accepts only `schemaVersion`, `kind`, `name`,
`displayName`, and `description`; see
[Command extensions](README.md#command-extensions).

## Tool files

| File | Required | Purpose |
|------|----------|---------|
| `spec.yaml` | yes | `kind: sandbox`, `sandbox.*` metadata, `network`, `credentials`, `providers`, `ports` |
| `install.sh` | yes | Installation script run during the image build |
| `gateway-allowlist.conf` | recommended | dnsmasq allowlist for network isolation. Resolved from the user extension tree ahead of the built-in one. A tool without one falls back to the broader `base.conf`. Can include fragments with `conf-file=`. |
| `templates/` | no | Settings templates copied to `/usr/local/share/enclave/templates/` |
| `config-base/` | no | Seed files for the tool's config store, overlaid into the generated config source before the host and per-project layers |
| `check-update.sh` | no | Prebuild hook returning an upstream fingerprint for [automatic updates](#automatic-updates-check-updatesh) |
| `entrypoint.d/*.sh` | no | Scripts sourced at container start, only for this tool |
| `skills/` | no | Agent skills shipped with the tool |
| `go/` | no | Go auth hooks and handlers, compiled into the binary |
| `commands/host/*` | no | Executables that become `enclave <name>` verbs on the host; read only from the user extension root; see [Custom commands](../commands.md#commands-from-extensions) |

## Feature files

| File | Required | Purpose |
|------|----------|---------|
| `spec.yaml` | yes | `kind: mixin`, optional `aptPackages` |
| `install.sh` | no | Installation script; runs as root if `needsRoot: true` |
| `feature-entrypoint.d/*.sh` | no | Scripts sourced at start for every tool, when the feature is enabled |
| `skills/` | no | Agent skills composed into the tool's skills directory when the feature is enabled |
| `commands/host/*` | no | Host commands, as for tools |

## Tool fields (`sandbox`)

`sandbox.entrypoint` is the shared sbx-style launch command. All other
`sandbox.*` fields are Enclave-native tool metadata.

| Field | Purpose |
|-------|---------|
| `entrypoint.run` | Argv used to launch the tool |
| `entrypoint.args` | Extra argv appended after `run`. Tokens in `run` and `args` must not contain whitespace (load error), because they are joined into one command string and re-split. |
| `configDir` | Config directory under `$HOME`, backed by the persistent config store |
| `skillsDir` | Path below `configDir` where managed skills are composed; see [Skills](#skills) |
| `memoryDir` | Home-relative native memory directory ([lifecycle](../runtime/stores.md#agent-memory)) |
| `memoryScope` | Requires `memoryDir`. `project` (default) shares memory per project and tool; `session` (also requires `configDir`) pairs it with each config store for writer isolation and cleanup |
| `noMemoryArgs` | Argv inserted before user arguments for `--no-memory` and `--ephemeral`; use the tool's native controls. Trimmed, blanks dropped, as for `continueArgs` |
| `statePaths` | Requires `configDir`. Config-relative runtime state (include database sidecars) preserved across overlays and excluded from passthrough. Trailing `/` matches a tree; globs match paths or basenames; absolute paths, traversal, and selection directives are rejected |
| `settingsFile` | Aggregated template name under `/usr/local/share/enclave/templates/`; see [Templates](#templates) |
| `settingsTarget` | Template target path below `configDir` |
| `yoloFlag` | Flag that skips the tool's approval prompts |
| `yoloEnabled` | Whether yolo mode is on by default; defaults to `true` |
| `continueArgs` | Args appended for `enclave continue`; see [Session continuation](#session-continuation) |
| `resumeArgs` | Args appended for `enclave resume` |
| `passthroughPaths` | Narrow, reviewed allow-list for `host_config=passthrough`; see [Host config passthrough](#host-config-passthrough) |
| `qemuMinMemoryMiB` | Minimum memory for generated QEMU microVM bundles; see [QEMU](#qemu) |
| `qemuStoreCacheMmap` | Mount the config store with 9p `cache=mmap` |
| `hostConfigDir`, `hostCredentialsFile`, `hostOauthJson` | Host-side locations used by `enclave auth import/export` |

A minimal skeleton is in
[Adding a tool](adding-a-tool.md#1-create-the-directory-and-spec); a complete
example is
[`extensions/tools/claude/spec.yaml`](../../extensions/tools/claude/spec.yaml).
These fields apply to installed third-party tools as well.

### Templates

- `templates/<filename>` is copied into the image as
  `/usr/local/share/enclave/templates/<tool>-<filename>`.
- Set `configDir`, `settingsFile` (that `<tool>-<filename>` name; path
  separators are rejected), and `settingsTarget` (path under `configDir`).
- The runtime composes the template into the generated config source before
  the container starts. The template and `config-base/` resolve from the
  built-in tree and `~/.config/enclave/extensions/tools/<tool>/`, user files
  winning per file as in the build context, so a user-global tool can use
  `templates/` without being upstreamed.
- Users override and patch the result ([Tool config](../tool-config.md));
  patches require `configDir`.

### Host config passthrough

Declare a narrow, reviewed, config-only `passthroughPaths` allow-list. Only
listed paths are copied, and a deny backstop still blocks auth, history,
session, and runtime-state paths. Built-in skill-capable tools include their
skills path; see
[Host config passthrough](../tool-config.md#host-config-passthrough).

### Skills

Set `skillsDir` below `configDir` (both home-relative or absolute). Each
immediate child of a skill source is one named skill, and the effective skills
are mounted at `skillsDir`. Sources, lowest precedence first: the tool
extension's `skills/` (built-in, then user-global), enabled features'
`skills/`, host passthrough, then the user's skill directories. Built-in skills
stay per tool so they can carry harness-specific metadata. Paths, full order,
and validation: [Managed skills](../tool-config.md#managed-skills).

### Session continuation

| Field | Used by | Should |
|-------|---------|--------|
| `continueArgs` | `enclave continue` | Target the latest session |
| `resumeArgs` | `enclave resume` | Open a session picker or list, when the tool has one |

Both are appended after the command. Each falls back to the other, one step
only; with neither, both commands are unsupported for the tool.

### QEMU

Bundles get the largest of 4096 MiB, `qemuMinMemoryMiB`, and what the
unpacked initramfs needs (the host sizes memory to it, so the setting never
accounts for the bundle itself). Set `qemuStoreCacheMmap` when the config
store needs 9p `cache=mmap`, for example for SQLite in WAL mode.

### Automatic updates (`check-update.sh`)

Runs in a containerized probe, never on the host.

| Result | Meaning |
|--------|---------|
| Exit `0` with non-empty stdout | Valid upstream fingerprint |
| Empty stdout or non-zero exit | Fingerprint unknown; no automatic rebuild |

A fingerprint that differs from the last stored one marks the tool for an
automatic update on the next build; see
[Agent updates](../tools.md#agent-updates).

## Post-start actions (`postStart`)

`postStart.openIDE` launches a host IDE once the container is running.
Supported values are `theia` and `theia-next`, used by the tools of the same
name. A run of such a tool starts detached so the IDE can attach; `shell`
sessions are exempt.

## Commands (`commands`)

| Field | Runs | Notes |
|-------|------|-------|
| `commands.install` | At image build | Features only. Woven into the build; an `install.sh` sidecar wins if a feature ships both. A step without a `user` field runs as root, independently of `needsRoot`. |
| `commands.startup` | At container start | Running as root is rejected at load. Runs after all init files and workspace files are in place. |
| `commands.initFiles` | At container start | Seeds files; see below |

`install` and `startup` entries have `command` (string or list), `user`,
`background`, and `description`. `background` is ignored, with a warning, for
`install` steps.

### Startup-seeded files (`commands.initFiles`)

Entry fields: `path`, `content`, `mode` (octal string), `onlyIfMissing`, and
`description`.

`path` and `content` pass through `envsubst` with an allowlist of exactly
`${WORKDIR}` (the project directory, `$PROJECT_DIR`), `${HOME}`, and `${USER}`.
Any other variable is left literal. The implementation is in
`runtime-assets/kit-init.sh` (`enclave_write_init_file`,
`enclave_apply_init_files`).

> [!WARNING]
> An entry whose path is relative or resolves under `${WORKDIR}` writes into
> the user's real project directory, and by default overwrites it on every
> container start. Set `onlyIfMissing: true` to seed once. This differs from
> `files/workspace/**`, which never overwrites.

## Environment (`environment`)

Honored for tools and features.

| Field | Behavior |
|-------|----------|
| `environment.variables` | Env vars injected into the container. Enabled features contribute first; the tool spec wins on key conflicts. Keys must be valid POSIX names. Reserved names (`PROJECT_DIR`, `TOOL`, anything prefixed `ENCLAVE_`) are skipped with a warning. |
| `environment.proxyManaged` | Credential env aliases that receive the gateway placeholder instead of the raw value. Each entry must match a declared `credentials.sources.<id>.env` alias; a typo is a load error. Entries are unioned across the tool spec and all enabled features. See [Placeholders](credentials.md#placeholders). |

## Network (`network`)

| Field | Purpose |
|-------|---------|
| `allowedDomains` | Hosts the session may reach |
| `deniedDomains` | Hosts the session may not reach |
| `serviceDomains`, `serviceAuth` | Hosts that receive a credential as an HTTP header; see [Credentials and providers](credentials.md#gateway-header-injection) |

A feature's `network` block is honored in full: its allowed and denied domains
and its credential injection apply alongside the tool spec's own. Every host
the proxy injects credentials for is added to the session allow set so it
resolves, so a service feature declares its own reachability without help from
the tool spec.

Tools also ship a `gateway-allowlist.conf` ([Tool files](#tool-files)); keep
it minimal and include registries (PyPI, npm, ...) only when needed.

## Ports (`ports`)

Tools and features can publish container ports without Go code:

```yaml
ports:
  - container: 3000
    publish: true
    label: Theia IDE
    openUrl: "http://localhost:{host_port}"
    hostAllocation: fixed   # or auto
```

| Field | Meaning |
|-------|---------|
| `container` | Container port, 1 to 65535 |
| `publish` | Publish the port at session start |
| `label` | Name shown with the URL; defaults to the extension name |
| `openUrl` | URL printed at start; `{host_port}` is replaced with the resolved host port. Defaults to `http://localhost:{host_port}`. |
| `hostAllocation` | `fixed` (default): host port equals container port, so concurrent sessions of the same tool contend for it. `auto`: published as `0:<container>`, the daemon picks a free host port. |

At start, Enclave publishes each `publish: true` port and prints the resolved
`openUrl`. While the session runs, `enclave ps` shows the resolved URL in its
`PORTS` column; published ports without a declaration appear as `host:port`.

| Rule | Detail |
|------|--------|
| Who wins | User `-p` mappings and tool-declared ports win over feature ports for the same container port. Feature ports are published only for sessions that enable the feature. |
| Host binding | `127.0.0.1` on the host; another interface only with `-p ip:host:container` (for example `-p 0.0.0.0:3000:3000`). The service must listen on a non-loopback address in the container. |
| Network isolation | With isolation the binding is on the gateway container (the tool shares its namespace), otherwise on the tool container. Both sit in a per-session bridge, so host access is the same either way, including for background sessions, and unrelated bridge containers cannot reach the address directly. Authenticating user-published services is the user's job. |
| OAuth callbacks | `providers[].oauthPorts` is a separate mechanism and is unaffected. |

## Project caches (`caches`)

Honored for tools and features (Enclave-native). Each entry persists one
directory of the container home across sessions of the same project, for
example the Maven local repository of a `java-dev` feature:

```yaml
caches:
  - name: m2               # subdirectory under the Enclave-managed project cache dir
    target: .m2/repository # container path inside $HOME
```

- At session start, declared entries join the built-in package caches (npm,
  pip, go, go-build, cargo, pnpm, uv, yarn, bun, nvm).
  `~/.cache/enclave/<tool>/<project-hash>/<name>` on the host is bind-mounted
  at `$HOME/<target>`.
- Feature caches apply only to sessions that enable the feature.
- `--no-cache` disables extension caches along with the built-in ones.
  `enclave cleanup` removes them unless `--keep cache` is set.
- The host side is always Enclave-managed: a spec names a subdirectory and a
  container target, never a host path.
- Load errors: a name outside the extension-name charset or taken by a
  built-in cache, a target escaping the container home, or a target equal to
  or nested with a built-in cache or reserved mount (auth stores, shell
  history, the SSH mount, persistent config files). Across specs, an identical
  `name` and `target` pair dedupes; the same name with another target, or
  overlapping targets, fail session start naming both claimants.
- A mount starts empty and shadows image content at its target. Seed it from a
  `feature-entrypoint.d/` script, as `node-dev` copies the default Node version
  into the `nvm` cache.
- A missing parent directory is created as root, so the agent cannot write
  siblings (`~/.m2/settings.xml` beside `.m2/repository`). Create and chown the
  parent in `install.sh` when siblings matter.

## Feature fields

| Field | Default | Meaning |
|-------|---------|---------|
| `priority` | `100` | Install order: lower first, ties broken by name |
| `aptPackages` | none | Apt packages installed automatically |
| `needsRoot` | `false` | Run `install.sh` as root |
| `failOnInstallError` | `false` | Make this feature's install failure fatal |
| `defaultEnabled` | `true` | Installed when the user does not select features explicitly |
| `configDir`, `authFiles` | none | Config directory and auth files for shared tooling such as `github-cli` or `gitlab-cli` |

Features may also declare `credentials` and `network`, for example:

```yaml
schemaVersion: "1"
kind: mixin
name: github-cli
displayName: GitHub CLI
description: GitHub CLI (gh)
needsRoot: true
priority: 50
configDir: .config/gh
authFiles: [hosts.yml, config.yml]
credentials:
  sources:
    github-token: { env: [GH_TOKEN, GITHUB_TOKEN] }
network:
  serviceDomains:
    api.github.com: github-token
    "*.github.com": github-token
  serviceAuth:
    github-token: { headerName: authorization, valueFormat: "Bearer %s" }
```

### Installation order

The CLI generates one source-copy and install block per selected feature, in
priority order: copy the feature's directory, make it readable and executable,
install its apt packages, then run its `install.sh` in the root or user phase.
Adding, removing, or changing one feature only invalidates layers at or after
its position; the other features stay cached.

A direct `docker build .` uses an aggregated fallback instead: all apt packages
together, then all root scripts, then all user scripts. It re-runs every
feature whenever the selection changes.

Either way, a feature must be self-contained. Its `install.sh` may rely only on
its own `aptPackages` or its own downloads, never on packages contributed by
another feature.

### Install failures

| Setting | Effect |
|---------|--------|
| Default | A feature's `install.sh` failure is a warning; the build continues |
| `ENCLAVE_FEATURE_INSTALL_STRICT=1` | Every feature install failure is fatal. Read by the build scripts; neither the CLI nor the Dockerfile sets it. |
| `failOnInstallError: true` | This feature's install failure is fatal even without strict mode |

### Selection

Users select features with the `features` config key or `--features`; see
[List options](../configuration.md#list-options). Changing the selection
changes the image hash, so the next run rebuilds the image with only the
selected features. `--slim` and the qemu backend enable no features.
Devcontainer mode enables none unless features are set explicitly; `+`/`-`
entries then start from no features.

## Build selection

Runtime images are per tool: the CLI builds and runs one tool image per
session, selected with `--tool <name>` or the `tool` key. Internally it sets
the `AGENT_TOOLS` build arg to that tool, and `enclave-install-tool` installs
just that tool.

A tool with `defaultIncluded: false` is opt-in. It is still selectable with
`--tool <name>`, but it is excluded from the raw Dockerfile's default
`AGENT_TOOLS=all` set used by a direct `docker build .`. See
[How it works](README.md#build-time) for the build args.

## sbx compatibility

The schema mirrors the sbx kit format so an sbx kit loads, but some fields
behave differently.

Accepted, then warned about and ignored (never silently):

| Field | Why |
|-------|-----|
| `sandbox.image` | Enclave keeps its own Debian base and never swaps it for a spec-declared image |
| `sandbox.aiFilename`, `agentContext` | No delivery path Enclave can guarantee reaches the agent, because the project-root memory file is the mounted host repository. Ship guidance as a skill or in the tool's own config. |
| `commands.install` on a tool | Tools install through an `install.sh` sidecar |
| Entries under `files/` other than `home/` and `workspace/` | Not part of the format |

Because `sandbox.image` is ignored, a pure-sbx tool kit that relies on the sbx
base image for its tooling loads but cannot start a session ("tool not
installed in image") unless it also ships an `install.sh` that installs the
tool's entrypoint. A pure-sbx feature kit that only layers packages or config
works without one.

Honored, but diverging on purpose, because the workspace is the mounted host
project and installs happen at build time:

| Field | Enclave behavior |
|-------|------------------|
| `files/home/**` | Baked into `$HOME` at build time; kit content wins on overwrite; changes need a rebuild |
| `files/workspace/**` | Copied into the project at container start; never overwrites an existing host file (warns and skips) |
| `commands.startup` as root | Rejected at load |
| `network.serviceDomains` / `network.serviceAuth` | The pairing must be complete in both directions, or the spec is rejected at load; see [Credentials](credentials.md#gateway-header-injection) |
| `commands.install` | Features only; woven into the build; an `install.sh` sidecar wins |

Enclave-native additions with no sbx counterpart include `providers`, `ports`,
`caches`, `serviceAuth.<id>.hosts`, `credentials.sources.<id>.apiKey`, and
`kind: command`.

## Examples

Opt-in tool, excluded from the default image:

```yaml
schemaVersion: "1"
kind: sandbox
name: example-heavy-tool
description: Example heavy tool
defaultIncluded: false
```

Feature with apt packages, simplified from
`extensions/features/devtools/spec.yaml`:

```yaml
schemaVersion: "1"
kind: mixin
name: devtools
displayName: Development Tools
description: Core development tools
priority: 40
aptPackages: [vim, htop, tree, ripgrep]
```

Opt-in feature:

```yaml
schemaVersion: "1"
kind: mixin
name: debug-tools
description: Debugging tools (gdb, strace, ltrace, etc.)
defaultEnabled: false
aptPackages: [gdb, strace, ltrace, tcpdump]
priority: 80
```
