# Extensions

Extensions add agents, development tools, and host commands to Enclave.

| Page | For |
|------|-----|
| [Installing extensions](installing.md) | Installing, updating, and removing extensions from git |
| [Spec reference](spec-reference.md) | Every `spec.yaml` field and sibling file |
| [Credentials and providers](credentials.md) | Secrets, gateway header injection, placeholders, auth detection |
| [Adding a tool](adding-a-tool.md), [Adding a feature](adding-a-feature.md) | Step-by-step guides |
| [Custom commands](../commands.md) | Host and session commands, including extension-shipped ones |
| [JSON output](json-output.md) | `list --json` and the `add`/`update`/`remove` result envelope |
| [DEV.md](../DEV.md#testing-extensions) | Testing the extension system itself |

## Extension kinds

| Kind | `kind` | Purpose | Built-in location |
|------|--------|---------|-------------------|
| Tool | `sandbox` | A runnable AI coding agent (Claude, Codex, OpenCode, ...) | `extensions/tools/<name>/` |
| Feature | `mixin` | Development tools added to the tool image (GitHub CLI, Python tools, ...) | `extensions/features/<name>/` |
| Command | `command` | Host commands only, no container side | None; user root only |

## Built-in features

| Feature | Priority | Default | Contents |
|---------|----------|---------|----------|
| `devtools` | 40 | on | General command-line and network tools (vim, htop, ripgrep, shellcheck, dig) |
| `golang-dev` | 45 | opt-in | Go toolchain and delve, from Debian |
| `github-cli` | 50 | on | GitHub CLI (`gh`) |
| `gitlab-cli` | 50 | opt-in | GitLab CLI (`glab`) |
| `node-dev` | 70 | on | Node.js dev tools: nvm, typescript, yarn, pnpm |
| `python-dev` | 70 | on | Python dev tools: black, ruff, mypy, pytest, pre-commit |
| `theia-dev` | 75 | opt-in | Headers for building Theia and other Electron apps: libx11, libxkbfile, libsecret |

Select features with the `features` key or `--features`
([List options](../configuration.md#list-options)). Built-in tools are listed
in [Tools](../tools.md). More tools and features are in
[enclave-extensions](https://github.com/eclipse-enclave/enclave-extensions):

```bash
enclave tools add eclipse-enclave/enclave-extensions --name <tool>
enclave features add eclipse-enclave/enclave-extensions --name <feature>
```

## Extension sources

| Root | Contents |
|------|----------|
| `extensions/` in this repository | Built-in extensions |
| `~/.config/enclave/extensions/` | User-global extensions, where `add` installs; per file, these win over built-ins |

Project-local definitions (for example `<project>/.enclave/extensions/`) are
never loaded, so opening a repository cannot supply install or startup code.

To use a custom extension in one project only:

- Tool: `enclave --tool <tool>`, or `tool` in the project config.
- Feature: declare it with `defaultEnabled: false`, then add
  `"features": ["+my-feature"]` to
  `~/.config/enclave/projects/<hash>/config.json`
  ([Configuration](../configuration.md)).

## Directory layout

```
extensions/
├── tools/
│   └── claude/
│       ├── spec.yaml               # Manifest: metadata, sandbox, network, credentials, providers
│       ├── gateway-allowlist.conf  # DNS allowlist (falls back to base.conf if missing)
│       ├── install.sh              # Runs during the image build
│       ├── check-update.sh         # Optional: upstream fingerprint for automatic updates
│       ├── templates/settings.json # Settings template
│       ├── config-base/            # Optional: seed files for the config store
│       ├── entrypoint.d/setup.sh   # Sourced at startup, only for this tool
│       ├── skills/                 # Optional: agent skills
│       ├── commands/host/          # Optional: host commands (user root only)
│       └── go/                     # Optional: auth hooks and handler, compiled into the binary
└── features/
    ├── github-cli/
    │   ├── spec.yaml
    │   └── install.sh
    └── node-dev/
        ├── spec.yaml
        ├── install.sh
        └── feature-entrypoint.d/setup.sh  # Sourced at startup for every tool
```

Only `spec.yaml` is the manifest; the other entries are sibling files (see
[tool files](spec-reference.md#tool-files) and
[feature files](spec-reference.md#feature-files)). The build scripts in
`runtime-assets/build-scripts/` read enablement, `priority`, `needsRoot`,
`aptPackages`, `failOnInstallError`, and `commands.install` from `spec.yaml`
(or `spec.json`) with `yq`.

## Tools and features compared

| Aspect | Tool (`kind: sandbox`) | Feature (`kind: mixin`) |
|--------|------------------------|-------------------------|
| `sandbox` block | Required | Not used |
| `gateway-allowlist.conf` | Ships one; falls back to `base.conf` | Not used; declares domains in `network` |
| Startup scripts | `entrypoint.d/`, only for this tool | `feature-entrypoint.d/`, for every tool |
| Selection | `--tool` or `tool`; one image per tool | `features` or `--features`; rebuilds on the next run |
| `needsRoot`, `aptPackages` | Not used | Install user; apt packages |
| Declared `ports` | Published for every session of the tool | Published when the feature is enabled |
| `credentials`, `network` | Yes | Yes |

## Host commands

Every extension kind may ship `commands/host/` executables, which become
`enclave <name>` verbs on the host. They are read only from extensions under
`~/.config/enclave/extensions/`, never from built-ins. See
[Custom commands](../commands.md#commands-from-extensions).

## Command extensions

A command extension (`kind: command`) ships host commands and nothing else.
Use it for a command with no container side, such as an `enclave usage` that
reads persisted sessions on the host. A feature would be listed, enabled, and
built into the image for no purpose.

```
usage/
├── spec.yaml
├── README.md
└── commands/host/usage     # chmod +x, becomes `enclave usage`
```

```yaml
schemaVersion: "1"
kind: command
name: usage
displayName: Session usage
description: Token usage across persisted sessions
```

- The spec accepts only `schemaVersion`, `kind`, `name`, `displayName`, and
  `description`. Any other field is a load error, including the `commands:`
  key, which is unrelated to the `commands/` directory.
- At least one executable in `commands/host/` is required
  ([rules](../commands.md#shipping-host-commands-in-an-extension)). Runtime
  files such as `install.sh`, `files/`, or `skills/` are reported as ignored.
- Manage them with `enclave commands list|add|update|remove`
  ([Installing extensions](installing.md)). They install into
  `~/.config/enclave/extensions/commands/<name>/`, never reach the build
  context or image hash, and `add` prints no rebuild or enable hint: installed
  means available.
- They are never built in. `kind: command` is Enclave's own; sbx kits have no
  host commands.

## How it works

### Build time

```text
system -> tool-base -> feature-base -> tool-* -> standard (final image)
```

| Stage | Contents |
|-------|----------|
| `system` | Base system packages and the non-root `agent` user |
| `tool-base` | Shared build deps, the private agent Node runtime, helper binaries |
| `feature-base` | One copy-and-install block per selected feature, in priority order: source tree, apt packages, `install.sh` in the root or user phase. Changing one feature does not re-run the others ([installation order](spec-reference.md#installation-order)). |
| `tool-*` | Generated per selected tool; runs `install.sh` through `enclave-install-tool` |
| `standard` | Final image: the tool stage output on `feature-base`, plus templates and bundled docs. Tagged `:latest`; `--slim` builds it with no features |

Before building, the CLI resolves the tool and feature selection, stages only
the selected extension trees (user overrides merged onto built-ins) into the
build context, and copies the build scripts to `/opt/enclave/build-scripts/`.
Tools with `check-update.sh` may be probed in a container first, when the
automatic update interval has elapsed.

Each tool gets its own image, `enclave-<tool>:...`, so rebuilding one tool
never invalidates another:

```bash
enclave --rebuild                  # build and run the selected tool
enclave --tool codex --rebuild     # build and run the codex image
```

| Build arg | Value | Installs |
|-----------|-------|----------|
| `AGENT_TOOLS` | `<tool>` | That tool (what the CLI sets per image) |
| | `"name1 name2"` | The listed tools |
| | `all` | Tools with `defaultIncluded` omitted or `true` (raw Dockerfile default) |
| `FEATURES` | `default` | Features with `defaultEnabled` omitted or `true` |
| | `all` | Every feature, including opt-in |
| | `"name1 name2"` | The listed features |
| | `""` | No features |

A direct `docker build .` has no per-tool layer caching and uses the
aggregated feature fallback ([DEV.md](../DEV.md#testing-extensions)).

### Runtime

At container start, `entrypoint.sh`:

1. Reads the tool from `$TOOL` and sources its `entrypoint.d/*.sh`.
2. Sources `feature-entrypoint.d/*.sh` of every feature enabled at build time.
3. Writes `commands.initFiles` and copies `files/workspace/**` for the tool and
   enabled features, then runs their `commands.startup` entries, so all files
   exist before any startup command runs.

### Go code (tools only)

Extensions with a `go/` directory are imported by `go generate ./cmd/enclave`
(writes `cmd/enclave/tool_imports.go`). Their `init()` functions register auth
hooks with `auth.RegisterHooks()` ([auth hooks](credentials.md#auth-hooks-go))
and handlers with `tools.RegisterHandler()`. An installed extension's `go/` is
copied but never compiled in; it requires rebuilding the `enclave` binary.

| File | Role |
|------|------|
| `internal/config/spec.go` | `specDocument`, the authoritative on-disk spec shape |
| `internal/model/types.go` | `Extension` with `IsMixin()` and `IsSandbox()` |
| `internal/config/extension.go` | Extension loading and filtering |
| `internal/config/profile.go` | `ListProfiles()` returns only tool extensions |
| `internal/network/merge.go` | `ResolveToolAllowlist` finds `gateway-allowlist.conf`, user tree first, else `base.conf` |
