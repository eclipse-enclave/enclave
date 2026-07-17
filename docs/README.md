# Documentation

## User Guides

- [cli-reference.md](cli-reference.md) — Commands and flags
- [configuration.md](configuration.md) — Config files, tool overrides, and config patches
- [auth.md](auth.md) — Authentication, OAuth, secrets, and SSH keys
- [networking.md](networking.md) — Network isolation, allowlists, and host-port bridging
- [persistence.md](persistence.md) — Sessions, persistent stores, and cleanup
- [tools.md](tools.md) — Built-in tools, images, devcontainers, and updates
- [session-status.md](session-status.md) — Terminal snapshots for external orchestrators
- [host-image-inbox.md](host-image-inbox.md) — Explicit host image import

## Developer Documentation

- [ARCHITECTURE.md](ARCHITECTURE.md) — Architecture, repository layout, and option resolution
- [DEV.md](DEV.md) — Build, test, generation, and contribution workflow
- [extensions/README.md](extensions/README.md) — Tool and feature extension architecture
- [extensions/adding-a-tool.md](extensions/adding-a-tool.md) — Adding a tool extension
- [DEPENDENCIES.md](DEPENDENCIES.md) — Direct Go module dependencies

## Runtime Internals

- [runtime/stores.md](runtime/stores.md) — Persistent store lifecycle and auth reconciliation
- [runtime/auth-sync.md](runtime/auth-sync.md) — Explicit auth import and export
- [runtime/network-request-flow.md](runtime/network-request-flow.md) — Restricted-mode request flow

## Security

- [security/README.md](security/README.md) — Current security boundaries and residual risks
- [security/host-hardening.md](security/host-hardening.md) — Rootful Docker host hardening
- [security/rootless.md](security/rootless.md) — Rootless Docker compatibility status

## Diagrams

Diagrams are Mermaid blocks embedded in the documentation:

- [ARCHITECTURE.md](ARCHITECTURE.md#diagrams) — Docker runtime and store layout, Go code generation
- [runtime/network-request-flow.md](runtime/network-request-flow.md#diagram) — Restricted network request flow
