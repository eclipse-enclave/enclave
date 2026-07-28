# Developer Guide

Concise notes for contributors working on the enclave codebase.

## Requirements

- Go 1.24.x (see `go.mod` toolchain)
- Docker daemon running (for runtime testing)
- Linux or macOS host. Native Windows is unsupported; use WSL2, where the
  Linux instructions apply. `make cross-build` compiles Windows targets only
  to guard code portability.

## Common Commands

Build the CLI:

```bash
make build
```

Install to your PATH:

```bash
make install
```

Run tests:

```bash
make test
```

Format code:

```bash
gofmt -w .
```

Lint only changed files/packages (faster than full `make lint` during iteration):

```bash
make lint-changed                    # staged + unstaged + untracked changes
make lint-changed BASE_REF=main      # all changes since main
```

## Running Changes from the Working Copy

enclave embeds its runtime assets, but a development binary uses checkout files
first. It locates an "app root" in this order (`internal/config/paths.go`,
`discoverAppRoot`):

1. `ENCLAVE_HOME`, which must contain the required assets when set.
2. Walk up from the binary's directory until a valid app root is found.
3. Extract the embedded assets into a content-hash-keyed directory under the
   platform cache root.

On Linux the extraction store is
`${XDG_CACHE_HOME:-~/.cache}/enclave/assets/<hash>/`. On macOS it is
`~/Library/Caches/org.eclipse.enclave/assets/<hash>/`. Each distinct asset set
uses its own atomically published directory and does not modify other entries. The tree
is reproducible from the binary, so deleting it is safe; the next run extracts
it again.

Because `bin/enclave` lives inside the checkout, tier 2 finds the repository
root. Running an in-tree binary therefore uses the working copy's assets:

```bash
make build
./bin/enclave --tool <tool> [args…]
```

When in doubt, pin the app root explicitly. This defeats a stale `ENCLAVE_HOME`,
an installed `enclave` earlier on your `PATH`, or launching from an unexpected
directory:

```bash
ENCLAVE_HOME="$(pwd)" ./bin/enclave --tool <tool> [args…]
```

### Picking up asset and template changes

Tool templates and other build inputs are baked into the Docker image at build
time, not mounted at run time. The build check hashes asset **content** into the
image's `enclave.hash` label, so editing an asset normally triggers an
automatic rebuild. To force it, pass `--rebuild`:

```bash
./bin/enclave --tool <tool> --rebuild
```

Gotchas:

- Invoke `./bin/enclave`, not an installed `enclave` on your `PATH`. An
  installed binary uses the assets embedded when it was built, not your
  checkout.
- Canonical full config overrides under `~/.config/enclave/tools/<tool>/`
  and `~/.config/enclave/projects/<hash>/<tool>/config/`, plus JSON/TOML patches
  under the global/project `patches/<tool>/` directories, can shadow your
  working-copy template. Clear them if you want to see the in-tree version.
- `--no-cache` is **not** the flag for this — it only disables runtime package
  cache mounts, not `docker build` caching or asset refresh. Use `--rebuild`.

## Pre-commit Checks

Before committing, run:

```bash
make build
make test
make lint
```

Exception: if a change only touches files under `docs/`, these make targets are
not required.

## Code Generation

Generated files are checked in. If you change the option definitions or tool
imports, regenerate and commit the outputs.

Generate all:

```bash
make generate
```

Or run directly:

```bash
go generate ./internal/config ./cmd/enclave
```

Notes:
- Option definitions live in `internal/config/options_def.go`.
- Generated outputs:
  - `internal/config/options_registry_gen.go`
  - `internal/config/options_cli_gen.go`
  - `internal/model/option_sources_gen.go`
  - `cmd/enclave/tool_imports.go` (tool extension Go imports)

## Adding or Updating Options

1) Add fields in `model.Options` (and related embedded struct).
2) Add defaults in `config.Defaults` if configurable.
3) Add the option entry in `internal/config/options_def.go`.
4) Run code generation (`make generate`).
5) Run tests (`go test ./...`).
6) Update docs for user-visible flag behavior (`docs/cli-reference.md`,
   `docs/configuration.md`, `docs/ARCHITECTURE.md`, and related command docs).

For CLI-only options (not configurable via config files), omit `DefaultsField`
and use `Apply: ApplyNone` in `options_def.go` (for example:
`--force-base-image` and `--no-rebuild`).

### Practical Checklist

Use this when adding a new option so you do not stop after `options_def.go`:

