# herdr host lifecycle provider

Requires Python 3 on the host; Enclave does not install it automatically. This
host-only feature has `defaultEnabled: false` and no container packages. It is not
a built-in or auto-discovered from this checkout.

Install from a Git source containing these files (after they exist as Git content):

```sh
enclave features add /path/to/enclave --path examples/host-hooks/features/herdr --yes
```

Enable in global/project/per-tool config independently of `features`:

```json
{"host_hooks": {"feature:herdr": {"enabled": true, "settings": {}}, "builtin:session-tint": {"enabled": false}}}
```

Settings: `agents` maps tool names to Herdr labels for run/continue/resume;
empty labels disable tagging. `execRules` is an array of
`{"argv":["company-wrapper","--agent"],"agent":"claude"}` entries. Explicit exec
uses longest matching argv prefix (basename for the executable); conflicting
same-length rules decline tagging. Tool mappings never label arbitrary exec.

Only `HERDR_ENV=1` is eligible. Existing `HERDR_AGENT`, including empty opt-out,
is preserved. Shell, bare exec, background, IDE auto-background and attach decline.
Without a mapping, the executable basename must be in the read-only
`herdr server agent-manifests --json` catalog (one-second timeout), using
`HERDR_BIN_PATH` or `herdr` on PATH. Explicit aliases work without a catalog;
no static label allowlist or wrapper-shell interpretation is used. Missing,
slow or malformed catalogs warn and never block the session. Startup prompts or
Herdr's acquisition timing can still prevent recognition. A successful hook does
not indicate that the agent is ready for input.

The executable runs on your host with your privileges. Review it before enabling.
Missing interpreters and protocol failures produce optional provider-prefixed
stderr diagnostics. Inspect effective configuration with `enclave config`.
`enclave features update herdr` updates the provider without rebuilding Enclave
or images; `enclave features remove herdr` removes code but leaves the config
entry, which you should remove separately.

Run provider tests with `python3 examples/host-hooks/features/herdr/test_provider.py`.
The core external-only conformance tests copy providers into isolated installations;
they do not modify your installed integrations or Herdr panes. Catalog stubs do
not certify actual upstream Herdr acquisition.

See the [v1 contract](../../../../docs/extensions/host-hooks.md) for protocol,
ordering, environment/replay, bounds and cleanup semantics.
