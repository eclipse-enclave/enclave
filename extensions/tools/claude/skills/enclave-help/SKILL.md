---
name: enclave-help
description: Answer questions about Enclave by reading the bundled documentation
allowed-tools: Read, Glob, Grep
---

# Enclave Help

Answer the user's question about Enclave by reading the relevant documentation
files bundled at `/usr/share/doc/enclave/`.

## Important context

You are running inside an Enclave container. Host-global configuration
(e.g. `~/.config/enclave/config.json`, network policies, secrets) lives on the
host filesystem and is not directly editable from inside the container.
When a question involves host-side configuration changes, explain what the
user needs to do on the host or via the `enclave` CLI rather than
attempting in-container file edits.

## Documentation index

| Path | Topic |
|------|-------|
| `docs/README.md` | Documentation index |
| `docs/install.md` | Requirements, installation, updates, running as root, upgrade notes |
| `docs/cli-reference.md` | All commands and flags |
| `docs/configuration.md` | Config files, precedence, keys, list options, environment variables |
| `docs/tool-config.md` | Tool config overrides, managed skills, config patches |
| `docs/persistence.md` | Sessions, background mode, stored data, cleanup |
| `docs/git.md` | Git identity, config protection, commit signing |
| `docs/mounts.md` | Project mount, extra directories, sensitive mounts |
| `docs/networking.md` | Network isolation, gateway, network log, port bridging |
| `docs/auth.md` | Authentication, secrets, API keys, SSH keys |
| `docs/tools.md` | Supported agent tools, images, devcontainers, updates |
| `docs/backends.md` | Docker, Podman, and QEMU backends |
| `docs/commands.md` | Custom host and session commands |
| `docs/integration.md` | JSON output contracts for scripts |
| `docs/windows.md` | Windows (WSL2) launcher |
| `docs/ARCHITECTURE.md` | Detailed architecture with diagrams |
| `docs/extensions/` | Extension system (installing, adding tools and features, spec reference) |
| `docs/runtime/` | Runtime internals (stores, network request flow) |
| `docs/security/` | Security model and host hardening |
| `extensions/tools/<tool>/README.md` | Per-tool documentation |
| `extensions/features/<feature>/README.md` | Per-feature documentation |

All paths are relative to `/usr/share/doc/enclave/`.

## Steps

1. Read the doc(s) most relevant to the user's question.
2. Synthesize a clear, concise answer from what you find.
3. If the docs don't cover the topic, say so explicitly.
