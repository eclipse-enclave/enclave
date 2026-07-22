# Extension Architecture

This document describes Enclave's modular extension system.

## Overview

Enclave uses a unified extension architecture that supports two types of extensions:

1. **Tool Extensions** - Runnable AI coding agents (Claude, Codex, OpenCode, etc.)
2. **Feature Extensions** - Development tools and capabilities (GitHub CLI, Python dev tools, etc.)

Extensions are organized into two directories:

- `extensions/tools/` - AI coding agents (Claude, Codex, OpenCode, etc.)
- `extensions/features/` - Development tools (GitHub CLI, Python tools, etc.)

This separation makes it immediately clear what type each extension is.

## Extension Sources

Extension definitions are loaded from:

1. Built-in repository extensions under `extensions/`
2. User-global overrides under `~/.config/enclave/extensions/`

Project-local extension definitions (for example,
`/path/to/project/.enclave/extensions/`) are not loaded. Install custom
definitions in the user-global root. Select a custom tool for a project with
`enclave --tool <tool>`; project config cannot set the active tool, although
global config can set a host-wide default. For a project-specific feature,
declare it with `defaultEnabled: false` (to avoid the project-specific
extension being loaded for all projects/containers), then add
`"features": ["+my-feature"]` to
`~/.config/enclave/projects/<hash>/config.json`; see
[Configuration](../configuration.md). Keeping executable extension definitions
outside the checkout prevents a repository from supplying extension install and
startup code merely by being opened.

When an extension exists in both built-in and user-global roots, user files
take precedence over built-in files with per-file overlay behavior.

## Directory Structure

```
extensions/
├── tools/
│   └── claude/
│       ├── spec.yaml               # Extension spec: metadata, sandbox, network, credentials, providers
│       ├── gateway-allowlist.conf  # Network allowlist (required)
│       ├── templates/
│       │   └── settings.json      # Settings template
│       ├── entrypoint.d/
│       │   └── setup.sh           # Tool-specific startup script
│       ├── install.sh             # Docker build installation
│       └── go/                    # Optional Go code
│           ├── hooks.go           # Auth hooks
│           └── handler.go         # Tool handler
└── features/
    ├── github-cli/
    │   ├── spec.yaml
    │   └── install.sh
    ├── node-dev/
    │   ├── spec.yaml
    │   ├── install.sh
    │   └── feature-entrypoint.d/
    │       └── setup.sh           # Runs for ALL tools
    └── devtools/
        ├── spec.yaml
        └── install.sh
```

`spec.yaml` is the extension manifest. `install.sh`,
`gateway-allowlist.conf`, `entrypoint.d/`, `feature-entrypoint.d/`,
`templates/`, and `skills/` remain sibling files rather than fields in the
manifest. The in-container build shell scripts under
`runtime-assets/build-scripts/` read the metadata they need (feature/tool
enablement, `priority`, `needsRoot`, `aptPackages`, `failOnInstallError`)
straight from `spec.yaml` (falling back to `spec.json`) with `yq`.

## Extension Spec (`spec.yaml`)

Every extension has one `spec.yaml` (`spec.json` is also accepted — YAML is a
JSON superset, and the loader reads both with `sigs.k8s.io/yaml`) describing
its metadata, sandbox/mixin behavior, network policy, and credentials. Field
names are camelCase. The schema is defined by `specDocument` in
`internal/config/spec.go`; that struct is the authoritative on-disk shape.

