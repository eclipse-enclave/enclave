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

- Treat CLI JSON schemas, background-run output, lifecycle commands, and container naming as integration contracts for external consumers; coordinate breaking changes. This includes `enclave tools|features|commands list --json` and the shared `add`/`update`/`remove` result envelope (both `schemaVersion: "1"`; see `docs/extensions/json-output.md`), `enclave config --json` (`schemaVersion: "1"`, `kind: "config"`), and `enclave network print --json` (`schemaVersion: "1"`, `kind: "network-policy"`). `docs/integration.md` documents all JSON contracts.
- Session provenance (`options` in `enclave ps --json`) is contract; the `enclave.options` label that stores it is not. Recorded options follow `AffectsSession` in `internal/config/options_def.go` and must never carry secret values.
- Prefer structured CLI output over Docker labels or human-readable output for integration contracts.
- HomeShell (`eclipse-enclave/homeshell`) is a known consumer; `docs/integration.md#known-consumers` says what it relies on.

## Build and Test

- Build with `make build` (or `go build ./cmd/enclave`); run tests with `make test`.
- The version comes from release tags via `scripts/version.sh`; never hardcode one. See [Versions](docs/RELEASING.md#versions).
- Format modified Go files with `gofmt -w`.
- Run `make lint-changed` for fast feedback while iterating.
- Before committing, run `make build`, `make test`, and `make lint`.
- Documentation-only changes under `docs/` do not require those checks.
- Run `make generate` after changing `internal/config/options_def.go` or tool-extension Go code. Generated files are checked in: rebuild and commit them.
- For dependency, toolchain, image, or GitHub Actions updates, run `make check-versions` and follow `.agents/skills/dependency-updates/SKILL.md`; minimum versions (Go, Docker, buildx, git) may not exceed what the supported Debian and Ubuntu LTS releases ship.

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

Do not edit `CHANGELOG.md` in a PR, except the release-prep PR. Describe user-visible changes in the `changelog` block of the PR description, or write `none` there, as the [pull request checklist](CONTRIBUTING.md#pull-request-checklist) explains.

When reviewing, a missing `CHANGELOG.md` entry is not a finding. These are: an empty or missing `changelog` block, a user-visible change marked `none`, a note not written in the user's terms, and a `Breaking changes` checkbox that disagrees with the `Breaking:` items.

## Documentation

- Keep documentation concise and aimed at experienced developers. Lead with common tasks, put reference tables next, and keep edge cases after the main flow.
- Give each fact one canonical page and link to it from elsewhere; `docs/README.md` maps topics to pages.
- Wrap Markdown prose at 80 columns. Use sentence-case headings; avoid em dashes, bold-label paragraphs, and filler.
- Document user-visible flags and behavior changes.
- Diagrams are Mermaid code blocks embedded in the docs; update them together with the behavior they document.
- Update `docs/ARCHITECTURE.md` and `docs/README.md` when repository layout changes.
- Update `docs/ARCHITECTURE.md`, this file, and relevant extension docs when profile, template, patch, or runtime-asset organization changes.
- Update `docs/cli-reference.md`, `docs/configuration.md`, and `docs/DEV.md` when CLI flags or option definitions change.
- Update the documentation index in `extensions/tools/*/skills/enclave-help/SKILL.md` when docs pages are added, renamed, or removed.

## Extensions

- Start with `docs/extensions/README.md` (architecture), `docs/extensions/spec-reference.md` (`spec.yaml` fields), and `docs/extensions/adding-a-tool.md` / `adding-a-feature.md` (step-by-step guides). Every tool or feature extension must include a `README.md`.
- Core ships six tools (Claude Code, Codex, Copilot, Theia with `theia-next`, OpenCode, Pi). New tools go to `eclipse-enclave/enclave-extensions`; see `CONTRIBUTING.md`.
- Kinds: tools (`kind: sandbox`, `extensions/tools/<tool>/`), features (`kind: mixin`, `extensions/features/<feature>/`), and command extensions (`kind: command`, user root only, host commands only). Tool settings templates live under `extensions/tools/<tool>/templates/` and are baked into the image.
- Memory scope, disable arguments, and preserved runtime state are declared with `sandbox.memoryScope`, `sandbox.noMemoryArgs`, and `sandbox.statePaths`; see [Agent Memory](docs/runtime/stores.md#agent-memory).
- User-side overrides, patches, and skills (paths and precedence) are documented in `docs/tool-config.md`.
- Provenance (`.enclave-source.json`) and `commands/` in an installed extension are excluded from the build context and image hash. See `docs/extensions/installing.md`.
- Every kind may ship `commands/host/<name>` executables, which run on the host as `enclave <name>` verbs. The capability summary reports them in its first row, the update diff repeats the warning, and the `hostCommands` field carries them in `list --json` and the `add`/`update`/`remove` result envelope. Keep all of those accurate when touching that code. Anything under `commands/` other than `host/` is a validation warning. See `docs/commands.md`.

## Host Data

Enclave uses the XDG base directories on Linux and other Unixes and the Apple layout on macOS (`XDG_*` overrides are ignored there); root resolution lives in `internal/config/host_roots.go`. Per-project config and state are keyed by project hash and kept outside the worktree. Agent memory is agent-writable: project scope follows the selected config anchor and tool, while session scope follows the checkout's config store. The full path table is in [Host directories](docs/configuration.md#host-directories); store details are in `docs/runtime/stores.md`.
