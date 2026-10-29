# Configuration

## Config Files

Global defaults live in `~/.config/enclave/config.json`. Project overrides live
in `~/.config/enclave/projects/<hash>/config.json`, keyed by project hash and
kept outside the worktree. CLI flags always take the highest precedence.

Project overrides live outside the worktree, where neither the repository nor a
session can edit them, so a project cannot alter its own isolation policy. Extension files are discovered from built-in `extensions/`
plus user-global `~/.config/enclave/extensions/`.

Extensions installed with `enclave tools|features add` carry a
`.enclave-source.json` provenance sidecar in their user-global directory.
Editing an installed extension's files by hand blocks
`enclave tools|features update` for that extension until `--force`. See
[Installing Extensions](extensions/installing.md).

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

A tool override is more specific than a top-level setting, so it wins in either
file: a global `tool_overrides.<tool>` value beats a project's top-level value.
Between the two files' tool overrides, the project's wins. List options merge
across these layers; see [List Options](#list-options).

Because only you can edit it, project config may set every option global config
can, with the same effect for that project. The exceptions are `auto_update`
and `update_channel`: they are global-only rather than project or per-tool
options because they replace the host executable, so project and tool-override
values are ignored with a warning. Opt-ins without a config key, such as
`--allow-sensitive-mounts`, cannot be granted by any config file.

Top-level `host_config_paths` is not part of that set: it is ignored with a
warning in any config file, global included, because passthrough paths are only
supported under `tool_overrides.<tool>`.

## Config Keys

| Key | Description |
|-----|-------------|
| `auto_update` | Automatically replace a stale standalone binary with the latest build of its update channel during the daily update check (default: `false`; global config only; ignored by package and source builds) |
| `update_channel` | Release channel binary updates come from: `rolling` (builds of `main`) or `stable` (official releases from version tags). Unset follows the channel the binary was published on, which `enclave config` shows as the default (global config only; see [`self-update`](cli-reference.md)) |
| `tool` | Default tool (e.g. `claude`, `codex`); unset means the first interactive session asks once and writes the answer here (see [Tool selection](cli-reference.md#tool-selection)) |
| `backend` | Isolation backend: `auto` (default) uses docker or podman, whichever is installed, and asks once when both are; or `docker`, `podman`, experimental `qemu`. Override per invocation with `--backend` on engine-using commands. |
| `host_config` | `none` (default) or `passthrough` |
| `skills_validation` | Shared skill validation: `strict` (default) or `agent`; supports tool overrides |
| `tool_overrides.<tool>.host_config_paths` | Per-tool passthrough path directives (`default`, `+path`, `-path`, or explicit list) |
| `yolo` | Enable YOLO mode (default: `true`). Only consulted when the selected tool's profile leaves `yoloEnabled` unset; every bundled CLI agent sets it, so use `--yolo`/`--no-yolo` for those |
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
| `no_memory` | Disable per-project agent memory; see [memory controls](runtime/stores.md#agent-memory) |
| `session_monitor` | Run agents under the managed tmux session (enables `status` snapshots) |
| `session_tint` | Terminal background color marking a session-owned terminal, as `#rrggbb` (unset: no tint) |
| `timezone` | Container `TZ`, e.g. `UTC` or `Europe/Vienna` (unset: host time zone); a `TZ` from `.env`, devcontainer `containerEnv`, or `--pass-env` wins |
| `base_image` | Docker base image override |
| `devcontainer` | Derive base image from devcontainer.json |
| `slim` | Build without features (tools only) |
| `cache_from` | Docker build cache source |
| `progress` | Docker build progress output style |
| `image_name` | Override default image name/tag |
| `features` | Feature extensions to enable |
| `use_remote_user` | Honor devcontainer `remoteUser` for agent sessions |
| `network_log` | Network audit mode: `coarse` (default, one event per TLS connection) or `requests` (one event per HTTP/HTTPS request, forcing MITM). See [Coverage and granularity](networking.md#coverage-and-granularity) |
| `verbose` | Verbose logging |
| `ports` | Publish container ports to the host (container → host) |
| `add_dirs` | Additional directories to mount |
| `add_readonly_dirs` | Additional directories to mount read-only |
| `sensitive_dir_exceptions` | Paths whose subtrees may be mounted although they are sensitive; mounts nothing. See [Sensitive-dir exceptions](cli-reference.md#sensitive-dir-exceptions) |
| `project_mount` | `writable` (default) or `readonly` for the project/worktree mount |
| `worktree_metadata` | Linked-worktree git metadata mounts: `follow` (default), `readonly`, or `none` |
| `playwright_mcp` | Enable Playwright MCP server (Claude only) |
| `bridge_ports` | Forward host ports into the container (host → container) |

In readonly mode, writable `add_dirs` entries inside the project are mounted
read-only.

`worktree_metadata` controls the gitdir/commondir mounts that enclave
resolves from a linked worktree's `.git` file. `follow` ties them to the
project mount mode, `readonly` forces them read-only while the working copy
stays writable, and `none` skips them entirely. With read-only metadata the
agent can edit files and read history (`git log`, `git diff`, `git status`),
but every git write, including `git add`, fails; staging and committing happen
on the host. For a regular repository whose `.git`
directory sits inside the project, the project mount mode governs and this
option has no effect.

Regardless of these modes, existing config files for the current repository
receive read-only file mounts; other Git metadata stays writable in writable
mode. See [Git config protection](persistence.md#git) for scope and limitations.

`session_tint` marks the terminal that owns a session, so a sandboxed session
is visually distinct from an ordinary shell. When set to an `#rrggbb` value,
`run` (including `shell`, `continue`, and `resume`), `exec`, and `attach` set
the terminal background on start and reset it on exit. Inside tmux 3.3 or later
only the session's pane is tinted; elsewhere the whole window is. Because only
the background changes, pick a color that keeps your foreground text readable —
enclave does not adjust it. Invalid values are reported when the config file is
read and ignored.

The tint is skipped when stdout is not a terminal and when `NO_COLOR` or
`ENCLAVE_COLOR=never` is set; `ENCLAVE_COLOR=always` does not enable it, as the
config key is the only switch. Because per-tool and per-project config layers
apply, `tool_overrides.<tool>.session_tint` gives each tool its own color and a
project config gives each project one; `attach` and `exec --name` resolve the
color for the tool and project of the session they enter, not for the current
directory.
Agents that paint their own background can cover the tint.

The reset restores the terminal's configured default background, not whatever
background was in effect before the session, so a color set at runtime (theme
switchers, base16-style scripts) is dropped when the session ends. If enclave is
killed with `SIGKILL` the reset never runs; clear a leftover tint with
`printf '\e]111\a'`.

**Example:**

```json
{
  "tool": "codex",
  "project_mount": "readonly",
  "secrets_scope": "project",
  "pass_env": ["GITHUB_TOKEN"],
  "allow_all_network": false
}
```

## List Options

List options come in two kinds. Each layer (global, project, the global and
then the project file's tool override, CLI) merges onto the value it inherits.

**Extras** are `add_dirs`, `add_readonly_dirs`, `sensitive_dir_exceptions`,
`ports`, `allow_domains`, `bridge_ports`, `pass_env`, and `cache_from`. Config files and CLI flags
follow the same rules, so existing configs and scripts keep their meaning:

| Entry | Effect |
|---|---|
| `x` | replaces the inherited list |
| `+x` | adds to it |
| `-x` | removes from it |
| `[]`, `["none"]`, or `--flag none` | clears |

A list is either plain or all `+`/`-`: mixing the two, or `none` with other
entries, is an error, because replacing a list and amending it in the same
place has no meaning a plain list cannot express.

The exception is `--add-dir` and `--add-readonly-dir`: on the command line a
plain path adds to the configured directories, because mounting one more
directory is what these flags are almost always used for. `-dir` removes a
configured one, and `none` makes the other paths replace them.

```json
// Global config:
{ "ports": ["8080:80"], "add_dirs": ["~/shared"] }

// Project config:
{ "ports": ["+3000"], "add_dirs": ["-~/shared", "+./data"] }

// Result: ports ["8080:80", "3000"], add_dirs ["./data"]
```

- Removing an entry that is not there is silent.
- Exactly one prefix is stripped. Write `./+dir`, `./-dir`, or `./none` for a
  relative directory with such a name; `++x` adds `+x`. When a directory is not
  found but the name with a leading `+` exists, the error suggests `./+x`.
- Entries are compared after normalization: `3000`, `3000:3000`, and
  `127.0.0.1:3000:3000` are the same port; domains are case-insensitive;
  directories compare as resolved paths.
- Two ports clash when they publish the same host port on overlapping
  addresses (an omitted address is `127.0.0.1`; `0.0.0.0` and `::` overlap
  everything; host port `0` never clashes). Within one layer that is an error;
  across layers the more specific entry replaces the other, so `-p 8080:81`
  overrides a configured `8080:80`.
- An invalid entry, a removal included, is an error that names the flag or the
  key and file that set it, such as
  `tool_overrides.claude.pass_env in ~/.config/enclave/config.json`.

**Selections** are `features` and `host_config_paths`. A bare list replaces
the inherited one and `+`/`-` entries amend it:

```json
// Global config:
{ "features": ["node-dev", "python-dev"] }

// Project config:
{ "features": ["+devtools", "-python-dev"] }

// Result: ["node-dev", "devtools"]
```

- A list with any bare entry (a name or keyword) replaces the inherited one;
  `[]` selects nothing.
- A list of only `+`/`-` entries amends the configured selection, and the more
  specific layer wins per item: a project `+x` restores an `x` that global
  config removed. The CLI works the same way: `--features +x` adds to the
  configured features, while `--features a,b` replaces them.
- Only when no layer selects features do directives apply to the implicit
  default-enabled set, so `["-node-dev"]` removes that default feature and
  `["+shell-extras"]` adds an opt-in one. In devcontainer mode the implicit set
  is empty.
- `--features none` selects nothing and cannot be combined with other values;
  in config, write `[]`.
- `--playwright-mcp` enables the `playwright` feature it needs even when
  `features` removes it, with a warning.

Feature names, including `+name` entries, must match an installed extension;
unknown names fail before building or starting a session. Unknown `-name`
removals only warn, as do unknown names from config when `--slim` or
`--backend qemu` discards the selection. Each message names the flag or the
key and file that set the entry. Use `enclave features list` to see available
names.

`host_config_paths` is resolved per tool against that tool's reviewed
`passthroughPaths` from its `spec.yaml`. Use:

- `["default"]` to start from the built-in reviewed allow-list
- `["+commands/", "-skills/"]` to modify the built-in reviewed allow-list (a
  removal matches with or without the trailing `/`)
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

For pi, use `-agent/skills/`. At session start the log lists exactly which allow-listed paths pass through. Shared skills do not require host passthrough; the directives above exclude host skills while retaining passthrough for other settings.

`skills_validation` controls validation of global and project shared skills:

- `strict` (default): `SKILL.md` must be a regular, readable file with YAML frontmatter containing required `name` and `description` fields and optional `license`, `compatibility`, and `metadata` fields. The name must match the directory and use lowercase letters, numbers, and hyphens. Other fields, including `allowed-tools` and `disable-model-invocation`, cause the skill to be skipped.
- `agent`: require a regular, readable `SKILL.md`, then copy its bytes and supporting files unchanged. Enclave does not parse the frontmatter, enforce name matching, strip fields, or translate metadata between agents. The selected agent validates and interprets the skill; metadata accepted by one agent may be ignored or rejected by another.

In both modes, overrides are keyed by directory name. In `agent` mode, different directories can declare the same frontmatter `name` and both reach the agent; Enclave does not deduplicate them.

Enclave warns and skips invalid shared skills rather than failing the session. A skipped higher-precedence skill leaves the lower-precedence skill intact. Tool-specific overrides, host passthrough, and extension skills are unaffected by this option.

Set `"skills_validation": "agent"` in global or project `config.json`, or under `tool_overrides.<tool>`. The mode applies to all shared skills in the session. Values are case-insensitive and surrounding whitespace is ignored. The normal option precedence applies; `--skills-validation strict|agent` takes priority over config files. Project configuration may select `agent` because it is stored in Enclave's host-managed configuration root and does not expose additional host paths.

In both modes, symlinked skill directories and symlinks inside skills are ignored; `SKILL.md` itself must be a regular file.

Built-in skills remain tool-specific so extensions can carry harness-specific metadata. Skills shipped by enabled features overlay as trusted extension content and skip shared-skill validation. Tools without `sandbox.skillsDir` ignore all shared skill sources.

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
| copilot | `settings.json` |
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

When a config source is active, Enclave carries edits made by the tool to its
declared settings file (`sandbox.settingsTarget`) into the next launch. It
merges changed keys into the current generated settings. New patch or override
values win conflicts; objects merge recursively, while arrays and scalars are
atomic. Only that settings file is carried forward, independently for each
project/worktree/session store; patch and override files are never changed.
A launch without an overlay resets the carry-over baseline. Entrypoint writes
to this file also carry over, including yolo-mode permission and trust keys,
until changed by a patch, override, or the tool. See [Config Store](runtime/stores.md#config-store)
for snapshot lifecycle, missing-file behavior, and upgrade limitations.

## Environment Variables

| Variable | Description |
|----------|-------------|
| `ENCLAVE_HOME` | Override asset discovery to point at a specific repo checkout |
| `ENCLAVE_LOG_LEVEL` | Log level: `info` (default) or `debug` |
| `ENCLAVE_AGENT_UPDATE_INTERVAL_HOURS` | Minimum hours after a tool's last successful automatic update before `check-update.sh` is eligible to probe again (`0` = always) |
| `ENCLAVE_NO_REBUILD` | Set to `1` to suppress session image/bundle builds and automatic agent update probes; see [Image & Build](cli-reference.md#image--build). |
| `ENCLAVE_DEVCONTAINER_REWRITE_VARS` | Comma-separated extra env var names for devcontainer home-path normalization |
| `ENCLAVE_ALLOW_ROOT` | Set to `1` to skip the root check (same as `--allow-root`); see [Running as root](cli-reference.md#running-as-root) |
| `ENCLAVE_ALLOW_SENSITIVE_MOUNTS` | Set to `1` to mount sensitive host directories anyway (same as `--allow-sensitive-mounts`); see [Sensitive mounts](cli-reference.md#sensitive-mounts) |

These are read by the Windows launcher on the Windows side only, and are not
forwarded into the WSL2 distribution. See [windows.md](windows.md).

| Variable | Description |
|----------|-------------|
| `ENCLAVE_WSL_DISTRO` | Distribution to use for a Windows drive working directory; ignored with a warning when the working directory already names one |
| `ENCLAVE_WSL_ALLOW_WINDOWS_PATH` | Set to `1` to accept a Windows drive working directory and reach it through `/mnt/<letter>` |
| `ENCLAVE_WSL_FORWARD_ENV` | Comma-separated extra variables to forward into the distribution, each optionally with a `WSLENV` flag suffix (`/p`, `/l`, `/u`, `/w`) |

Buildx cache and canonical build UID/GID controls are CLI-only. Use
`--buildx-cache-dir`, `--build-uid`, `--build-gid`, and `--runtime-uid-remap`
for event/offline runs.

`--allow-root` is CLI-only as well; `ENCLAVE_ALLOW_ROOT=1` is its only
alternative, so no config file can let enclave run as root.
`--allow-sensitive-mounts` is CLI-only too; `ENCLAVE_ALLOW_SENSITIVE_MOUNTS=1`
is its only alternative, so no config file can let a session mount sensitive
host data. Global and project config can exempt specific paths with
`sensitive_dir_exceptions`, which warns on every start and never covers home,
enclave's own roots, or the SSH agent socket. `--add-sensitive-dir` and
`--add-readonly-sensitive-dir` exempt their paths for one run and have no
config key. `add_dirs` and `add_readonly_dirs` entries from config are checked
like `--add-dir`, and a refusal names the flag or config file of each entry.

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

### JSON output (`schemaVersion: "1"`)

`enclave config --json` emits a versioned envelope for scripts and tools. Every
view carries the full envelope; `--view` only affects the legacy keys.

| Field | Content |
|-------|---------|
| `schemaVersion`, `kind`, `view` | `"1"`, `"config"`, and the selected view |
| `tool` | `{name, source}`; `name` is `null` when no tool is selected |
| `project` | `{dir, realDir, hash, name}` of the current project, or `null` (with a `project_unavailable` warning) when it cannot be resolved |
| `files` | `global` and `project` config files as `{path, status}`, status `present`, `missing`, or `unavailable` |
| `options` | One entry per option (see below) |
| `warnings` | Array of `{code, option, layer, path, message}`; `option`, `layer`, and `path` are omitted when not applicable |
| `valid`, `error` | Whether the options pass the validation a run applies and the tool profile loads, and the first error |

Each `options.<name>` entry has:

- `kind`: `string`, `bool`, or `string_slice`.
- `value`: the typed value a run with the same options uses, normalized and
  validated (lowercased domains, conflict resolution, qemu coercion); only
  normalized if validation fails. Lists are always arrays.
  `features` is the sorted set a run enables (after `slim`, devcontainer mode,
  `playwright_mcp`, and `+`/`-` directives, without unknown names), or `null`
  with a `features_unavailable` warning when features cannot be listed (`valid`
  can stay `true` then). `host_config_paths` and `yolo` are resolved against
  the tool (`null` without a loadable tool); `yolo` adds `resolvedBy` (`cli`,
  `config`, or `tool`). `backend` keeps the configured value, including `auto`.
- `source`: the layer that last set the raw option: `default`, `global`,
  `project`, `tool_override`, or `cli`.
- `layers`: the raw value each of `global`, `project`, `tool_override`, and
  `cli` sets, after ignored keys are dropped and before normalization; absent
  layers do not set the option. List entries keep their `+`/`-` prefixes as
  written; `tool_override` shows the two files' overrides combined.
- `directives` (`features`, `host_config_paths` only): the raw selection,
  `null` when unset and `[]` for none.

Warning codes: `invalid_config_json`, `config_path_is_directory`,
`unsupported_scope`, `invalid_tool_override`, `invalid_value`,
`suspicious_value`, `project_guardrail` (config loading;
`layer` is `global`, `project`, or `tool_override:<tool>`), `unknown_feature`,
`features_unavailable`, `tool_unresolved`, `tool_unavailable` (also makes
`valid` false), `project_unavailable`, and `validation` (e.g. an ignored
option).

`valid: false` does not change the exit code (0). Validation runs the checks of
`enclave run`, including the `qemu` backend restrictions, but treats `auto` as
`docker` and skips the container engine, mounts, and image build.

The earlier keys `effective`, `sources`, `paths`, and `status` are unchanged
for compatibility; they hold display strings (lists as `"[a,b]"`).

### Detecting session drift

`enclave ps --json` records the same typed values in `options` for the options
that shape a session (see the [`ps` reference](cli-reference.md#session)).
To check whether a fresh session would differ from a running one:

1. Run `enclave config --json` from the session's `projectDir` with its
   `--tool` and the same flags.
2. Compare each key of the session's `options` with `options.<name>.value`.
   A key missing on either side means unknown, not changed.

Backend-dependent validated values may differ, since `config --json` validates
`auto` as `docker` while a run validates the detected engine.

Sessions started with `enclave devcontainer run|shell` cannot be compared.
Devcontainer mode has no flag, so `config --json` resolves `devcontainer` and
`features` without it; treat these sessions as unknown. A `devcontainer` key
set in config is resolved by both sides.