Enclave follows the naming and directory structure of Docker's experimental
[sandbox kit format](https://docs.docker.com/ai/sandboxes/customize/kit-reference/),
but implements an independent contract with both extensions and restrictions.
This document is the Enclave reference; do not assume an arbitrary sbx kit has
identical behavior. The differences are called out below.

```yaml
schemaVersion: "1"        # enclave owns this version; sbx alignment is per-release
kind: sandbox | mixin     # sandbox = tool (was "tool"), mixin = feature (was "feature")
name: <kebab-case>        # must match the extension's directory name
displayName: <human name>
description: <one line>

sandbox: {...}            # kind: sandbox only, see "Tool Extensions" below
commands: {...}           # install/startup/initFiles, see below
network: {...}
environment: {...}
credentials: {...}
providers: [...]          # enclave-native, see "Tool Extensions" below
ports: [...]              # enclave-native

# mixin-only fields (kind: mixin)
priority: <int, default 100>
aptPackages: [<packages>]
needsRoot: <bool>
failOnInstallError: <bool>
defaultEnabled: <bool>

# tool-only field (kind: sandbox)
defaultIncluded: <bool>
```

`name` and `kind` are validated against the file's location and directory
name at load time.

### Reserved and deferred fields

The schema mirrors Docker's experimental [sandbox kit format](https://docs.docker.com/ai/sandboxes/customize/kit-reference/)
so an sbx kit loads here, but a few sbx fields are accepted and then **warn and
no-op** — a declared value is never a silent no-op:

- `sandbox.image` — enclave keeps its own Debian base and never swaps it for a
  spec-declared image.
- `sandbox.aiFilename` and `agentContext` — no delivery path enclave can
  guarantee reaches the agent (the project-root memory file is the mounted host
  repo). Ship tool guidance via a skill or the tool's own config instead.

Because enclave keeps its own base image and ignores `sandbox.image`, a
pure-sbx **sandbox** (tool) kit that relies on the sbx base image for its
tooling will load but cannot start a session ("tool not installed in image")
unless it also ships an `install.sh` that installs the tool's entrypoint. A
pure-sbx **mixin** kit that only layers packages/config works without one.

A few honored fields also diverge from sbx on purpose, because the workspace is
the mounted host project directory and installs happen at `docker build` time:

- `files/home/**` is baked into `$HOME` in the image at **build** time — kit
  content wins on overwrite, and changing it requires a rebuild.
- `files/workspace/**` is copied into the project at container **start** and
  **never clobbers** an existing host file (warns and skips).
- `commands.startup` running as root is **rejected loudly at load**.
- `commands.install` (mixins only) is woven into the build; an `install.sh`
  sidecar wins if a feature ships both.

### Startup-seeded files (`commands.initFiles`)

`commands.initFiles` entries seed a file at container start. Fields per entry:
`path`, `content`, `mode` (octal string), `onlyIfMissing`, `description`. Both
`path` and `content` pass through `envsubst` with a whitelist of exactly
`${WORKDIR}` (the project directory, `$PROJECT_DIR`), `${HOME}`, and `${USER}`;
any other variable is left literal. Implemented in
`runtime-assets/kit-init.sh` (`enclave_write_init_file`,
`enclave_apply_init_files`).

Caveat: an entry whose path resolves under `${WORKDIR}` (or is relative)
writes into the user's **real** project directory, and the default is to
**overwrite** on every container start — set `onlyIfMissing: true` to seed
once. This is deliberately unlike `files/workspace/**`, which never clobbers.

### Environment (`environment`)

Honored for both sandbox and mixin extensions:

- `environment.variables` — env vars injected into the container. Enabled
  mixins contribute first; the tool spec wins on key conflicts. Keys must be
  valid POSIX names, and reserved names (`PROJECT_DIR`, `TOOL`, anything
  prefixed `ENCLAVE_`) are skipped with a warning.
- `environment.proxyManaged` — names the credential env aliases that receive
  the proxy-swapped placeholder instead of the raw value in the container
  environment (the gateway injects the real secret at the network edge). Each
  entry must match a declared `credentials.sources.<id>.env` alias; a typo is
  a load error. Entries are unioned across the tool spec and all enabled
  mixins.

### Examples

**Tool extension (`extensions/tools/claude/spec.yaml`)** — see the full
worked example under [Tool Extensions](#tool-extensions) below.

**Opt-in tool (not included by default):**
```yaml
schemaVersion: "1"
kind: sandbox
name: example-heavy-tool
description: Example heavy tool
defaultIncluded: false
```

**Feature with root install (`extensions/features/github-cli/spec.yaml`):**
```yaml
schemaVersion: "1"
kind: mixin
name: github-cli
displayName: GitHub CLI
description: GitHub CLI (gh)
needsRoot: true
priority: 50
```

**Feature with apt packages (`extensions/features/devtools/spec.yaml`):**
```yaml
schemaVersion: "1"
kind: mixin
name: devtools
displayName: Development Tools
description: Core development tools
priority: 40
aptPackages: [vim, htop, tree, ripgrep]
```

**Opt-in feature (disabled by default):**
```yaml
schemaVersion: "1"
kind: mixin
name: debug-tools
description: Debugging tools (gdb, strace, ltrace, etc.)
defaultEnabled: false
aptPackages: [gdb, strace, ltrace, tcpdump]
priority: 80
```

## Tool Extensions

Tool extensions are runnable AI coding agents (`kind: sandbox`). They require
additional files beyond `spec.yaml`.

### Required Files

| File | Purpose |
|------|---------|
| `spec.yaml` | Extension spec: `kind: sandbox`, `sandbox.*` metadata, `network`, `credentials`, `providers`, `ports` |
| `gateway-allowlist.conf` | dnsmasq config for network isolation |
| `install.sh` | Installation script run during Docker build |

### Optional Files

| File | Purpose |
|------|---------|
| `templates/` | Settings templates copied to `/usr/local/share/enclave/templates/` |
| `check-update.sh` | Optional prebuild hook that returns a stable upstream fingerprint for automatic update probes |
| `entrypoint.d/*.sh` | Scripts sourced at container startup (only for matching tool) |
| `go/` | Go code for custom hooks/handlers (compiled into binary) |

### Complete example — `extensions/tools/claude/spec.yaml`

```yaml
schemaVersion: "1"
kind: sandbox
name: claude
displayName: Claude Code
description: Claude Code AI assistant

sandbox:
  entrypoint: { run: [claude] }
  configDir: .claude
  qemuMinMemoryMiB: 4096
  skillsDir: .claude/skills
  memoryDir: .claude/memory
  settingsFile: claude-settings.json
  settingsTarget: .claude/settings.json
  yoloFlag: --dangerously-skip-permissions
  yoloEnabled: true
  continueArgs: [--continue]
  resumeArgs: [--resume]
  passthroughPaths: [agents/, commands/, settings.json, skills/]
  hostConfigDir: .claude
  hostCredentialsFile: .credentials.json
  hostOauthJson: .claude.json

credentials:
  sources:
    anthropic-api-key: { env: [ANTHROPIC_API_KEY] }
    claude-code-oauth-token: { env: [CLAUDE_CODE_OAUTH_TOKEN], apiKey: false }

network:
  serviceDomains:
    api.anthropic.com: anthropic-api-key
    "*.anthropic.com": anthropic-api-key
  serviceAuth:
    anthropic-api-key: { headerName: x-api-key }

providers:
  - name: anthropic
    credentials: [anthropic-api-key, claude-code-oauth-token]
    authFiles: [config.json, .credentials.json]
    securestorageDirEnv: CLAUDE_SECURESTORAGE_CONFIG_DIR
    authSession:
      mode: any
      checks:
        - { file: config.json, type: file_exists }
        - { file: .credentials.json, type: file_exists }
```

`sandbox.*` fields (`configDir`, `skillsDir`, `memoryDir`,
`settingsFile`, `settingsTarget`, `yoloFlag`, `yoloEnabled`, `continueArgs`, `resumeArgs`,
`passthroughPaths`, `qemuMinMemoryMiB`, `qemuStoreCacheMmap`,
`hostConfigDir`, `hostCredentialsFile`, and `hostOauthJson`) are enclave-native
tool metadata. `sandbox.entrypoint.run` is the shared sbx-style command to
launch the tool.

When using `templates/`, set `sandbox.configDir`, `sandbox.settingsFile`
(aggregated name like `<tool>-settings.json`), and `sandbox.settingsTarget`
(path under `configDir`). Runtime composes the built-in template into the
generated tool config source before container startup.

If a tool supports host config passthrough, declare a narrow reviewed
`sandbox.passthroughPaths` allow-list. Host passthrough is fail closed: only
listed paths are eligible for copy, and a hard-coded deny backstop still
blocks auth/history/session/runtime-state paths.

Generated QEMU microVM bundles use max(default 4096 MiB,
`sandbox.qemuMinMemoryMiB`). Use it for tools that need more memory than the
default to start reliably. Set `sandbox.qemuStoreCacheMmap` when the tool's
config store needs 9p `cache=mmap` (for example SQLite WAL shared-memory files).

`check-update.sh` runs in a controlled containerized probe environment, never directly on the host. Contract:
- Exit `0` with non-empty stdout: valid upstream fingerprint.
- Empty stdout or non-zero exit: fingerprint unknown, no automatic rebuild.
- The fingerprint is compared against the last successful stored fingerprint in host build state. A changed fingerprint marks the tool for automatic update on the next build; unchanged fingerprints do not.

If a tool supports skills, set `sandbox.skillsDir` to a path below
`sandbox.configDir` (both may be home-relative or absolute). Each immediate
child of a skill source is one named skill directory. Enclave composes the
native skills path in this order, from lowest to highest precedence:

1. Built-in tool-extension skills: `extensions/tools/<tool>/skills/`
2. User-global tool-extension skills:
   `~/.config/enclave/extensions/tools/<tool>/skills/`
3. Allow-listed host config, when `host_config=passthrough`
4. Global shared skills: `~/.config/enclave/skills/<skill>/`
5. Global tool-specific skills:
   `~/.config/enclave/tools/<tool>/<relative-skills-path>/<skill>/`
6. Project shared skills: `~/.config/enclave/projects/<hash>/skills/<skill>/`
7. Project tool-specific skills:
   `~/.config/enclave/projects/<hash>/<tool>/config/<relative-skills-path>/<skill>/`

Host passthrough (layer 3) delivers host skills for every built-in tool with
skill support: each reviewed `passthroughPaths` allow-list includes the tool's
skills path (`skills/` for most tools, `agent/skills/` for pi). Opt out for a
specific tool under `tool_overrides.<tool>.host_config_paths`; for example:

```json
{
  "tool_overrides": {
    "claude": {
      "host_config_paths": ["default", "-skills/"]
    }
  }
}
```

At session start the log lists exactly which allow-listed paths pass through.

For tool-specific sources (layers 5 and 7), `<relative-skills-path>` mirrors
`sandbox.skillsDir` relative to `sandbox.configDir`. For example, a skill
named `review` for Claude (`skillsDir: .claude/skills`) lives at
`~/.config/enclave/tools/claude/skills/review/`, and for Pi
(`skillsDir: .pi/agent/skills`) at
`~/.config/enclave/tools/pi/agent/skills/review/`. A higher-precedence
same-named skill replaces the complete lower-precedence directory. In
particular, project shared skills override global tool-specific skills, and
project tool-specific skills override both. The effective skills are mounted
at `sandbox.skillsDir`. The same layout and precedence are documented with
host paths in [Configuration](../configuration.md#managed-skills) and
[persistent stores](../runtime/stores.md#managed-skills).

Shared skills must use portable Agent Skills frontmatter: required `name` and
`description`, with optional `license`, `compatibility`, and `metadata` only.
The name must match the skill directory. Invalid shared skills warn and are
skipped, so one bad source does not prevent a tool session from starting.
Harness-specific metadata belongs in a tool-specific skill and is validated by
the selected harness. Built-in duplicated skills remain per-tool so they can
carry harness-specific metadata. Tools without `sandbox.skillsDir` ignore all
shared sources.

Canonical host-side tool config overrides live under `~/.config/enclave/tools/<tool>/` (global) and `~/.config/enclave/projects/<hash>/<tool>/config/` (project).

Generic JSON/TOML patches mirror native config paths under `~/.config/enclave/patches/<tool>/` and `~/.config/enclave/projects/<hash>/patches/<tool>/`. Patches require `sandbox.configDir`, and each target must exist in a lower-precedence layer. Resolution layers built-in config, host passthrough, global full-or-patch, then project full-or-patch. Defining a full file and patch for the same path at one scope is an error.

### Credentials, service auth, and providers

Secrets are split across two `spec.yaml` sections:

- `credentials.sources.<id>` declares the credential itself: `env` (one or
  more env-var aliases for the same credential) and the enclave-native
  `apiKey` bool (`false` for OAuth/session tokens; omitted/`true` means it's
  an API key).
- `credentials.sources.<id>.file` optionally sources the secret from a host
  file: `path` (supports `~`) plus a `parser` — empty for the trimmed raw file
  contents, or `json:<dot.path>` (e.g. `json:auth.token`) to extract a scalar
  from a JSON document. The dot-path is split on `.`; it is **not** an RFC
  6901 JSON Pointer. A malformed parser is a load error, but a missing file —
  or a file that does not (yet) contain the key — quietly falls back to the
  env aliases.
- `credentials.sources.<id>.priority` orders the two: `env-first` (the
  default) consults the env aliases before the file, `file-first` the reverse.
- `network.serviceDomains` (`host -> service-id`) plus
  `network.serviceAuth.<service-id>` (`headerName`, optional `valueFormat`)
  describe how the gateway injects that credential as an HTTP header when it
  proxies requests to those hosts. The service-id in `serviceAuth` and
  `serviceDomains` is the same key used under `credentials.sources`.

The placeholder convention is Go `fmt`-style `%s`, not `{secret}`. An empty
`valueFormat` means "inject the raw secret value" with no wrapping (see
`anthropic-api-key`'s `x-api-key` header above, which has no `valueFormat`);
a non-empty `valueFormat` must contain `%s` (see `"Bearer %s"` in the
`github-cli` example below, or codex's `openai-api-key: { headerName:
authorization, valueFormat: "Bearer %s" }`).

`network.serviceAuth.<id>.hosts` is a **enclave-native superset** over sbx:
it lets a service declare its own hosts directly, which `serviceDomains`
(a single `host -> service-id` mapping) cannot express when multiple
services share the same host. `gitlab-cli` uses this — three tokens
(`gitlab-token`, `gitlab-oauth-token`, `gitlab-job-token`) are all valid on
`gitlab.com`:

```yaml
network:
  serviceAuth:
    gitlab-token:       { headerName: private-token, hosts: [gitlab.com, "*.gitlab.com"] }
    gitlab-oauth-token:  { headerName: authorization, valueFormat: "Bearer %s", hosts: [gitlab.com, "*.gitlab.com"] }
    gitlab-job-token:    { headerName: job-token, hosts: [gitlab.com, "*.gitlab.com"] }
```

When the gateway is enabled, credentials with a `serviceAuth` entry are
replaced with random placeholders in the container environment; the gateway
MITM proxy intercepts HTTPS requests to the declared hosts and rewrites the
configured header with the real secret, denying plaintext HTTP requests that
carry a placeholder. Credentials without a `serviceAuth` entry are injected
as normal env vars.

`providers[]` is enclave-native and describes an auth *provider* (as
opposed to a raw credential): `name`, `credentials` (a list of
`credentials.sources` keys), `authFiles` (paths relative to `sandbox.configDir`),
`authSession` (`mode`: `any`/`all`, plus `checks`), `oauthPorts`, and
`securestorageDirEnv`.

Auth session detection is provider-specific and defaults to "any auth file exists" for that provider. For multi-provider auth files, use `providers[].authSession` with `mode` (`any` or `all`) and `checks`:
- `file_exists`: require a file to exist.
- `json_pointer`: require a JSON Pointer (RFC 6901) to resolve in a JSON file (null counts as present).
- `json_pointer_non_null`: require a JSON Pointer (RFC 6901) to resolve to a non-null value.
OAuth callback port mapping is defined per provider in `providers[].oauthPorts`.

A provider may set `providers[].securestorageDirEnv` to the name of an environment variable the tool reads to locate its credential-storage directory. In the `shared` auth scope, enclave sets it to the shared auth store mount so the tool writes its credential file there directly. Claude uses `CLAUDE_SECURESTORAGE_CONFIG_DIR` so concurrent sessions coordinate OAuth refresh-token rotation natively (see [persistent stores](../runtime/stores.md) and [authentication](../auth.md)).

`--no-api-key` suppresses provider credentials whose `credentials.sources.<id>.apiKey` is unset/`true`; it does not suppress credentials with `apiKey: false`.

Session continuation arguments are declared under `sandbox`:
- `continueArgs`: args appended after the command when the user runs `enclave continue`; should target the latest session.
- `resumeArgs`: args appended after the command when the user runs `enclave resume`; should open a session picker/list when the tool supports it.
- Fallbacks are one-step only (no loops):
  - `continue` falls back to `resumeArgs` if `continueArgs` is missing.
  - `resume` falls back to `continueArgs` if `resumeArgs` is missing.
  - If both are missing, both commands are unsupported for that tool.

### Build Selection

Runtime images are per-tool: the CLI builds and runs exactly one tool image per
session, selected with `--tool <name>` (default: `claude`). Each tool gets its
own image tagged `enclave-<tool>:...`, so rebuilding or updating one tool
never invalidates another tool's image.

```bash
enclave --rebuild                  # build/run the default tool (claude)
enclave --tool codex --rebuild     # build/run the codex image
```

Internally the CLI sets the `AGENT_TOOLS` build arg to the single selected tool,
and `enclave-install-tool` installs just that tool. A tool with
`defaultIncluded: false` is opt-in: it is still selectable with `--tool <name>`,
but it is excluded from the direct-Docker fallback's default set (see below).

Direct `docker build .` also works for quick testing (see
[Developer Testing](#developer-testing-direct-docker-builds)), but without
per-tool layer caching. The raw Dockerfile defaults `AGENT_TOOLS=all`, which
installs every tool whose `defaultIncluded` is omitted or set to `true`.

## Feature Extensions

Feature extensions provide development tools and capabilities that work with all tools.

### Required Files

| File | Purpose |
|------|---------|
| `spec.yaml` | Extension spec: `kind: mixin`, optional `aptPackages` |

Mixin specs may also declare `configDir`, `authFiles`, and `credentials`/`network` for shared tooling such as `github-cli` or `gitlab-cli`:

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

A mixin's `network` block is honored in full: its
`allowedDomains`/`deniedDomains` and its `serviceDomains`/`serviceAuth`
credential injection apply to the session alongside the tool spec's own, and
every host the proxy injects credentials for is unioned into the session
allow set so it resolves. A service mixin thus declares its own reachability
without help from the tool spec.

### Optional Files

| File | Purpose |
|------|---------|
| `install.sh` | Installation script (runs as root if `needsRoot: true`) |
| `feature-entrypoint.d/*.sh` | Scripts sourced at startup for ALL tools |

### Feature Selection

Configure which features to install via `~/.config/enclave/config.json` (global) or `~/.config/enclave/projects/<hash>/config.json` (project):

```json
{
  "features": ["github-cli", "python-dev", "devtools"]
}
```

When `features` is not specified, all `defaultEnabled` features are installed (default). An empty array `[]` disables all features.
Opt-in features require an explicit list; additive-only entries do not change the implicit default.

**Devcontainer mode:** When devcontainer mode is used (`devcontainer run` or config `devcontainer: true`), features are disabled by default (the devcontainer already defines its own environment). To install features alongside a devcontainer, pass `--features` explicitly.

The next time you run `./enclave --rebuild`, only the specified features will be installed.

**Available features:** `devtools`, `github-cli`, `gitlab-cli`, `node-dev`, `playwright`, `python-dev`, `debug-tools`, `shell-extras`

**Opt-in features (not installed unless explicitly listed):** `debug-tools`, `gitlab-cli`, `playwright`, `shell-extras`

### Installation Order

Features are installed in priority order (lower `priority` numbers first; default
100, ties broken by name).

The CLI generates **one source-copy + install block per selected feature**: copy
that feature's directory, make it readable/executable, install its apt packages,
then run its `install.sh` in the appropriate root/user phase if present. Adding,
removing, or changing one feature only invalidates layers at or after its
position — the other features' copies, apt installs, and scripts stay cached and
are not re-run.

A direct `docker build .` (no CLI) uses an aggregated fallback instead: all apt
packages installed together, then all root scripts, then all user scripts. This
is simpler but re-runs every feature whenever the selection changes (see
[Developer Testing](#developer-testing-direct-docker-builds)).

Either way, a feature must be **self-contained**: its `install.sh` may rely only
on its own `aptPackages` (or its own downloads), never on packages contributed
by another feature, since per-feature ordering no longer guarantees that every
other feature's apt packages are present first.

### Feature install failure behavior

- Default behavior: feature `install.sh` failures are warnings (build continues).
- `ENCLAVE_FEATURE_INSTALL_STRICT=1`: all feature install failures are fatal.
- Per-feature override: set `failOnInstallError: true` in `spec.yaml` to make that feature's install failure fatal even when strict mode is disabled.

### Available Features

| Feature | Priority | Description |
|---------|----------|-------------|
| `devtools` | 40 | Core tools + linters (vim, htop, ripgrep, golang-go, shellcheck, golangci-lint, gosec). |
| `github-cli` | 50 | GitHub CLI (gh) |
| `gitlab-cli` | 50 | GitLab CLI (glab) (opt-in) |
| `node-dev` | 70 | Node.js dev tools: typescript, eslint, prettier |
| `python-dev` | 70 | Python dev tools: black, ruff, mypy, pytest |
| `playwright` | 75 | Playwright browsers and MCP server for UI testing (opt-in) |
| `debug-tools` | 80 | Debug tools: gdb, strace, ltrace, tcpdump (opt-in) |
| `shell-extras` | 90 | Shell enhancements: zsh, oh-my-zsh, direnv (opt-in) |

## Key Differences: Tools vs Features

| Aspect | Tool (`kind: sandbox`) | Feature (`kind: mixin`) |
|--------|------|---------|
| `spec.yaml` `sandbox` block | Required | N/A |
| `gateway-allowlist.conf` | Required | N/A (uses tool's network) |
| `entrypoint.d/` | Runs only for this tool | N/A |
| `feature-entrypoint.d/` | N/A | Runs for ALL tools |
| Selection | `--tool` (per-tool image; internal `AGENT_TOOLS`) | `FEATURES` build arg |
| Selectable at runtime | Yes (`--tool`) | No (build-time only) |
| `needsRoot` | N/A | Controls install user |
| `aptPackages` | N/A | Auto-installed apt packages |

## How It Works

### Build Time (Dockerfile)

The Dockerfile defines a single final stage (`standard`) on top of several
internal build stages:

```text
system -> tool-base -> feature-base -> tool-* -> standard (final image)
```

| Stage | Description |
|-------|-------------|
| `system` | Base system packages and the non-root `agent` user |
| `tool-base` | Shared build deps, the private agent Node runtime, helper binaries |
| `feature-base` | One source-copy + install block per selected feature, priority ordered; the per-feature cache boundary |
| `tool-*` | Per-tool install stage (one per selected tool); the per-tool cache boundary |
| `standard` | Final image: copies the selected tool stage(s) onto `feature-base`, then templates + docs. Tagged `:latest`; `--slim` builds the same stage with no features |

**Build process:**

1. Effective tool/feature selection is resolved from the current build options.
2. Only the selected extension trees are staged into the runtime image build context (with user overrides merged onto built-ins).
3. Docker weaving scripts are copied to `/opt/enclave/build-scripts/` from `runtime-assets/build-scripts/`.
4. `tool-base` installs shared build deps, the private agent Node runtime, and helper binaries.
5. `feature-base` copies and installs each selected feature in its own priority-ordered block (source tree, apt packages, then its `install.sh` in the root/user phase), so changing one feature does not re-run the others. (Direct `docker build .` falls back to aggregated `FEATURES`-aware phases.)
6. Before the rebuild gate, tools with `check-update.sh` may be probed in a containerized environment when the automatic update interval has elapsed.
7. Generated `tool-*` stages run `install.sh` via `enclave-install-tool` for the selected tool set.
8. `standard` starts from `feature-base`, copies in outputs from the selected tool stages, then aggregates templates and bundled docs.

Feature selection semantics:
- `FEATURES=default`: install only features with `defaultEnabled` omitted/true.
- `FEATURES=all`: install every feature (including opt-in).
- `FEATURES="name1 name2"`: install only listed features.
- `FEATURES=""`: install no features.

Tool selection semantics (`AGENT_TOOLS` is an installer-internal build arg; the
CLI sets it to the single selected tool):
- `AGENT_TOOLS=<tool>`: install just that tool (what the CLI emits per image).
- `AGENT_TOOLS="name1 name2"`: install only the listed tools.
- `AGENT_TOOLS=all`: install every tool with `defaultIncluded` omitted/true (the direct-Docker fallback default).

### Runtime (entrypoint.sh)

1. Determines current tool from `$TOOL` env var
2. Sources scripts from `extensions/tools/{tool}/entrypoint.d/*.sh` (tool-specific)
3. Sources scripts from `extensions/*/feature-entrypoint.d/*.sh` (all features)

### Go Registration (Tools Only)

1. Extensions with `go/` directory are imported via `go generate ./cmd/enclave` (outputs `cmd/enclave/tool_imports.go`)
2. `init()` functions register hooks via `auth.RegisterHooks()` and handlers via `tools.RegisterHandler()`

### Auth Hook Phases (Tools Only)

Hooks run in a fixed order during runtime auth preparation:

- `OnAuthReady`: runs after auth stores are prepared; use `VolumeHasSession` to gate behavior (true when any provider session is detected via `providers[].authSession` or auth file presence)
- `AfterEnvInjected`: runs after API keys are injected as env vars
- `FinalizeAuth`: last chance to adjust config stores before the container starts

## Framework Integration

- `internal/model/types.go`: `Extension` struct with `IsMixin()` and `IsSandbox()` methods
- `internal/config/extension.go`: Extension loading and filtering functions
- `internal/config/profile.go`: `ListProfiles()` returns only tool extensions
- `internal/runtime/network_manager.go`: Loads DNS allowlists from `extensions/tools/{tool}/gateway-allowlist.conf`

## Adding a New Tool Extension

1. Create `extensions/tools/{tool}/` directory
2. Add `spec.yaml`:
   ```yaml
   schemaVersion: "1"
   kind: sandbox
   name: mytool
   description: My AI tool
   sandbox:
     entrypoint: { run: [mytool] }
   ```
3. Add required files:
   - `sandbox`, `credentials`, `network`, `providers` fields in `spec.yaml` as needed (see the claude example above)
   - `gateway-allowlist.conf` (can include fragments via `conf-file=`)
   - `install.sh` with installation commands
4. Optionally add:
   - `check-update.sh` to opt into automatic update probes
   - `templates/` for settings files
   - `entrypoint.d/setup.sh` for container initialization
   - `go/` for custom hooks/handlers (requires recompiling binary)
5. If adding Go code, run `go generate ./cmd/enclave` to refresh tool imports

## Adding a New Feature Extension

1. Create `extensions/features/{feature}/` directory
2. Add `spec.yaml`:
   ```yaml
   schemaVersion: "1"
   kind: mixin
   name: myfeature
   description: My development tools
   aptPackages: [tool1, tool2]
   priority: 70
   ```
3. Optionally add:
   - `install.sh` for custom installation (set `needsRoot: true` if it needs root)
   - `feature-entrypoint.d/setup.sh` for runtime initialization

### Feature Guidelines

- **Priority**: Lower values install earlier. Use 40-60 for core tools, 70-80 for language tools, 90+ for shell/UI
- **aptPackages**: Prefer apt packages over install.sh when possible
- **needsRoot**: Only set `true` if the install script requires root (e.g., adding apt repos)
- **feature-entrypoint.d/**: Use sparingly; these scripts run on every container start

## Testing & Verification

Use the following steps to verify the extension system is working correctly.

### 1. Go Code Verification

```bash
# Refresh generated tool imports
go generate ./cmd/enclave

# Build CLI and verify no errors
go build ./cmd/enclave

# Validate extension metadata
./enclave validate-extensions

# Verify tool loading works
./enclave --help
./enclave --tool claude    # Should recognize claude as valid tool
```

### 2. Build and Run with enclave

```bash
# Build and run with default tool (claude)
# This builds the full image with all default-enabled features
./enclave --rebuild

# Run with a specific tool
./enclave --tool codex
```

### 3. Verify Features in Container

```bash
# Start a shell session
./enclave --tool claude

# Inside container, verify features are installed:

# Default-enabled features:
# GitHub CLI (github-cli feature)
gh --version

# Python tools (python-dev feature)
black --version
ruff --version

# Node.js tools (node-dev feature)
tsc --version
eslint --version

# Dev tools (devtools feature)
vim --version
rg --version       # ripgrep

# Opt-in features (if enabled):
# GitLab CLI (gitlab-cli feature)
glab --version

# Debug tools (debug-tools feature)
which gdb
which strace

# Shell extras (shell-extras feature)
zsh --version
type direnv
```

### 4. Verify Feature Entrypoints

```bash
# Inside container, verify feature-entrypoint.d scripts ran:

# Shell-extras feature (opt-in) should have set up direnv hooks
# (check ~/.bashrc or ~/.zshrc for direnv hook)
```

### 5. Verify Tool-Specific Entrypoints

```bash
# Tool entrypoint.d scripts run only for the selected tool
./enclave --tool claude
# Claude-specific setup should have run

./enclave --tool codex
# Codex-specific setup should have run
```

### 6. Test Minimal Images

```bash
# Test agents-only image (no features)
./enclave --slim --rebuild

# Inside container:
claude --version   # Should work (tool installed)
gh --version       # Should fail (feature not installed)
vim --version      # Should fail (only vim-tiny)

# Devcontainer mode also disables features by default
./enclave devcontainer run --rebuild

# Inside container:
gh --version       # Should fail (feature not installed)

# Opt in to features explicitly with --features
./enclave devcontainer run --features github-cli --rebuild
```

### 7. Verify `spec.yaml` Is Required

`spec.yaml` (or `spec.json`) is mandatory and its `kind`/`name` fields must
match the extension's location.

```bash
# Temporarily remove spec.yaml from a tool
mv extensions/tools/claude/spec.yaml /tmp/
go build ./cmd/enclave
./enclave --tool claude --help   # Should fail: spec.yaml not found
mv /tmp/spec.yaml extensions/tools/claude/
```

### Developer Testing (Direct Docker Builds)

For quick testing during development, you can build the Dockerfile directly.
The raw Dockerfile includes a fallback that installs the default-included tool
set, but without the per-tool layer caching that the CLI provides:

```bash
# Build with all default-enabled features (default)
docker build -t enclave:latest .

# Build with specific features only (including opt-in)
docker build \
  --build-arg FEATURES="github-cli python-dev" \
  -t enclave:selective .

# Verify selective build
docker run --rm enclave:selective gh --version      # Works
docker run --rm enclave:selective glab --version     # Fails (not installed)

# Watch feature installation order
docker build --progress=plain . 2>&1 | \
  grep -E "(Installing feature|Feature .+: adding)"
```

For production builds with per-tool layer caching, use the CLI:

```bash
enclave --rebuild
```

### Quick Verification Checklist

| Test | Command | Expected |
|------|---------|----------|
| Go builds | `go build ./cmd/enclave` | No errors |
| Default run | `./enclave` | Starts claude in container |
| Tool select | `./enclave --tool codex` | Starts codex |
| gh installed | (in container) `gh --version` | Shows version |
| glab installed (opt-in) | (in container) `glab --version` | Shows version when enabled |
| Python tools | (in container) `black --version` | Shows version |
| Node tools | (in container) `tsc --version` | Shows TypeScript |
| direnv hook | (in container) `type direnv` | Found |
| Slim image | `./enclave --slim` | No features installed |
| Devcontainer | `./enclave devcontainer run` | No features installed |
| Devcontainer + features | `./enclave devcontainer run --features github-cli` | Only github-cli installed |
