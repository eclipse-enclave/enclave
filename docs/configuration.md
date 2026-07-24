# Configuration

## Config Files

Global defaults live in `~/.config/enclave/config.json`. Project overrides live
in `~/.config/enclave/projects/<hash>/config.json`, keyed by project hash and
kept outside the worktree. CLI flags always take the highest precedence.

Project overrides live outside the worktree so a project cannot alter its own
isolation policy. Extension files are discovered from built-in `extensions/`
plus user-global `~/.config/enclave/extensions/`.

The `~/.config/enclave/` paths shown throughout this document are the Linux
(XDG) config root. On macOS the config root is
`~/Library/Application Support/org.eclipse.enclave/config/` instead;
substitute it for `~/.config/enclave/` in every path below.

**Option source precedence** (highest to lowest):

1. CLI flags
2. Selected tool override (`tool_overrides.<tool>` in config)
3. Project config (`~/.config/enclave/projects/<hash>/config.json`)
4. Global config (`~/.config/enclave/config.json`)
5. Built-in defaults

Security guardrail: project config cannot elevate guarded options such as `allow_all_network=true` or `allow_domains`. Those values are ignored with a warning; use global config or CLI flags for explicit host opt-in.

## Config Keys

| Key | Description |
|-----|-------------|
| `tool` | Default tool (e.g. `claude`, `codex`) |
| `backend` | Isolation backend (`docker`, default; experimental `qemu`) |
| `host_config` | `none` (default) or `passthrough` |
| `tool_overrides.<tool>.host_config_paths` | Per-tool passthrough path directives (`default`, `+path`, `-path`, or explicit list) |
| `yolo` | Enable YOLO mode (default: `true`) |
| `ephemeral` | Run without persistent auth/env stores |
| `auth_scope` | `shared` or `project` (default: `shared`) |
| `auth_name` | Named per-tool shared auth identity slug; unset uses the default store |
| `secrets_scope` | `both`, `global`, or `project` (default: `both`) |
| `reset_auth` | Clear auth on start |
| `no_api_key` | Disable API key injection |
| `pass_api_key` | Allow API key in ephemeral mode |
| `pass_env` | List of host env vars to forward |
| `allow_all_network` | Disable network restrictions |
| `allow_domains` | Extra domains added to the gateway allowlist (bare DNS names; ignored when `allow_all_network=true`) |
| `no_cache` | Disable package caches |
| `no_history` | Disable shell history |
| `no_memory` | Disable per-project agent memory |
| `session_monitor` | Run agents under the managed tmux session (enables `status` snapshots) |
| `base_image` | Docker base image override |
| `devcontainer` | Derive base image from devcontainer.json |
| `slim` | Build without features (tools only) |
| `cache_from` | Docker build cache source |
| `progress` | Docker build progress output style |
| `image_name` | Override default image name/tag |
| `features` | Feature extensions to enable |
| `use_remote_user` | Honor devcontainer `remoteUser` for agent sessions |
| `network_log` | Network audit mode: `coarse` (default) or `requests` |
| `verbose` | Verbose logging |
| `ports` | Publish container ports to the host (container → host) |
| `add_dirs` | Additional directories to mount |
| `add_readonly_dirs` | Additional directories to mount read-only |
| `project_mount` | `writable` (default) or `readonly` for the project/worktree mount |
| `worktree_metadata` | Linked-worktree git metadata mounts: `follow` (default), `readonly`, or `none` |
| `playwright_mcp` | Enable Playwright MCP server (Claude only) |
| `bridge_ports` | Forward host ports into the container (host → container) |

Project config may set `project_mount` to `readonly`, but `project_mount="writable"`
is ignored at project scope so it cannot weaken a stricter global default. In
readonly mode, writable `add_dirs` entries inside the project subtree are
mounted read-only.

`worktree_metadata` controls the gitdir/commondir mounts that enclave
resolves from a linked worktree's `.git` file. `follow` ties them to the
project mount mode, `readonly` forces them read-only while the working copy
stays writable, and `none` skips them entirely. With read-only metadata the
agent can edit files and read history (`git log`, `git diff`, `git status`),
but every git write, including `git add`, fails; staging and committing happen
on the host. Project config may strengthen the inherited mode from `follow` to
`readonly` or `none`, or from `readonly` to `none`; settings that would weaken
the inherited mode are ignored. For a regular repository whose `.git`
directory sits inside the project, the project mount mode governs and this
option has no effect.

