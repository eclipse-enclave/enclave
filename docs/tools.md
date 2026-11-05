# Tools and images

Which agents Enclave ships, how their images are built, named, and updated, and
how to control builds and caches.

## Built-in tools

```bash
enclave tools      # List available tool profiles
enclave features   # List available feature extensions
```

| Tool | Description |
|------|-------------|
| `claude` | [Claude Code](https://www.anthropic.com/claude-code) (Anthropic) |
| `codex` | [Codex CLI](https://github.com/openai/codex) (OpenAI) |
| `copilot` | [GitHub Copilot CLI](https://github.com/features/copilot/cli) (GitHub) |
| `opencode` | [OpenCode](https://opencode.ai/) |
| `pi` | [Pi](https://github.com/earendil-works/pi) |
| `theia` | Host [Eclipse Theia](https://theia-ide.org/) desktop IDE attached to an Enclave container |
| `theia-next` | Preview [Theia Next](https://theia-ide.org/) desktop IDE attached to an Enclave container |

These six (Theia counting with its `theia-next` preview) change only by a vote
of the Eclipse Enclave committers; see
[CONTRIBUTING.md](../CONTRIBUTING.md#where-it-goes). Every other tool lives in
[enclave-extensions](https://github.com/eclipse-enclave/enclave-extensions):

```bash
enclave tools add eclipse-enclave/enclave-extensions --name <tool>
```

The tool is chosen once and saved as the `tool` key; IDE profiles are selected
with `--tool theia`. See [Tool selection](cli-reference.md#tool-selection).
Profiles live in `extensions/tools/<tool>/spec.yaml` (`kind: sandbox`) and
declare the command, config and skills directories, QEMU settings, and auth
providers; see [Extensions](extensions/README.md).

## Base images

The default base image is `debian:trixie-slim`. Override it with
`--base-image` or `base_image`, e.g. `enclave --base-image ubuntu:24.04`.

- The Dockerfile assumes a Debian/Ubuntu-style base (apt, bash).
- An existing UID 1000 user (e.g. `node` in `node:22`) is renamed to `agent`,
  with a symlink from its original home to `/home/agent`. Home-relative tool
  configs keep working; hardcoded absolute paths outside home do not.

## Devcontainer support

```bash
enclave devcontainer run         # Start from devcontainer.json
enclave devcontainer shell       # Interactive shell from devcontainer.json
enclave devcontainer generate    # Generate .devcontainer/devcontainer.json
```

Or set `"devcontainer": true`. Enclave reads `.devcontainer/devcontainer.json`
or `devcontainer.json` in the project root.

- Mutually exclusive with `--base-image`.
- Runs on `docker` and `podman`, not `qemu`; see
  [Podman devcontainers](backends.md#devcontainers).
- Enables no Enclave features unless you pass `--features` or set `features`.
- Bind sources must lie inside the project; in read-only project mode,
  devcontainer project mounts are read-only. See [Mounts](mounts.md).

| `devcontainer.json` field | Handling |
|---------------------------|----------|
| `workspaceFolder`, `workspaceMount`, `containerEnv`, `mounts`, `postCreateCommand`, `postStartCommand` | Applied (best effort) |
| `runArgs` | Applied (best effort). A `--security-opt` survives only if it keeps the default confinement (`no-new-privileges`, `label=type:container_t`, podman's `mask=`); others, including custom seccomp profiles and SELinux types, are dropped with a warning |
| `remoteUser` | Honored for shell-only sessions; agent containers use the internal user unless `--use-remote-user` is set |
| `dockerComposeFile`, `features`, `remoteEnv`, `initializeCommand`, `onCreateCommand`, `updateRemoteUserUID` | Ignored with a warning |

## Image variants and naming

Each tool gets its own image.

| Build | Image tag |
|-------|-----------|
| Default (tool plus default features) | `enclave-<tool>:latest` |
| `--slim` (tool only) | `enclave-<tool>:slim` |
| Explicit `features` or `--features` | `enclave-<tool>:custom-<hash>-latest` (slim: `custom-<hash>-slim`) |
| `--base-image` | `enclave-<tool>:base-<hash>-latest` (slim: `base-<hash>-slim`) |
| Devcontainer mode | `enclave-<tool>:devcontainer-<hash>-latest` (slim: `devcontainer-<hash>-slim`) |
| `--image-name <name>` | Used as given; disables all derivation above |

When Enclave runs from a checkout of its own sources on a non-default branch,
the default and slim tags get a `branch-<name>-<hash>-` prefix so they do not
overwrite default-branch images. Podman stores images as
`localhost/enclave-<tool>:...`.

## Node runtime

Node-based agent CLIs run on a private Node at `/opt/enclave/node`, pinned in
`Dockerfile` via `AGENT_NODE_IMAGE`. User shells and project commands use the
`node`/`npm` from the base image or nvm.

## Agent updates

```bash
enclave update              # Rebuild the default tool's image, then exit
enclave update codex        # A specific tool
enclave update claude codex # Several tools
```

`update` always rebuilds and starts no session. Without arguments it targets
the resolved default tool. It accepts the run's build flags (`--slim`,
`--features`, `--base-image`, …) to refresh the matching image variant, and
`--backend` to select the engine.

Automatic updates apply only to tools with
`extensions/tools/<tool>/check-update.sh`. Once the interval has elapsed (24
hours by default; `ENCLAVE_AGENT_UPDATE_INTERVAL_HOURS=0` probes on every
rebuild-eligible run), a run executes the hook in a probe container and
rebuilds only on a changed upstream fingerprint. A normal run never forces an
update. Tools without the hook refresh on `enclave update`, `--rebuild`, or any
other rebuild.

`enclave tools|features|commands update` refresh extension sources, not
images.

## Build cache

The image rebuilds when the Dockerfile template, entrypoint, target, base image
or devcontainer, or features change; when an update probe reports a new
fingerprint; or on `--rebuild` and `enclave update`.

### Suppressing rebuilds

`--no-rebuild` skips all runtime and gateway image builds and update probes for
one invocation, uses existing local images, and fails fast if one is missing.

`export ENCLAVE_NO_REBUILD=1` does the same for every session command (`run`,
`shell`, `continue`, `resume`, devcontainer run/shell, user session commands),
including QEMU bundles.

- Non-building commands ignore it; `enclave config` reports it.
- `--rebuild` and `enclave update` are rejected while it is set; extension
  source updates are unaffected.
- Override it with `unset ENCLAVE_NO_REBUILD`, `ENCLAVE_NO_REBUILD=0`, or
  `--no-rebuild=false`. There is no config key.

### Package caches

`--no-cache` disables the package cache mounts from `~/.cache/enclave/`, not the
Docker build cache. Container-local caches still work and are discarded with
the container.

The pnpm store is pinned to `~/.local/share/pnpm/store` (persistent unless
`--no-cache`), so pnpm cannot create `.pnpm-store` in the project. It is set via
`PNPM_CONFIG_STORE_DIR` (pnpm 11+) and `store-dir` in pnpm's global config
(pnpm 9 and 10), which project pnpm config overrides. Store and project are
separate mounts, so pnpm copies instead of hard-linking.

### Image build cache

Builds use `docker build` with inline cache. `--cache-from` (or `cache_from`)
adds sources; the tool image's own prior build is used automatically. All
builds need the buildx plugin (the Dockerfile uses `RUN --mount`); without it
Enclave fails before building and says how to install it.

```bash
enclave --buildx-cache-dir ~/.cache/enclave/buildx --rebuild shell -- -lc true
```

| Flag | Effect |
|------|--------|
| `--buildx-cache-dir <path>` | Build with buildx, import the local cache if populated, export a `type=local,mode=max` cache |
| `--buildx-cache-from <spec>`, `--buildx-cache-to <spec>` | Raw buildx cache specs, e.g. registry or shared-directory targets |

Podman ignores these flags; see [Podman image builds](backends.md#image-builds).

### Build UID and GID

Images bake the host UID/GID. For shared event artifacts, bake a canonical
identity and remap at runtime:

```bash
enclave --build-uid 1000 --build-gid 1000 --runtime-uid-remap --rebuild shell -- -lc true
```

The image hash includes the effective build UID/GID, so an image built for
another identity is rebuilt. Runtime remap is incompatible with a devcontainer
`remoteUser` applied to shell sessions.

## Runtime assets

DNS allowlists in `runtime-assets/gateway-allowlists/` are baked into the
image. Tool settings templates in `extensions/tools/<tool>/templates/` are
copied to `/usr/local/share/enclave/templates/` in the container; see
[Tool config](tool-config.md).
