# Documentation

Reference documentation for Eclipse Enclave. For a guided introduction, see the
[website](https://enclave.eclipse.dev/docs).

## Using Enclave

| I want to … | Read |
|-------------|------|
| Install, update, or check requirements | [install.md](install.md) |
| Look up a command or flag | [cli-reference.md](cli-reference.md) |
| Run named, background, or parallel sessions; clean up stored data | [persistence.md](persistence.md) |
| Log in, pass secrets or API keys, use SSH keys | [auth.md](auth.md) |
| Allow domains, read the network log, reach host services | [networking.md](networking.md) |
| Set config keys, understand precedence, use environment variables | [configuration.md](configuration.md) |
| Override tool settings, patch tool config, add skills | [tool-config.md](tool-config.md) |
| Choose a tool, base image, or devcontainer; control rebuilds | [tools.md](tools.md) |
| Mount extra directories or sensitive paths | [mounts.md](mounts.md) |
| Understand Git identity and config protection in the sandbox | [git.md](git.md) |
| Use Podman or the experimental QEMU backend | [backends.md](backends.md) |
| Add my own `enclave <name>` commands | [commands.md](commands.md) |
| Script Enclave with JSON output | [integration.md](integration.md) |
| Run Enclave from Windows | [windows.md](windows.md) |

## Extensions

| I want to … | Read |
|-------------|------|
| Understand tools, features, and command extensions | [extensions/README.md](extensions/README.md) |
| Install extensions from a Git repository | [extensions/installing.md](extensions/installing.md) |
| Add a tool | [extensions/adding-a-tool.md](extensions/adding-a-tool.md) |
| Add a feature | [extensions/adding-a-feature.md](extensions/adding-a-feature.md) |
| Look up a `spec.yaml` field | [extensions/spec-reference.md](extensions/spec-reference.md) |
| Declare credentials, service auth, or providers | [extensions/credentials.md](extensions/credentials.md) |
| Parse `tools\|features\|commands` JSON output | [extensions/json-output.md](extensions/json-output.md) |

## Security

| Page | Covers |
|------|--------|
| [security/README.md](security/README.md) | Security boundaries and residual risks |
| [security/host-hardening.md](security/host-hardening.md) | Rootful Docker host hardening |
| [security/rootless.md](security/rootless.md) | Rootless engines: Podman supported, rootless Docker not |

## Contributing and internals

| Page | Covers |
|------|--------|
| [../CONTRIBUTING.md](../CONTRIBUTING.md) | Build quick start, where changes go, pull request checklist |
| [DEV.md](DEV.md) | Full build, test, packaging, and code-generation guide |
| [ARCHITECTURE.md](ARCHITECTURE.md) | Architecture, repository layout, option resolution, diagrams |
| [RELEASING.md](RELEASING.md) | Release process and release acceptance testing |
| [runtime/stores.md](runtime/stores.md) | Persistent store lifecycle and auth reconciliation; internal native-auth projection and exchange are mapped in [ARCHITECTURE.md](ARCHITECTURE.md#runtime-execution) |
| [runtime/network-request-flow.md](runtime/network-request-flow.md) | Restricted-mode request flow |
| [DEPENDENCIES.md](DEPENDENCIES.md) | Direct Go module dependencies; the [Dash inventory](../DEPENDENCIES) covers license checks |

Diagrams are Mermaid blocks embedded in
[ARCHITECTURE.md](ARCHITECTURE.md#diagrams) (Docker runtime and stores, Go
code generation) and
[runtime/network-request-flow.md](runtime/network-request-flow.md#diagram).
