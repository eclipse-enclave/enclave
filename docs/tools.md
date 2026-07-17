# Tool Profiles & Images

## Built-in Tools

```bash
enclave tools      # List available tool profiles
enclave features   # List available feature extensions
```

Built-in tool profiles:

| Tool | Description |
|------|-------------|
| `claude` | [Claude Code](https://www.anthropic.com/claude-code) (Anthropic) — default |
| `codex` | [Codex CLI](https://github.com/openai/codex) (OpenAI) |
| `gemini` | [Gemini CLI](https://github.com/google-gemini/gemini-cli) (Google) |
| `mistral-vibe` | [Mistral Vibe CLI](https://github.com/mistralai/mistral-vibe) (opt-in/experimental) |
| `opencode` | [OpenCode](https://opencode.ai/) |
| `pi` | [Pi](https://github.com/earendil-works/pi) |
| `theia` | Host [Eclipse Theia](https://theia-ide.org/) desktop IDE attached to an Enclave container |
| `theia-next` | Preview [Theia Next](https://theia-ide.org/) desktop IDE attached to an Enclave container |

Tool profiles live in `extensions/tools/<tool>/spec.yaml` (`kind: sandbox`) and declare the command, config directory, optional skills directory, QEMU microVM settings, and auth providers (API key vars, auth files, OAuth ports).

## Base Images

The default base image is `debian:trixie-slim`. Override with `--base-image`:

```bash
enclave --base-image ubuntu:24.04
```

The Dockerfile assumes a Debian/Ubuntu-style base (apt, bash).

When the base image already has a UID 1000 user (e.g. `node` in `node:22`), enclave renames it to `agent` and keeps a compatibility symlink from the original home path to `/home/agent`. This preserves home-path-based tool configs but cannot fix hardcoded absolute paths outside the home directory.

## Devcontainer Support

Derive the base image from a `devcontainer.json` in your project:

```bash
enclave devcontainer run         # Start from devcontainer.json
enclave devcontainer shell       # Interactive shell from devcontainer.json
enclave devcontainer generate    # Generate .devcontainer/devcontainer.json
```

Or set `devcontainer: true` in your config. enclave looks for `.devcontainer/devcontainer.json` or `devcontainer.json` in the project root.

`--base-image` and devcontainer mode are mutually exclusive.

Applied devcontainer settings (best effort): `workspaceFolder`, `workspaceMount`, `containerEnv`, `mounts`, `runArgs`, `postCreateCommand`, `postStartCommand`. `remoteUser` is honored for shell-only sessions; agent containers run as the internal user unless `--use-remote-user` is set.

Unsupported fields (`dockerComposeFile`, `features`, `remoteEnv`, `initializeCommand`, `onCreateCommand`, `updateRemoteUserUID`) are ignored with a warning.

## Image Variants

Images are per-tool: each agent gets its own image, selected with `--tool`
(default: `claude`).

| Flag | Image tag | Description |
|------|-----------|-------------|
| (default) | `enclave-<tool>:latest` | Selected tool + default features |
| `--slim` | `enclave-<tool>:slim` | Selected tool, no features |

Use `--image-name <name>` to override the tag. When running from a non-default branch, the tag is automatically prefixed with the branch name and hash to avoid overwriting main images.

When `--base-image` or devcontainer mode is active without `--image-name`, the image name is auto-derived (e.g. `enclave-<tool>:base-<hash>-latest`).

## Node Runtime

enclave installs a private Node runtime at `/opt/enclave/node` for Node-based agent CLIs. Agent launchers are pinned to this runtime, while user shells and project commands use the user-facing `node`/`npm` from the base image or nvm. The private runtime version is pinned in `Dockerfile` via `AGENT_NODE_IMAGE`.

## Agent Updates

Agent CLIs auto-update after the interval elapses (24 hours by default), but only for tools that define `extensions/tools/<tool>/check-update.sh`. On a steady-state run, once that interval has elapsed, enclave runs the hook in a probe container; only a changed upstream fingerprint triggers a rebuild.

To refresh an agent now, use the build-only `update` command. It rebuilds the tool's image with the latest agent CLI and exits without starting a session:

```bash
enclave update              # Refresh the default tool's image, then exit
enclave update codex        # Refresh a specific tool
enclave update claude codex # Refresh several tools
```

With no arguments, `update` targets the resolved default tool (honoring `--tool` and config). It accepts the same build flags as a run (for example `--slim`, `--features`, `--base-image`), so it refreshes the exact image variant a matching run would use.

`update` always rebuilds. A normal `enclave` run never forces an agent update — it relies on the automatic interval check above. Tools without `check-update.sh` do not auto-update, but they still refresh on `enclave update`, on an explicit `--rebuild`, or when another change already causes a rebuild.

Set `ENCLAVE_AGENT_UPDATE_INTERVAL_HOURS=0` to run automatic update probes on every rebuild-eligible invocation. For offline events or frozen build inputs, use `--no-rebuild` to suppress image builds entirely (existing images are reused).

## Build Cache

The image rebuilds when the Dockerfile template, entrypoint, target, base image/devcontainer, or features change, when an automatic update probe reports a changed fingerprint for a tool hook, or when forced via `--rebuild` or the `update` command.

`--no-rebuild` bypasses all runtime and gateway image builds for the current invocation, including automatic update probes and rebuilds. It uses existing local images as-is and fails fast if a required image is missing.

`--no-cache` disables the runtime package caches (host directories bind-mounted from `~/.cache/enclave/`) and does not affect the Docker build cache.

By default, enclave builds with `docker build` and inline image cache.
`--cache-from <image>` adds image cache sources, and the current tool image's
own prior build (`enclave-<tool>:...`) is considered automatically when present.

Use buildx for shared incremental layer cache:

```bash
enclave --buildx-cache-dir ~/.cache/enclave/buildx --rebuild shell -- -lc true
```

`--buildx-cache-dir` uses buildx for the build, imports the local cache when it
is populated, and exports a `type=local,mode=max` cache. Use
`--buildx-cache-from` and `--buildx-cache-to` for raw buildx cache specs such as
registry or shared-directory targets. All image builds require the buildx
plugin because the Dockerfile uses BuildKit-only `RUN --mount` cache mounts. If
buildx is unavailable, enclave fails before starting the build and reports how
to install it.

Runtime images normally bake the host UID/GID. For shared event artifacts,
build with a canonical identity and remap at runtime:

```bash
enclave --build-uid 1000 --build-gid 1000 --runtime-uid-remap --rebuild shell -- -lc true
```

The image hash includes the effective build UID/GID, so a loaded image built for
a different numeric identity is rebuilt instead of being treated as current.
Runtime UID remap is incompatible with devcontainer `remoteUser` when that user
would be applied for shell sessions.

## Runtime Assets

Runtime DNS allowlists live in `runtime-assets/gateway-allowlists/` and are baked into the image at build time. Tool settings templates live in `extensions/tools/<tool>/templates/` and are copied to `/usr/local/share/enclave/templates/` inside the container. See [Configuration](configuration.md) for full config overrides and JSON/TOML patches.