- Core types:
  add the field to the relevant `model.*Options` struct in
  `internal/model/types.go`.
- Config-backed options:
  add the field to `config.Defaults` in `internal/config/config.go`.
- Removed config keys:
  add the key to `removedConfigFields` in `internal/config/config.go` so
  `readDefaults()` rejects it loudly instead of silently ignoring it.
- Option registry:
  add the definition in `internal/config/options_def.go`.
- Generated outputs:
  run `make generate` after changing `options_def.go`.
- Guardrail tests:
  if you added a config-backed option or new option source, update the fixture
  values in:
  - `internal/config/merge_test.go`
  - `internal/model/option_sources_test.go`
- Behavioral tests:
  add or update focused tests for parsing and runtime behavior, not just the
  generated files.
- Docs:
  update `docs/cli-reference.md`, `docs/configuration.md`,
  `docs/ARCHITECTURE.md`, and any command docs that describe the affected
  behavior.
- Security-sensitive options:
  update `docs/security/README.md` and review whether project config guardrails
  or validation logic need changes.

Quick rule of thumb:
- CLI-only option: model field, `options_def.go`, generate, tests, docs.
- Config-backed option: model field, `config.Defaults`, `options_def.go`,
  generate, merge/source test fixtures, tests, docs.
- User-facing runtime option: all of the above plus security notes when
  applicable.

Backend option note: `--backend` is config-backed and defaults to `docker`; experimental `qemu` is available for foreground slim/no-feature unrestricted sessions.

Build option note: `--features` is available on CLI. In devcontainer mode,
unset features default to none, so pass `--features` explicitly when needed.
`--features none` is the explicit "no features" selection.

Project mount note: `--project-mount writable|readonly` is config-backed.
Project config may opt into `readonly`, but guardrails strip `writable` at
project scope so project defaults cannot weaken a stricter global setting.
`--worktree-metadata follow|readonly|none` works the same way for the
linked-worktree gitdir/commondir mounts: project config may strengthen the
inherited mode (`follow < readonly < none`), but cannot weaken it.

Config additive note: feature additive directives (`+`/`-`) are applied against
the implicit default-enabled feature set when `features` is unset. For example,
`["-node-dev"]` removes that default feature from the implicit set.

Config additive note: `host_config_paths` uses the same directive style, but it
resolves against the selected tool profile's reviewed `passthroughPaths`
instead of global defaults. Use `default` to include the built-in allow-list
before applying `+`/`-` edits.

## Option Source Precedence

When a value exists in multiple layers, enclave resolves in this order:

1. CLI flags
2. Selected tool override (`tool_overrides.<tool>`)
3. Project config (`~/.config/enclave/projects/<hash>/config.json`; per-project overrides live outside the worktree, keyed by project hash)
4. Global config (`~/.config/enclave/config.json`)
5. Built-in defaults

Security guardrail: project config (including `tool_overrides`) cannot elevate
guarded options such as `allow_all_network=true`. Those values are ignored with
warnings; use global config or CLI for explicit host opt-in.

## Network Policy Workflow

- Use `enclave network status` to inspect effective policy and runtime sync state.
- Use `enclave network apply` to push persisted policy to running gateways.
- Mutation commands (`network add-domain/remove-domain/set-mode`) auto-apply by default.
- Use `--no-apply` on mutation commands to persist only.
- Use `--all-running` with `network apply` or mutation commands to target all running gateways.
- `network set-mode unrestricted --global` is persisted immediately, but running restricted sessions must be restarted; unrestricted mode is not live-applied.

## Extensions

Every extension (tool or feature) must include a `README.md` in its directory.
See `docs/extensions/README.md` for the full extension architecture.

## Docker Weaving Scripts

Build composition scripts live in `runtime-assets/build-scripts/` and are
invoked from `Dockerfile`.

- Keep `Dockerfile` structural; move feature/tool composition logic into these scripts.
- Script contracts are documented in `runtime-assets/build-scripts/README.md`.
- Validate script changes with `make lint` (`shellcheck` runs on all `*.sh` files and `build-scripts/bin/` helpers).

## Docs and Diagrams

- Keep docs concise and aimed at experienced developers.
- Diagrams are Mermaid code blocks embedded in the docs; update them together with the behavior they document.

## Commits

Use Conventional Commits for commit subjects:
`type: short imperative summary`

Allowed types: `feat`, `fix`, `docs`, `refactor`, `test`, `chore`, `build`, `ci`,
`perf`, `revert`.

## Work Tracking

Work is tracked via GitHub Issues.
