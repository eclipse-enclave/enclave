---
name: enclave-help
description: Answer questions about Enclave by reading the bundled documentation
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
| `docs/ARCHITECTURE.md` | Detailed architecture with diagrams |
| `docs/cli-reference.md` | CLI flags and options |
| `docs/configuration.md` | Config files and settings |
| `docs/networking.md` | Network isolation and gateway |
| `docs/auth.md` | Authentication and API keys |
| `docs/persistence.md` | Persistent stores and session data |
| `docs/tools.md` | Supported agent tools |
| `docs/extensions/` | Extension system (adding tools, features) |
| `docs/runtime/` | Runtime behavior (stores, auth sync, network flow) |
| `docs/security/` | Security model and host hardening |
| `extensions/tools/<tool>/README.md` | Per-tool documentation |
| `extensions/features/<feature>/README.md` | Per-feature documentation |

All paths are relative to `/usr/share/doc/enclave/`.

## Steps

1. Read the doc(s) most relevant to the user's question.
2. Synthesize a clear, concise answer from what you find.
3. If the docs don't cover the topic, say so explicitly.
