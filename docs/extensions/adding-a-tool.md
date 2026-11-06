# Adding a tool

Steps to add a new agent CLI as a tool extension (`kind: sandbox`). Field
semantics are in the [spec reference](spec-reference.md).

## Where it goes

A new tool goes to
[enclave-extensions](https://github.com/eclipse-enclave/enclave-extensions),
the official extensions repository, under `tools/<tool>/`. Core ships only
Claude Code, Codex, Copilot, Theia (with `theia-next`), OpenCode, and Pi; see
[CONTRIBUTING.md](../../CONTRIBUTING.md#where-it-goes) for how that set
changes.

The file layout is the same in both repositories. Use `tools/<tool>/` in
enclave-extensions where the steps below say `extensions/tools/<tool>/`. The
core documentation and code-generation steps apply only to built-in tools.

## 1. Create the directory and spec

Create `extensions/tools/<tool>/` with:

| File | Content |
|------|---------|
| `spec.yaml` | `schemaVersion: "1"`, `kind: sandbox`, `name` (must match the directory name), `displayName`, `description`, and a `sandbox` block |
| `install.sh` | Install steps; runs during the image build |
| `gateway-allowlist.conf` | The tool's DNS allowlist (step 6) |
| `README.md` | Documents the extension |
| `check-update.sh` | Optional; opts into [automatic updates](spec-reference.md#automatic-updates-check-updatesh) |

Set top-level `defaultIncluded: false` for an opt-in tool that should not ship
in the default image.

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

### Installing from npm

For a tool that is a plain npm package, `install.sh` can call the shared
helper. It installs through the private agent Node runtime and checks that the
binary landed on `PATH`:

```sh
enclave-install-npm-tool [npm-flag ...] <package> <binary> [label]
```

- Leading `-`-prefixed arguments are forwarded to `npm install`. They must be
  self-contained (`--flag` or `--flag=value`); a flag with a separate value,
  such as `--omit dev`, would consume the package argument.
- Pass `--ignore-scripts` unless the package needs its lifecycle scripts, so
  dependency-authored code does not run at image build time. Check upstream
  first: some packages resolve a platform binary in a `postinstall` and break
  without it.

## 2. Fill in the `sandbox` block

Set the fields the tool needs; see
[Tool fields](spec-reference.md#tool-fields-sandbox). Usually:

- `entrypoint.run`, `configDir`
- `yoloFlag` (and `yoloEnabled` if yolo mode should be off by default)
- `continueArgs` and `resumeArgs`
- `skillsDir` if the tool supports skills
- `memoryDir`, `memoryScope`, `noMemoryArgs`, `statePaths` if the tool keeps
  memory or runtime state. Read
  [Agent memory](../runtime/stores.md#agent-memory)
  before enabling background memory generation.
- `passthroughPaths` for `host_config=passthrough`, config-only
- `hostConfigDir`, `hostCredentialsFile`, `hostOauthJson` for
  `enclave auth import/export`
- `qemuMinMemoryMiB`, `qemuStoreCacheMmap` if the defaults do not work on the
  qemu backend

## 3. Declare credentials and providers

Declare `credentials.sources`, `network.serviceDomains` and
`network.serviceAuth`, and `providers[]`; see
[Credentials and providers](credentials.md). Mark OAuth and session tokens
with `apiKey: false`.

## 4. Optional: ports and caches

- Declare top-level `ports` to publish a port without Go code; see
  [Ports](spec-reference.md#ports-ports).
- Declare top-level `caches` to persist container-home directories per
  project; see [Project caches](spec-reference.md#project-caches-caches).

## 5. Add a settings template and startup script

1. Create `extensions/tools/<tool>/templates/<filename>`. It is copied into the
   image as `/usr/local/share/enclave/templates/<tool>-<filename>`.
2. Reference it from `sandbox.settingsFile` (`<tool>-<filename>`) and
   `sandbox.settingsTarget` (path below `configDir`). Template copying is
   handled centrally from these two fields.
3. Include privacy defaults (disable telemetry, update checks, session logging)
   when the tool supports them. Keep comments short and limited to non-obvious
   behavior.
4. Create `extensions/tools/<tool>/entrypoint.d/setup.sh` to create the config
   directory and export tool-specific env vars (for example `TOOL_HOME`).

See [Templates](spec-reference.md#templates).

## 6. Add a DNS allowlist

Create `extensions/tools/<tool>/gateway-allowlist.conf`. Keep it minimal and
include registries (PyPI, npm, ...) only when needed. It can include fragments
with `conf-file=`.

## 7. Document and validate

- In enclave-extensions, list the tool in its `README.md`.
- For a built-in tool, update the tool list in `docs/tools.md`, the config-path
  table in `docs/tool-config.md` if a template was added, and
  `docs/ARCHITECTURE.md`, `docs/extensions/`, and `AGENTS.md` if needed.
- Run `gofmt` on any Go files you touched.
- For a built-in tool with `go/` hooks or handlers, run
  `go generate ./cmd/enclave`.
- Run `enclave validate-extensions`.
- Update any Mermaid diagram that documents the changed behavior.

## 8. Check auth and the session

- Run the [credentials checklist](credentials.md#checklist-for-authors).
- Start the tool in the container with `enclave --tool <tool>`.
