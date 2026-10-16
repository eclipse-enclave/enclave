# session-tint host lifecycle provider

Requires Python 3 on the host; Enclave does not install it automatically. This
host-only feature has `defaultEnabled: false` and no container packages. It is not
a built-in or auto-discovered from this checkout.

Install from a Git source containing these files (after they exist as Git content):

```sh
enclave features add /path/to/enclave --path examples/host-hooks/features/session-tint --yes
```

Enable in global/project/per-tool config independently of `features`:

```json
{"host_hooks": {"feature:session-tint": {"enabled": true, "settings": {"color":"#2a0f12"}}, "builtin:session-tint": {"enabled": false}}}
```

Settings: `color` is a required `#rrggbb` string. `NO_COLOR` and
`ENCLAVE_COLOR` suppression follow Enclave's existing policy. The provider returns
OSC 11 enter and OSC 111 reset bytes for the generic `background` resource; it
needs no leave process. Disable `builtin:session-tint` when using this provider.
Terminal reset cannot run after SIGKILL.

The executable runs on your host with your privileges. Review it before enabling.
Missing interpreters and protocol failures produce optional provider-prefixed
stderr diagnostics. Inspect effective configuration with `enclave config`.
`enclave features update session-tint` updates the provider without rebuilding Enclave
or images; `enclave features remove session-tint` removes code but leaves the config
entry, which you should remove separately.

Run provider tests with `python3 examples/host-hooks/features/session-tint/test_provider.py`.
The core external-only conformance tests copy providers into isolated installations;
they do not modify your installed integrations or Herdr panes. Catalog stubs do
not certify actual upstream Herdr acquisition.

See the [v1 contract](../../../../docs/extensions/host-hooks.md) for protocol,
ordering, environment/replay, bounds and cleanup semantics.
