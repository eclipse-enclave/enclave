# Extension JSON output

Integration contracts (`schemaVersion: "1"`) of
`enclave tools|features|commands list --json` and of the
`add`/`update`/`remove` result envelope; commands are in
[Installing extensions](installing.md).

## `list --json`

One entry per extension, built-in or installed.

```json
{
  "schemaVersion": "1",
  "kind": "feature",
  "selection": { "directives": ["+demo"], "source": "tool_override", "tool": "claude" },
  "extensions": [
    {
      "name": "github-cli",
      "displayName": "GitHub CLI",
      "description": "GitHub CLI (gh)",
      "source": "builtin",
      "enabled": true,
      "managed": false,
      "defaultEnabled": true,
      "needsRoot": true,
      "releaseHosts": ["*.ghe.com", "*.github.com", "*.githubusercontent.com", "api.github.com", "ghe.com"]
    },
    {
      "name": "demo",
      "displayName": "Demo",
      "description": "Demo feature for documentation walkthrough",
      "source": "user",
      "enabled": true,
      "managed": true,
      "origin": {
        "remote": "/tmp/acme-kits",
        "source": "/tmp/acme-kits",
        "subpath": "extensions/features/demo",
        "ref": "main",
        "refType": "branch",
        "commit": "5e6a30d81a0ae3c73172c29f429ee0ba84a28597",
        "installedAt": "2026-08-26T19:42:23Z",
        "locallyModified": false
      },
      "defaultEnabled": false,
      "needsRoot": false,
      "ports": [
        { "container": 6080, "publish": true, "hostAllocation": "auto", "label": "noVNC", "openUrl": "http://localhost:{host_port}/" }
      ],
      "allowedDomains": ["downloads.example.com"]
    }
  ]
}
```

### Fields for every kind

| Field | Meaning |
|-------|---------|
| `kind` | `tool`, `feature`, or `command`. `command` was added after the first release, so consumers that switch on `kind` should tolerate unknown values. |
| `source` | `builtin`, `user`, or `override` (a user directory shadowing a built-in of the same name) |
| `managed`, `origin` | Installed by Enclave, and its [provenance](installing.md#provenance) without `installedBy` and `treeHash`, plus `locallyModified` (the files differ from the recorded tree hash) |
| `originError` | Replaces `origin` when the sidecar exists but could not be read; `managed` is then `false` |
| `hostCommands` | Host commands the extension ships; see [Auditing host commands](installing.md#auditing-host-commands) |
| `enabled` | Always `true` for tools (selected per run) and command extensions (nothing to enable); for features see below |

### Feature-only fields

| Field | Meaning |
|-------|---------|
| `selection` | The configured `features` directives, the layer they come from (`default`, `global`, `project`, `tool_override`, or `cli`), and the tool used for `tool_overrides` (`null` when no tool is configured). `directives: null` means no layer sets features: the defaults apply unless slim, devcontainer, or the qemu backend disables features. |
| `defaultEnabled`, `needsRoot` | The spec's values; always present |
| `ports` | Declared container ports; `hostAllocation` is `fixed` or `auto` |
| `allowedDomains`, `deniedDomains` | The spec's network domains as the gateway enforces them: lowercased, `*.` wildcards collapsed to the parent domain, ports stripped, sorted |
| `releaseHosts` | Declared hosts that receive a credential, as written in the spec (wildcards kept), sorted. When the feature is enabled they join the allowlist normalized like `allowedDomains`, so `*.github.com` also opens `github.com`. Hosts selected at session start through `serviceAuth.hostsFromCredential` are not included. |

Empty arrays are omitted, never `[]` or `null`. A feature's `enabled` says
whether a run would enable it: `--slim` and `backend: qemu` enable none,
`devcontainer` none unless `features` is set, and
`tool_overrides.<tool>.features` applies for the `--tool` or configured tool
(an unknown `--tool` has none). If a run would reject the options, stderr gets
a warning, `enabled` reflects the options without run-time coercion, and the
exit code is 0.

## `add`, `update`, `remove` result envelope

With `--json`, all three write one JSON document to stdout:

```json
{
  "schemaVersion": "1",
  "kind": "feature",
  "results": [
    {
      "name": "demo",
      "action": "installed",
      "commit": "5e6a30d81a0ae3c73172c29f429ee0ba84a28597",
      "path": "/home/you/.config/enclave/extensions/features/demo"
    }
  ]
}
```

| Field | Meaning |
|-------|---------|
| `action` | `installed`, `updated`, `unchanged`, `removed`, `skipped`, or `failed` |
| `commit`, `path` | Set on success |
| `error` | Set on a `failed` entry instead of `commit` and `path`; also explains a `skipped` entry |
| `warnings` | Warnings for this result |
| `hostCommands` | Host commands the extension ships |

- `error` never repeats `name` (`"has local modifications; pass --force to
  discard them"`); prepend it for a full line, as the human output does.
- The document is the whole report: no other stdout, and no human narration
  (discovery, skip and warn lines, capability summary, hints), not even on
  stderr.
- An error before any extension is touched (unreachable source, rejected
  flags) is a `failed` result with an empty `name`, so stdout always parses.
- The exit code is non-zero if any result failed, even if others succeeded.
