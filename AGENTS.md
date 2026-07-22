# Agent Instructions

Read `README.md` before making user-facing changes.

## Architecture

- See `docs/ARCHITECTURE.md` for the architecture, repository layout, and container assets.
- Flow: CLI args → profile loading → image/bundle build check → store/auth/network setup → selected backend (`docker` by default, experimental `qemu` for a narrow path).
- Key packages (all under `internal/`): `app` (orchestration), `runtime` (container execution, mounts, auth injection), `gateway` (network-isolation sidecar), `network` (policy loading and dnsmasq config generation), `config` (profile and path resolution), `model` (shared types).
- Keep packages under `internal/` focused and avoid unnecessary coupling.

## Collaboration

- Be direct and critical when needed; flag unclear requirements before coding.
- Avoid hype, filler, and unrelated refactors.
- Prefer clear names over explanatory comments; comment only non-obvious behavior.
- Work is tracked via GitHub Issues.

## External Integrations

- Treat CLI JSON schemas, background-run output, lifecycle commands, container naming, and managed tmux behavior as integration contracts for external consumers; coordinate breaking changes.
- Prefer structured CLI output over Docker labels or human-readable output for integration contracts.

## Build and Test

- Build with `make build` (or `go build ./cmd/enclave`); run tests with `make test`.
- Format modified Go files with `gofmt -w`.
- Run `make lint-changed` for fast feedback while iterating.
- Before committing, run `make build`, `make test`, and `make lint`.
- Documentation-only changes under `docs/` do not require those checks.
- Run `make generate` after changing `internal/config/options_def.go` or tool-extension Go code. Generated files are checked in — rebuild and commit them.

## License Headers

- All technically feasible first-party source and configuration files must carry the Eclipse-style MIT header used in this repository:
  ```text
  Copyright (C) <year> <copyright holder> and others.

  This program and the accompanying materials are made available under the
  terms of the MIT License, which is available in the project root.

  SPDX-License-Identifier: MIT
  ```
- Preserve existing copyright years and owners. New files use their initial creation year and the actual legal copyright holder; do not guess ownership.
- Files created by splitting, refactoring, or generating existing content inherit the source header. Update generators so regenerated files retain the header.
- YAML files under `.github/` do not require headers.
- Headers are not required for Markdown, JSON, lock/sum files, test fixtures, rendered SVGs, images, PDFs, binaries, or imported third-party content.
- Run `make check-license-headers` after adding or moving files.

## Git

Use Conventional Commits: `type: short imperative summary`.

Allowed types: `feat`, `fix`, `docs`, `refactor`, `test`, `chore`, `build`, `ci`, `perf`, `revert`.

Write a single-line subject; add a body only when it conveys something the subject cannot (non-obvious motivation, trade-offs, or context).

When the user explicitly requests a WIP commit, prefix the subject with `wip: `.

Do not push, publish releases, or modify remote issues/PRs unless the maintainer explicitly requests it.

## Documentation

- Keep documentation concise and aimed at experienced developers.
- Document user-visible flags and behavior changes.
- Diagrams are Mermaid code blocks embedded in the docs; update them together with the behavior they document.
- Update `docs/ARCHITECTURE.md` and `docs/README.md` when repository layout changes.
- Update `docs/ARCHITECTURE.md`, this file, and relevant extension docs when profile, template, patch, or runtime-asset organization changes.
- Update `docs/cli-reference.md`, `docs/configuration.md`, and `docs/DEV.md` when CLI flags or option definitions change.

## Extensions

- See `docs/extensions/README.md` for the extension architecture and `docs/extensions/adding-a-tool.md` for the step-by-step guide; every tool or feature extension must include a `README.md`.
- `extensions/tools/<tool>/spec.yaml` (`kind: sandbox`) defines tool runtime, auth, network, settings, and provider behavior.
- `extensions/features/<feature>/spec.yaml` (`kind: mixin`) defines optional packages, runtime setup, auth, and network behavior.
- Tool settings templates live under `extensions/tools/<tool>/templates/` and are baked into the image.
- Full host config overrides use `~/.config/enclave/tools/<tool>/` globally and `~/.config/enclave/projects/<hash>/<tool>/config/` per project.
- JSON/TOML patches mirror native paths under `~/.config/enclave/patches/<tool>/` globally and `~/.config/enclave/projects/<hash>/patches/<tool>/` per project.
- Shared skills use `~/.config/enclave/skills/<skill>/` globally and `~/.config/enclave/projects/<hash>/skills/<skill>/` per project; tool-specific skills use the canonical tool config roots at the native skills path.

## Host Data

Enclave follows platform-standard roots: the XDG base directories on Linux and other Unixes, and the Apple layout under `~/Library/Application Support/org.eclipse.enclave/` and `~/Library/Caches/org.eclipse.enclave/` on macOS (`XDG_*` overrides are ignored there). Root resolution lives in `internal/config/host_roots.go`. Linux examples:

- Config: `~/.config/enclave/` (`$XDG_CONFIG_HOME`)
- State: `~/.local/state/enclave/` (`$XDG_STATE_HOME`)
- Cache: `~/.cache/enclave/` (`$XDG_CACHE_HOME`)
- Installed assets: `~/.local/share/enclave/` (`$XDG_DATA_HOME`)
- Per-project agent memory: `~/.local/state/enclave/projects/<hash>/<tool>/memory/` (Claude only; agent-writable, never shared between projects or agents)
- User-defined subcommands: `~/.config/enclave/commands/{host,session}/` (executable files become `enclave <name>` verbs)

Per-project config/state is keyed by project hash and kept outside the worktree. See `docs/configuration.md` and `docs/runtime/stores.md` for details.
