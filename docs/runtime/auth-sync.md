# Auth import and export

Enclave automatically reconciles provider auth files between a session's config
store and the selected shared auth store during startup and session
finalization. Explicit import/export commands are only for moving credentials
between the host tool's native config and Enclave stores.

## Import: host to store

Use import to seed an Enclave auth store from an existing host login:

```bash
enclave auth import --tool <tool>
```

Do not run the host tool concurrently while importing credentials it may be
refreshing.

## Export: store to host

Use export to copy credentials from an Enclave auth store back to the host
tool's native config location:

```bash
enclave auth export --tool <tool>
```

## Scope

- `--tool` is required.
- `--auth-scope shared|project` selects the shared identity store or the current
  project's config store.
- `--auth-name <slug>` selects a named shared identity and has no effect with
  project auth scope.
- Import/export is separate from declared environment-secret injection and the
  persisted env store.

See [Authentication and secrets](../auth.md) and
[Persistent stores](stores.md) for the complete lifecycle.
