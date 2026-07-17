# Adding a Tool

Use this checklist to add a new agent CLI end-to-end. Keep changes minimal and focused.

A tool is a `kind: sandbox` extension. Its on-disk shape is a single
`spec.yaml` (camelCase fields) plus a few sibling files. The authoritative,
exhaustive field reference — with worked examples — lives in
[`docs/extensions/README.md`](README.md#tool-extensions) (and the
`specDocument` struct in `internal/config/spec.go`). This checklist covers the
*process*; consult the README for the full field list.

## 1) Create the tool extension directory
- Create `extensions/tools/<tool>/`.
- Add `spec.yaml` with `schemaVersion: "1"`, `kind: sandbox`, `name` (must match
  the directory name), `displayName`, `description`, and a `sandbox` block.
- Set `defaultIncluded: false` (top-level) for opt-in tools that should not ship
  in the default image.
- Add `install.sh` with the install steps (runs during image build).
- Add `gateway-allowlist.conf` for the tool's network allowlist.
- Optionally add `check-update.sh` if the tool should participate in automatic
  update probes.
- Add a `README.md` documenting the tool extension.

Minimal skeleton:

```yaml
schemaVersion: "1"
kind: sandbox
name: <tool>
displayName: <Human Name>
description: <one line>

sandbox:
  entrypoint: { run: [<binary>] }   # command to launch the tool
  configDir: .<tool>                # config dir under $HOME, persisted in the config store
  settingsFile: <tool>-settings.json
  settingsTarget: .<tool>/settings.json
```

## 2) Fill in the `sandbox` block

`sandbox.*` is enclave-native tool metadata. Common fields (see the README for
the complete set and semantics):

- `entrypoint.run`: argv used to launch the tool (sbx-style command).
- `configDir`: config directory under `$HOME`, backed by the persistent config store.
- `skillsDir`: (optional) path below `configDir` where shared and tool-specific
  managed skills are composed. It may be home-relative or absolute, matching
  `configDir`.
- `settingsFile` / `settingsTarget`: aggregated template filename under
  `/usr/local/share/enclave/templates/` and its target below `configDir`.
- `yoloFlag` / `yoloEnabled`: flag to skip approvals, and whether yolo mode is on
  by default (`yoloEnabled` defaults to `true`).
- `continueArgs` / `resumeArgs`: args for `enclave continue` (latest session)
  and `enclave resume` (session picker). One-step fallback between them; if
  both are missing, those commands are unsupported for the tool.
- `passthroughPaths`: (optional) narrow, reviewed allow-list for
  `host_config=passthrough` — keep it config-only.
- `qemuMinMemoryMiB`: (optional) minimum memory for generated QEMU microVM
  bundles; the effective size is max(default 4096 MiB, this value).
- `qemuStoreCacheMmap`: (optional) mount the tool's config store with 9p
  `cache=mmap`; required when the store holds SQLite databases in WAL mode.
- `hostConfigDir` / `hostCredentialsFile` / `hostOauthJson`: (optional) host-side
  locations used by `auth import/export`.

## 3) Declare credentials, service auth, and providers

Secrets are split across `credentials` and `network` (see the README's
"Credentials, service auth, and providers" section for the full model):

- `credentials.sources.<id>`: the credential itself — `env` (one or more
  env-var aliases) and the enclave-native `apiKey` bool (`false` for
  OAuth/session tokens; omitted/`true` means it's an API key).
- `network.serviceDomains` (`host -> service-id`) + `network.serviceAuth.<id>`
  (`headerName`, optional `valueFormat` with a Go `fmt`-style `%s`): how the
  gateway injects the credential as an HTTP header when proxying to those hosts.
- `providers[]`: enclave-native auth provider — `name`, `credentials` (a list
  of `credentials.sources` keys), `authFiles` (relative to `sandbox.configDir`),
  `authSession` (`mode: any|all` + `checks`), `oauthPorts`, and
  `securestorageDirEnv`.

## 4) Optional: declarative ports

A tool can publish a default port without any Go code by declaring top-level
`ports`:

```yaml
ports:
  - container: 3000
    publish: true
    label: Theia IDE
    openUrl: "http://localhost:{host_port}"
```

On start, enclave publishes each `publish: true` port and prints the resolved
`openUrl` (`{host_port}` is replaced with the resolved host port; defaults to
`http://localhost:{host_port}`). While the session runs, `enclave ps` shows the
resolved URL in its `PORTS` column (published ports without a declaration appear
as `host:port`). Notes:

- **Loopback by default.** Published ports bind to `127.0.0.1` on the host.
  Binding to another interface is an explicit opt-in via `-p` using the
  `ip:host:container` form (for example `-p 0.0.0.0:3000:3000`).
- **Network isolation.** Under isolation the tool shares the gateway sidecar's
  network namespace, so the binding is applied on the gateway container; off
  isolation it is applied on the tool container. Either way the port is
  reachable the same way, including for background/detached sessions.
- **Host port.** The host port currently equals the container port, so two
  concurrent sessions of the same tool contend for the same host port. Dynamic
  host-port allocation is a planned follow-up.
- `providers[].oauthPorts` is a separate mechanism for OAuth loopback callbacks
  and is unaffected.

## 5) Add a settings template
- Create `extensions/tools/<tool>/templates/<filename>`.
- It is copied into the image as `/usr/local/share/enclave/templates/<tool>-<filename>`.
- Reference it from `sandbox.settingsFile` (aggregated name like
  `<tool>-settings.json`) and `sandbox.settingsTarget` (path below `configDir`).
- Include privacy defaults (disable telemetry, update checks, session logging)
  when supported. Keep comments concise and focused on non-obvious behavior.

## 6) Wire template setup in `entrypoint.sh`
- Create `extensions/tools/<tool>/entrypoint.d/setup.sh`.
- Create the config directory.
- Template copying is handled centrally via `sandbox.settingsFile` +
  `sandbox.settingsTarget`.
- Export any tool-specific env vars (example: `TOOL_HOME`).

## 7) Add a DNS allowlist
- Create `extensions/tools/<tool>/gateway-allowlist.conf`.
- Keep the allowlist minimal; include registries only when needed (PyPI/npm/etc.).

## 8) Document and validate
- Update the built-in tool list in `docs/tools.md`.
- Update the common config-path table in `docs/configuration.md` if a template was added.
- Update `docs/ARCHITECTURE.md`, `docs/extensions/README.md`, and `AGENTS.md` if needed.
- If you touched Go files, run `gofmt` on them.
- If the tool has `go/` hooks or handlers, run `go generate ./cmd/enclave`.
- Run `enclave validate-extensions`.
- If a Mermaid diagram documents the changed behavior, update it.

## 9) Optional: auth and UX checks
- Confirm declared `credentials.sources` and `providers[].credentials` cover all
  required env-based auth inputs, and mark OAuth/provider-token credentials with
  `apiKey: false` when `--no-api-key` should not suppress them.
- Confirm `enclave auth import --tool <tool>` copies required auth files
  (`providers[].authFiles`).
- Sanity check the tool runs inside the container (`enclave --tool <tool>`).
