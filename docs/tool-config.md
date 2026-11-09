# Tool config

How to customize the agent's own configuration (settings, commands, agents,
skills) without rebuilding the image.

| Goal | Mechanism |
|------|-----------|
| Reuse reviewed parts of your host config (e.g. `~/.claude/`) | [Host config passthrough](#host-config-passthrough) |
| Replace a native config file | [Tool config overrides](#tool-config-overrides) |
| Change a few keys in a JSON or TOML file | [Tool config patches](#tool-config-patches) |
| Add skills for every agent or one agent | [Managed skills](#managed-skills) |

| Scope | Overrides | Patches | Shared skills |
|-------|-----------|---------|---------------|
| Global | `~/.config/enclave/tools/<tool>/` | `~/.config/enclave/patches/<tool>/` | `~/.config/enclave/skills/<skill>/` |
| Project | `~/.config/enclave/projects/<project-hash>/<tool>/config/` | `~/.config/enclave/projects/<project-hash>/patches/<tool>/` | `~/.config/enclave/projects/<project-hash>/skills/<skill>/` |

At startup Enclave assembles a generated config source, lowest to highest:

1. Built-in settings, templates, tool-extension skills, enabled-feature skills
2. Allow-listed host config when `host_config=passthrough`
3. Global shared skills, then global tool-specific overrides and patches
4. Project shared skills, then project tool-specific overrides and patches

It is copied into the writable tool config store before auth symlinks and
tool-specific setup run; see [Config store](runtime/stores.md#config-store).

## Host config passthrough

`host_config=passthrough` (or `--host-config passthrough`; default `none`)
copies reviewed paths from the tool's host config directory into the session.
`tool_overrides.<tool>.host_config_paths` selects them, resolved against the
reviewed `passthroughPaths` in the tool's `spec.yaml` with the
[selection syntax](configuration.md#selections). Top-level `host_config_paths`
is ignored with a warning.

| Value | Effect |
|-------|--------|
| `["default"]` | The built-in reviewed allow-list |
| `["+commands/", "-skills/"]` | Modify it (a removal matches with or without the trailing `/`) |
| `["settings.json", "agents/"]` | Replace it |

Entries are relative to the host config directory: for Claude,
`settings.json` is `~/.claude/settings.json`, and a helper script is
`+statusline-command.sh`, not `+/home/alice/.claude/statusline-command.sh`. The
session log lists the paths that pass through. A backstop always blocks auth
files, OAuth JSON, history/session files, and common runtime-state
directories.

Passthrough follows symlinks, even to targets outside the config directory, so
dotfile managers (home-manager/Nix, GNU stow, chezmoi) work.

> [!WARNING]
> The auth/OAuth backstop rejects symlinks to known credential files, but the
> history/session/state backstop matches only at the config root: `sessions/`
> blocks `~/.claude/sessions`, not a `sessions/` inside a symlinked
> `commands/`. Only allow-list entries whose content you intend to share.

## Tool config overrides

An override file replaces the effective config at its scope. Override
directories mirror the native layout, e.g.
`~/.config/enclave/tools/claude/settings.json`, `commands/...`, `agents/...`,
`skills/...`.

## Tool config patches

A patch mirrors the native path below the patch directory and merges onto the
lower-precedence result. It can target any JSON or TOML file in the generated
config that exists in a lower layer; the tool must declare
`sandbox.configDir`.

| Tool | Main settings path |
|------|--------------------|
| claude | `settings.json` |
| codex | `config.toml` |
| copilot | `settings.json` |
| opencode | `opencode.json` |
| pi | `agent/settings.json` |

Layers apply in the [startup order](#tool-config). A full file and a patch for
the same path at the same scope is an error.

| Format | Merge |
|--------|-------|
| JSON | Scalars and arrays replace, objects deep-merge, `null` deletes keys |
| TOML | Scalars and arrays replace, tables deep-merge, no key deletion |

### Edits made by the tool

With a config source active, edits the tool makes to its settings file
(`sandbox.settingsTarget`) carry into the next launch:

- Changed keys merge into the new generated settings; new patch or override
  values win conflicts. Objects merge recursively, arrays and scalars are
  atomic.
- Only that file carries over, per project and session store. Patch and
  override files are never changed.
- Entrypoint writes (including yolo permission and trust keys) carry over too,
  until a patch, override, or the tool changes them.
- A launch without an overlay resets the baseline.

Snapshot lifecycle and limits: [Config store](runtime/stores.md#config-store).

## Managed skills

Only tools that declare `sandbox.skillsDir` receive managed skills; others
ignore all shared sources. Tool-specific skills mirror `skillsDir` relative to
`configDir` (Claude `skills/`, Pi `agent/skills/`):

```text
~/.config/enclave/tools/claude/skills/review/
~/.config/enclave/tools/pi/agent/skills/review/
```

### Precedence

Each immediate child of a source is one skill. Lowest to highest:

| # | Source | Location |
|---|--------|----------|
| 1 | Built-in tool-extension skills | `extensions/tools/<tool>/skills/` |
| 2 | User-global tool-extension skills | `~/.config/enclave/extensions/tools/<tool>/skills/` |
| 3 | Enabled features' skills | `extensions/features/<feature>/skills/` and the user extension tree |
| 4 | Host config (passthrough) | The tool's host skills path |
| 5 | Global shared | `~/.config/enclave/skills/<skill>/` |
| 6 | Global tool-specific | `~/.config/enclave/tools/<tool>/<relative-skills-path>/<skill>/` |
| 7 | Project shared | `~/.config/enclave/projects/<hash>/skills/<skill>/` |
| 8 | Project tool-specific | `~/.config/enclave/projects/<hash>/<tool>/config/<relative-skills-path>/<skill>/` |

The result is mounted at `sandbox.skillsDir`. A same-named higher skill
replaces the whole lower directory (no file merge), so a project shared skill
beats a global tool-specific one. Built-in skills stay per tool so they can
carry harness-specific metadata; feature skills are trusted extension content
and skip validation.

### Host skills

Passthrough includes each built-in tool's skills path by default (`skills/`,
or `agent/skills/` for pi). Shared skills do not need passthrough. To exclude
host skills but keep other passthrough:

```json
{
  "tool_overrides": {
    "claude": { "host_config_paths": ["default", "-skills/"] }
  }
}
```

For pi, use `-agent/skills/`.

### Validation

`skills_validation` (or `--skills-validation`; case-insensitive, whitespace
trimmed) applies to all global and project shared skills of a session.
Tool-specific skills are validated by the agent; passthrough and extension
skills are unaffected.

| Mode | Behavior |
|------|----------|
| `strict` (default) | `SKILL.md` needs YAML frontmatter with `name` and `description`, optionally `license`, `compatibility`, `metadata`. `name` must match the directory and use lowercase letters, digits, and hyphens. Any other field (e.g. `allowed-tools`, `disable-model-invocation`) skips the skill |
| `agent` | Copies `SKILL.md` and supporting files unchanged; the agent interprets the metadata, which other agents may ignore or reject |

- Project config may select `agent`: it lives in Enclave's host config root
  and exposes no extra host paths.
- Invalid skills are skipped with a warning; the lower-precedence version
  stays.
- Overrides key on directory name. In `agent` mode two directories with the
  same frontmatter `name` both reach the agent.
- `SKILL.md` must be a regular, readable file in both modes. Symlinked skill
  directories and symlinks inside skills are ignored.