**Example:**

```json
{
  "tool": "codex",
  "yolo": false,
  "secrets_scope": "project",
  "pass_env": ["GITHUB_TOKEN"],
  "allow_all_network": false
}
```

## Additive Syntax for `features` and `host_config_paths`

Use `+` and `-` prefixes to modify the parent config instead of replacing it:

```json
// Global config (~/.config/enclave/config.json):
{ "features": ["node-dev", "python-dev"] }

// Project config (~/.config/enclave/projects/<hash>/config.json):
{ "features": ["+devtools", "-python-dev"] }

// Result: ["devtools", "node-dev"]
```

Values without prefixes replace the parent set entirely.

`features` defaults to implicit `"default"` when unset. Additive entries are
applied against the implicit default-enabled set, so `["-node-dev"]` removes
that default feature and `[]` means "none".

`host_config_paths` is resolved per tool against that tool's reviewed
`passthroughPaths` from its `spec.yaml`. Use:

- `["default"]` to start from the built-in reviewed allow-list
- `["+commands/", "-skills/"]` to modify the built-in reviewed allow-list
- `["settings.json", "agents/"]` to replace the built-in allow-list entirely

Entries are relative to the tool's host config directory, not absolute host
paths. For Claude, `settings.json` means `~/.claude/settings.json`, and adding a
helper script should use `+statusline-command.sh`, not
`+/home/alice/.claude/statusline-command.sh`.

`host_config_paths` only affects `host_config=passthrough`. A hard safety
backstop still blocks auth files, OAuth JSON, history/session files, and common
runtime-state directories even if you add them here.

Passthrough follows symlinks: an allow-listed entry copies whatever its symlink
resolves to, even a target outside the tool config directory. This is what lets
dotfile managers (home-manager/Nix, GNU stow, chezmoi) symlink configs into
place. The auth/OAuth backstop still rejects any symlink that resolves to a known
credential file, but the history/session/state backstop is anchored at the config
root and does not re-match those names at nested paths reached through a symlinked
directory (e.g. `sessions/` blocks `~/.claude/sessions` but not a symlinked
`commands/` that contains a `sessions/` subdir). Because the config directory and
its symlinks are user-controlled, only point allow-listed entries at content you
intend to share with the container.

## Tool Config Overrides

Override tool-native config files without rebuilding the image.

Canonical override directories:

- Global: `~/.config/enclave/tools/<tool>/`
- Per-project: `~/.config/enclave/projects/<project-hash>/<tool>/config/`

Each directory mirrors the tool's native config layout. For example, Claude overrides live under `~/.config/enclave/tools/claude/` with files like `settings.json`, `commands/...`, `agents/...`, and `skills/...`.

At startup, enclave assembles a generated config source from:

1. Built-in settings, templates, tool-extension skills, and enabled-feature skills
2. Allow-listed host config when `host_config=passthrough`
3. Global shared skills, then global tool-specific overrides and patches
4. Project shared skills, then project tool-specific overrides and patches

The generated source is then copied into the writable tool config store before auth symlinks and tool-specific setup run.

## Managed Skills

A tool receives managed skills only when its extension declares `sandbox.skillsDir`. Shared skills use tool-neutral paths:

- Global: `~/.config/enclave/skills/<skill>/`
- Per-project: `~/.config/enclave/projects/<project-hash>/skills/<skill>/`

Tool-specific skills belong in the canonical native config tree. Mirror `skillsDir` relative to `configDir`:

- Global: `~/.config/enclave/tools/<tool>/<relative-skills-path>/<skill>/`
- Per-project: `~/.config/enclave/projects/<project-hash>/<tool>/config/<relative-skills-path>/<skill>/`

For example, Claude uses `skills/`, while Pi uses `agent/skills/`:

```text
~/.config/enclave/tools/claude/skills/review/
~/.config/enclave/tools/pi/agent/skills/review/
```

Skill precedence, lowest to highest, is:

1. Built-in tool-extension skills
2. User-global tool-extension skills
3. Skills shipped by enabled features (only features selected for the session contribute; see [Extensions](extensions/README.md))
4. Allow-listed native host config when passthrough is enabled
5. Global shared skills
6. Global tool-specific skills
7. Project shared skills
8. Project tool-specific skills

