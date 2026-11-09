# Integration

Machine-readable output for scripts and tools that drive Enclave. Prefer these
contracts over human-readable output, container labels, or Docker inspection.

| Command | Format | Contract |
|---------|--------|----------|
| `enclave ps --json` | JSON array of sessions | [`ps --json`](#ps---json) |
| `enclave config --json` | Envelope, `schemaVersion: "1"`, `kind: "config"` | [`config --json`](#config---json) |
| `enclave network print --json` | Envelope, `schemaVersion: "1"`, `kind: "network-policy"` | [`network print --json`](#network-print---json) |
| `enclave network log --json` | Raw JSONL event stream, or one aggregate object with `--summary` | [Reading the network log](networking.md#reading-the-network-log) |
| `enclave version --json` | Object with version, commit, and date | [Inspect commands](cli-reference.md#inspect) |
| `enclave tools\|features\|commands list --json`, and `add`/`update`/`remove --json` | `schemaVersion: "1"` | [Extension JSON output](extensions/json-output.md) |

Background-run output, lifecycle commands, and container naming
(`enclave-<tool>-<project-hash>`, plus `-<session>` for other sessions; see
[Session names](persistence.md#session-names)) are integration contracts too.

Known consumers rely on more than the JSON output; see
[Known consumers](#known-consumers).

## `ps --json`

An array with one object per container (`--all` adds stopped ones):

| Field | Content |
|-------|---------|
| `name`, `tool`, `projectHash` | Container name, tool profile, project hash |
| `projectDir` | Absolute, resolved project path |
| `worktree` | Directory (project or linked worktree) the session was started from |
| `status` | Container status as reported by the engine |
| `createdAt` | RFC 3339, empty if unknown |
| `sessionName` | The `--name` session name or the auto-assigned `1`, `2`, … |
| `background` | Whether the session runs detached |
| `ports` | Array of `{containerPort, hostPort, hostIP, protocol}` bindings; a host port requested as `0` shows the assigned port |
| `network` | `{name, subnet}` of the per-session bridge under Docker and podman; `null` for other backends and sessions started by earlier releases |
| `options` | Session provenance; see below |

### Session provenance

`options` holds the session-shaping options as validated for the run, keyed and
typed like `options.<name>.value` of [`config --json`](#config---json).

- `features` is the sorted enabled set; `pass_env` holds names, never values.
- `backend` and `image_name` are the configured values (e.g. `auto`), not the
  detected engine or derived tag.
- Host-side and one-shot options (e.g. `progress`, `verbose`, `reset_auth`,
  `session_tint`, `host_env`, `session_name`, build-cache options) are not
  recorded.
- `options` is omitted when unknown: older sessions, unresolvable features, or
  unreadable provenance. qemu sessions are not listed.

The `enclave.options` label that stores it is not a contract.

## `config --json`

Every view emits the full envelope; `--view` only affects the legacy keys. New
options and kinds may appear within a schema version; skip unknown `kind`s.

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

| Field | Content |
|-------|---------|
| `kind` | `string`, `bool`, `string_slice`, or `string_map` |
| `value` | The typed value a run with the same options uses, normalized and validated (lowercased domains, conflict resolution, qemu coercion); only normalized if validation fails. Lists are always arrays, maps always objects |
| `source` | The layer that last set the raw option: `default`, `global`, `project`, `tool_override`, or `cli` |
| `layers` | The raw value each of `global`, `project`, `tool_override`, and `cli` sets, after ignored keys are dropped and before normalization. Absent layers do not set the option. List entries keep their `+`/`-` prefixes as written; `tool_override` shows the two files' overrides combined |
| `directives` | `features` and `host_config_paths` only: the raw selection, `null` when unset and `[]` for none |

- `features` is the sorted set a run enables (after `slim`, devcontainer mode,
  and directives, without unknown names), or `null` with a
  `features_unavailable` warning (`valid` may stay `true`).
- `host_config_paths` and `yolo` are resolved against the tool (`null`
  without one); `yolo` adds `resolvedBy` (`cli`, `config`, or `tool`).
- `backend` keeps the configured value, including `auto`.

Warning codes:

| Code | Meaning |
|------|---------|
| `invalid_config_json`, `config_path_is_directory`, `unsupported_scope`, `invalid_tool_override`, `invalid_value`, `suspicious_value`, `project_guardrail` | Config loading; `layer` is `global`, `project`, or `tool_override:<tool>` |
| `unknown_feature`, `features_unavailable` | Feature resolution |
| `tool_unresolved` | No tool selected |
| `tool_unavailable` | Tool profile cannot be loaded; also makes `valid` false |
| `project_unavailable` | Project cannot be resolved |
| `validation` | A validation finding, e.g. an ignored option |

- `valid: false` keeps exit code 0. Validation runs `enclave run`'s checks
  (qemu restrictions included) but treats `auto` as `docker` and skips the
  engine, mounts, and image build.
- Legacy keys `effective`, `sources`, `paths`, and `status` hold display
  strings (lists as `"[a,b]"`).

### Detecting session drift

To check whether a fresh session would differ from a running one:

1. Run `enclave config --json` from the session's `projectDir` with its
   `--tool` and the same flags.
2. Compare each key of the session's `options` with `options.<name>.value`.
   A key missing on either side means unknown, not changed.

Backend-dependent values may differ (`auto` validates as `docker`). Sessions
from `enclave devcontainer run|shell` cannot be compared, since devcontainer
mode has no flag; treat them as unknown (a configured `devcontainer` key is
fine).

## `network print --json`

The allowlist a run with the same options would enforce:

```json
{
  "schemaVersion": "1",
  "kind": "network-policy",
  "tool": "claude",
  "project": { "dir": "/home/you/src/app", "hash": "e9671acd2448" },
  "mode": "restricted",
  "domains": ["anthropic.com", "api.anthropic.com", "github.com"],
  "denied": ["telemetry.example.com"],
  "notes": ["Hosts selected at session start through serviceAuth.hostsFromCredential are not listed."],
  "error": ""
}
```

| Field | Content |
|-------|---------|
| `domains` | The allow set the gateway renders, canonical and sorted, without entries equal to or under a denied domain |
| `denied` | The blackholed domains, sorted |
| `mode` | `restricted` or `unrestricted`; in unrestricted mode `domains`, `denied`, and `notes` are empty |
| `project` | `null` when the project cannot be resolved |

Arrays are never `null`. Fields are only added, never renamed or removed.

If the policy cannot be resolved (no or unknown tool, options a run would
reject, a malformed spec, policy, or defaults file), stdout has one envelope
with `error` set, `mode: null`, empty arrays (`tool: null` for startup
failures), and exit code 1. Usage errors and warnings go to stderr only (exit
1, empty stdout). Policy inputs:
[Managing the network policy](networking.md#managing-the-network-policy).

## Known consumers

[HomeShell](https://github.com/eclipse-enclave/homeshell) drives sessions
through the `enclave` CLI. Besides the JSON contracts above, it relies on:

- `run --background` and `continue`, with `--tool`, `--name`,
  `--pass-env +NAME`, and `--add-sensitive-dir`
- `attach`, `exec`, and `stop` on a session
- `ps --json --all`
- `tools list --json` and `features list --json`

Coordinate breaking changes to any of these with HomeShell.
