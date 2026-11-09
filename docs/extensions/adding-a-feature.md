# Adding a feature

Steps to add a feature extension (`kind: mixin`): development tools that are
installed into every tool's image when the feature is enabled. Field semantics
are in the [spec reference](spec-reference.md#feature-fields).

New features that are not part of core go to
[enclave-extensions](https://github.com/eclipse-enclave/enclave-extensions)
under the same layout; see
[CONTRIBUTING.md](../../CONTRIBUTING.md#where-it-goes).

## 1. Create the directory and spec

Create `extensions/features/<feature>/spec.yaml`:

```yaml
schemaVersion: "1"
kind: mixin
name: myfeature
description: My development tools
aptPackages: [tool1, tool2]
priority: 70
```

`name` must match the directory name. Add `defaultEnabled: false` for an opt-in
feature.

## 2. Add install and startup scripts (optional)

| File | Use |
|------|-----|
| `install.sh` | Custom installation. Set `needsRoot: true` if it needs root. |
| `feature-entrypoint.d/setup.sh` | Runtime initialization, for every tool |
| `skills/` | Agent skills added to the tool's skills when the feature is enabled |
| `commands/host/<name>` | Host commands; see [Custom commands](../commands.md#shipping-host-commands-in-an-extension) |

`install.sh` may rely only on the feature's own `aptPackages` or its own
downloads, never on packages from another feature; see
[Installation order](spec-reference.md#installation-order).

## 3. Declare network, credentials, ports, caches (optional)

- `network.allowedDomains` for hosts the tooling must reach.
- `credentials`, `serviceDomains`, `serviceAuth`, `configDir`, `authFiles` for
  authenticated tooling such as `github-cli`; see
  [Credentials and providers](credentials.md).
- `ports` and `caches`; see [Ports](spec-reference.md#ports-ports) and
  [Project caches](spec-reference.md#project-caches-caches).

## 4. Validate

- Run `enclave validate-extensions`.
- Enable the feature (`"features": ["+myfeature"]` or `--features +myfeature`)
  and start a session. Changing the feature selection rebuilds the image on the
  next run.
- For a built-in feature, update the feature table in
  [Extensions](README.md#built-in-features).

## Guidelines

| Setting | Guideline |
|---------|-----------|
| `priority` | Lower installs earlier. Use 40–60 for core tools, 70–80 for language tools, 90+ for shell and UI. |
| `aptPackages` | Prefer apt packages over `install.sh` when possible. |
| `needsRoot` | Set `true` only if the install script needs root, for example to add apt repositories. |
| `feature-entrypoint.d/` | Use sparingly; these scripts run on every container start. |
| `failOnInstallError` | Set `true` if a failed install should fail the build; see [Install failures](spec-reference.md#install-failures). |