A higher-precedence same-named skill replaces the complete lower-precedence skill directory; files from two versions are not merged. Project scope therefore wins over global specificity: a project shared skill overrides a global tool-specific skill, while a project tool-specific skill overrides both.

With `host_config=passthrough`, every built-in skill-capable tool passes its native host skills directory through by default: the tool's reviewed `passthroughPaths` includes the skills path (`skills/` for most tools, `agent/skills/` for pi). Opt out under the selected tool override:

```json
{
  "tool_overrides": {
    "claude": {
      "host_config_paths": ["default", "-skills/"]
    }
  }
}
```

For pi, use `-agent/skills/`. At session start the log lists exactly which allow-listed paths pass through.

Shared skills must use the portable Agent Skills subset. `SKILL.md` must be a regular file with YAML frontmatter containing required `name` and `description` fields and only optional `license`, `compatibility`, and `metadata` fields. The name must match the directory and use lowercase letters, numbers, and hyphens. Harness-specific fields such as `allowed-tools` belong in a tool-specific skill. Enclave warns and skips an invalid shared skill rather than failing the session; tool-specific skills are left for the selected harness to validate. Symlinks inside shared skill sources are ignored.

Built-in skills remain tool-specific so extensions can carry harness-specific metadata. Skills shipped by enabled features overlay as trusted extension content and skip the portable-skill validation applied to shared skills. Tools without `sandbox.skillsDir` ignore all shared skill sources.

## Tool Config Patches

A full config override uses the file's native path in the canonical config directory:

- Global: `~/.config/enclave/tools/<tool>/<native-config-path>`
- Per-project: `~/.config/enclave/projects/<project-hash>/<tool>/config/<native-config-path>`

A patch mirrors the same native path below a dedicated patch directory:

- Global: `~/.config/enclave/patches/<tool>/<native-config-path>`
- Per-project: `~/.config/enclave/projects/<project-hash>/patches/<tool>/<native-config-path>`

Common settings paths:

| Tool | Native config path |
|------|--------------------|
| claude | `settings.json` |
| codex | `config.toml` |
| mistral-vibe | `config.toml` |
| opencode | `opencode.json` |
| pi | `agent/settings.json` |

Patches can target any existing JSON or TOML file in the generated tool config, not only the declared settings file. The target must exist in a lower-precedence layer.

Config files are resolved from lowest to highest precedence:

1. Built-in config
2. Allow-listed host config when `host_config=passthrough`
3. Global full file or patch
4. Project full file or patch

A patch merges onto the complete lower-precedence result. A full file replaces the effective config at its scope. Defining both a full file and a patch for the same path at the same scope is an error.

Merge semantics:
- **JSON:** scalars replace, objects deep-merge, arrays replace, `null` deletes keys
- **TOML:** scalars replace, tables deep-merge, arrays replace (key deletion not supported)

## Environment Variables

| Variable | Description |
|----------|-------------|
| `ENCLAVE_HOME` | Override asset discovery to point at a specific repo checkout |
| `ENCLAVE_LOG_LEVEL` | Log level: `info` (default) or `debug` |
| `ENCLAVE_AGENT_UPDATE_INTERVAL_HOURS` | Minimum hours after a tool's last successful automatic update before `check-update.sh` is eligible to probe again (`0` = always) |
| `ENCLAVE_DEVCONTAINER_REWRITE_VARS` | Comma-separated extra env var names for devcontainer home-path normalization |

Buildx cache and canonical build UID/GID controls are CLI-only. Use
`--buildx-cache-dir`, `--build-uid`, `--build-gid`, and `--runtime-uid-remap`
for event/offline runs.

The experimental `qemu` backend only runs unrestricted, slim/no-feature bundles, so selecting it implies `allow_all_network=true` and `slim=true` automatically (with a per-run notice). Requesting features or an allowlist (`--allow-domain`) is rejected because the backend cannot honor them.

## Inspecting Resolved Config

```bash
enclave config                   # Show configuration values (matrix view)
enclave config --view source     # Annotate each value with where it came from
enclave config --view diff       # Show values overridden by higher precedence
enclave config --view effective  # Show effective values only
enclave config --json            # Emit JSON output
```

| Flag | Description |
|------|-------------|
| `--view <mode>` | Output view: `matrix` (default), `effective` (effective values only), `diff` (values overridden by higher precedence), or `source` (where each value comes from: CLI, tool override, project, global, default) |
| `--json` | Emit JSON output |
