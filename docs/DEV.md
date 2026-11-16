# Developer guide

How to build, test, and change the Enclave codebase. For the contribution
process (issues, ECA, pull requests), see [CONTRIBUTING.md](../CONTRIBUTING.md);
for how the code is organized, see [ARCHITECTURE.md](ARCHITECTURE.md).

## Prerequisites

The [quick start](../CONTRIBUTING.md#building-from-source) lists the basics:
Go 1.24 or newer, Make, and `make build|install|test|lint`. In addition:

- Releases and most CI jobs build with the `toolchain` in `go.mod`, which `go`
  downloads when the local Go is older. One CI job uses the exact `go`
  directive version with `GOTOOLCHAIN=local`.
- A running Docker or Podman engine, for runtime testing.
- Linux or macOS. Native Windows is unsupported; use WSL2.
- `golangci-lint`, `gosec`, and `shellcheck` for `make lint`
  ([Before you commit](#before-you-commit)).

## More make targets

| Command | Purpose |
|---------|---------|
| `make lint-changed` | Lint staged, unstaged, and untracked changes; faster than `make lint` |
| `make lint-changed BASE_REF=main` | Lint all changes since `main` |
| `make generate` | [Code generation](#code-generation) |
| `make cross-build` | Compile `./...` for `darwin/arm64`, `darwin/amd64`, `linux/arm64`, `windows/amd64`, and `windows/arm64`; the whole tree must stay portable, although the Windows binary is only the [WSL launcher](#windows-launcher) |
| `make check-versions` | [Dependency updates](#dependency-updates) |
| `make deb`, `make rpm`, `make deb-quick` | [Packaging](#packaging) |

Format Go code with `gofmt -w`. Plain `make` equals `make build`.

## Run changes from the working copy

```bash
make build
./bin/enclave --tool <tool> [args…]
# Pin the app root against a stale ENCLAVE_HOME or an installed enclave:
ENCLAVE_HOME="$(pwd)" ./bin/enclave --tool <tool> [args…]
```

### How the binary finds its assets

`discoverAppRoot` (`internal/config/paths.go`) tries, in order:

1. `ENCLAVE_HOME`, which must contain the required assets when set.
2. The binary's directory and its parents. `bin/enclave` therefore uses the
   checkout; package installs use `/usr/share/enclave`.
3. The embedded assets, extracted to
   `${XDG_CACHE_HOME:-~/.cache}/enclave/assets/<hash>/` (Linux) or
   `~/Library/Caches/org.eclipse.enclave/assets/<hash>/` (macOS).

Extraction (`internal/config/embedded_assets.go`):

- Each content hash gets its own directory, published atomically via a
  per-content lock, a temporary sibling, and a rename, so concurrent first runs
  share one complete entry. Missing or invalid entries are recreated, so
  deleting the tree is safe.
- The entry's mtime records its last use, refreshed at most daily under its
  lock. A failed refresh (a read-only cache, for example) keeps the entry in
  use.
- After extracting or refreshing, `removeUnusedAppAssets` removes entries
  unused for 30 days whose lock it can take without waiting.
- Shell completion runs before the root guard and never extracts: it uses a
  published entry or reads the built-in extension names from the binary.

### Picking up asset and template changes

Tool templates and other build inputs are baked into the image at build time,
not mounted at run time. The build check hashes asset content into the image's
`enclave.hash` label, so editing an asset normally triggers an automatic
rebuild. To force one:

```bash
./bin/enclave --tool <tool> --rebuild
```

| Pitfall | Fix |
|---------|-----|
| An installed `enclave` on your `PATH` uses the assets embedded when it was built, not your checkout | Invoke `./bin/enclave` |
| Full config overrides under `~/.config/enclave/tools/<tool>/` and `~/.config/enclave/projects/<hash>/<tool>/config/`, or JSON/TOML patches under the global or project `patches/<tool>/` directories, shadow your working-copy template | Clear them to see the in-tree version |
| `--no-cache` only disables runtime package cache mounts, not `docker build` caching or asset refresh | Use `--rebuild` |

## Before you commit

Run `make build`, `make test`, and `make lint`; changes only under `docs/` do
not need them. `scripts/install-go-linters.sh` installs the Go linters at CI's
versions into `~/.local/bin` (bump them there). Inside Enclave, the
`enclave-dev` feature from enclave-extensions installs the same versions
(with `+golang-dev`).

## Code generation

Generated files are checked in. After changing option definitions or tool
extension Go code, run `make generate` (`go generate ./internal/config
./cmd/enclave`) and commit the outputs:

| Input | Generated output |
|-------|------------------|
| `internal/config/options_def.go` (via `scripts/gen_options.go`) | `internal/config/options_registry_gen.go`, `internal/config/options_cli_gen.go`, `internal/model/option_sources_gen.go` |
| Tool extension Go packages (via `cmd/enclave/internal/gen_tool_imports`) | `cmd/enclave/tool_imports.go` (`//go:build !windows`) |

See also the [diagram](ARCHITECTURE.md#go-code-generation).

## Adding or updating options

User-facing model: [configuration.md](configuration.md); resolution flow:
[ARCHITECTURE.md](ARCHITECTURE.md#option-resolution-flow).

### Checklist

| Step | Where | Applies to |
|------|-------|------------|
| Field in the relevant `model.*Options` struct | `internal/model/types.go` | All |
| Default in `config.Defaults` | `internal/config/config.go` | Config-backed |
| Option entry with `AffectsSession` and, if needed, `AffectsNetwork` (below); then `make generate` | `internal/config/options_def.go` | All |
| Guardrail fixture values | `internal/config/merge_test.go`, `internal/model/option_sources_test.go` | Config-backed, new sources |
| Focused parsing and runtime tests, then `go test ./...` | | All |
| Docs for user-visible behavior | `docs/cli-reference.md`, `docs/configuration.md`, `docs/ARCHITECTURE.md`, command docs | All |
| Security notes; review validation and whether the opt-in should be CLI-only | `docs/security/README.md` | Security-sensitive |
| Key in `removedConfigFields`, so `readDefaults()` rejects it loudly | `internal/config/config.go` | Removed keys |

- `AffectsSession` is required. `SessionAffected` when a new value makes a
  fresh session differ from a running one (image, features, mounts, network,
  env, auth, backend); the value is recorded as provenance in
  `enclave ps --json`, so it must not carry secrets. `SessionUnaffected` for
  host-side, UX, or one-shot options.
- `AffectsNetwork` when the option changes the session allowlist, so
  `network status|print|diff` accept its flags; a test fails otherwise.

Opted-in verified worktrees use the main checkout's project layer. Pass the
resolved `model.Project` through the command context: use `ConfigHash` for
user-authored inputs and project memory, and `Hash` for runtime ownership. The
filesystem verifier in `internal/gitworktree` is shared by app project
resolution and mounts; the gateway-facing `config` package does not import it.
See [Linked worktrees](configuration.md#linked-worktrees).

The source selector is a session-affecting bootstrap boolean. Read only that
key from the checkout-local file, then from the verified anchor's file, before
selecting a layer, so ignored obsolete or invalid options cannot block
inheritance. Preserve the deciding project-level value for normal source
reporting. Actions that build on the selected source belong in
`actionRequiresConfigSource` so they fail instead of using a fallback layer.
Precedence and supported scopes are documented in
[Linked worktrees](configuration.md#linked-worktrees); JSON fields are defined
in [Integration](integration.md#config---json).

### Option kinds

| Kind | How to define it |
|------|------------------|
| CLI-only (for example `--force-base-image`, `--no-rebuild`) | Omit `DefaultsField` and use `Apply: ApplyNone` |
| Config-only (for example `host_config_paths`) | Omit `CLIFlags` |
| Host-wide only | Add the key to `stripGlobalOnlyKeys` in `internal/config/config.go`; project config can otherwise set every config-backed option |
| Opt-in no config file may grant (like `--allow-sensitive-mounts`) | No config key at all |
| List | `ApplyListExtras` or a selection kind; a key function in `listEntryKey` and, if entries have a format, a validator in `listEntryValidators` (`internal/config/list_entries.go`); the flag uses `appendListFlag` |
| String map (`OptionKindStringMap`, `ApplyStringMap`) | [String map options](ARCHITECTURE.md#string-map-options) |

`ENCLAVE_NO_REBUILD=1`, the terminal-scoped form of `--no-rebuild`, is applied
in `app.Run` after user session commands are rewritten and before defaults
resolution, reusing the `NoRebuild` gates and conflict checks instead of a
config key. Options with subsystem rules are in
[Subsystem notes](ARCHITECTURE.md#subsystem-notes).

## Testing extensions

Manual end-to-end checks with the
[working-copy](#run-changes-from-the-working-copy) binary.

### Go code and metadata

```bash
go generate ./cmd/enclave            # Refresh generated tool imports
make build                           # Build the CLI
./bin/enclave validate-extensions    # Validate extension metadata
./bin/enclave --help                 # Verify tool loading works
./bin/enclave --tool claude          # Should recognize claude as a valid tool
```

### Build and run

```bash
./bin/enclave --tool claude --rebuild   # claude image with all default-enabled features
./bin/enclave --tool codex              # another tool
```

### Features inside the container

Start a session (`./bin/enclave --tool claude`) and check:

| Feature | Default | Check |
|---------|---------|-------|
| `github-cli` | Enabled | `gh --version` |
| `python-dev` | Enabled | `black --version`, `ruff --version`, `pre-commit --version` |
| `node-dev` | Enabled | `tsc --version`, `pnpm --version`; `type nvm` (its `feature-entrypoint.d` script loads nvm without activating a version) |
| `devtools` | Enabled | `vim --version`, `rg --version` |
| `theia-dev` | Opt-in | `pkg-config --modversion x11 xkbfile libsecret-1` |
| `gitlab-cli` | Opt-in | `glab --version` |
| `golang-dev` | Opt-in | `go version`, `dlv version` |

Every image hooks direnv into the shell, independent of features: `type direnv`
should find it.

### Tool-specific entrypoints

Tool `entrypoint.d` scripts run only for the selected tool. Start
`./bin/enclave --tool claude` and `./bin/enclave --tool codex` and check that
each tool's own setup ran.

### Minimal images

| Command | Inside the container |
|---------|----------------------|
| `./bin/enclave --tool claude --slim --rebuild` | `claude --version` works; `gh --version` fails (feature not installed); `vim --version` fails (only `vim-tiny`) |
| `./bin/enclave devcontainer run --rebuild` | `gh --version` fails: devcontainer mode disables features by default |
| `./bin/enclave devcontainer run --features github-cli --rebuild` | Only `github-cli` is installed |

### `spec.yaml` is required

`spec.yaml` (or `spec.json`) is mandatory, and its `kind` and `name` fields must
match the extension's location. To check, temporarily move it out and restore
it afterwards:

```bash
mv extensions/tools/claude/spec.yaml /tmp/
make build
./bin/enclave --tool claude --help   # Should fail: spec.yaml not found
mv /tmp/spec.yaml extensions/tools/claude/
```

### Direct Docker builds

For quick experiments you can build the Dockerfile directly. The raw
Dockerfile falls back to installing the default-included tool set, without the
per-tool layer caching of `enclave --tool <name> --rebuild`:

```bash
# All default-enabled features
docker build -t enclave:latest .

# Specific features only (opt-in ones included)
docker build \
  --build-arg FEATURES="github-cli python-dev" \
  -t enclave:selective .

docker run --rm enclave:selective gh --version     # Works
docker run --rm enclave:selective glab --version   # Fails (not installed)

# Watch feature installation order
docker build --progress=plain . 2>&1 | \
  grep -E "(Installing feature|Feature .+: added)"
```

## Windows launcher

On Windows, `cmd/enclave` builds `main_windows.go`, which forwards to the Linux
binary inside WSL2 ([windows.md](windows.md)). Without assets and extensions
([why](ARCHITECTURE.md#windows-launcher)) it is about 3 MB, against roughly
14 MB for the Linux binary.

### Build and test on Linux

All logic is in `internal/wslshim`, and everything except the two drive-letter
queries is host-independent:

```bash
go test ./internal/wslshim/...
GOOS=windows go build ./... && GOOS=windows go vet ./...
```

- `make lint` runs `go vet` and `gosec` again with `GOOS=windows`, since
  neither checks files their build constraints exclude; otherwise the
  `unsafe.Pointer` code in `drivetype_windows.go` and its `#nosec` annotations
  would go unchecked. `golangci-lint` is not repeated, so Windows-only files
  stay outside it.
- The two shell scripts the launcher sends into the distribution are Go
  constants. `internal/wslshim/script_exec_test.go` executes them against the
  host's `sh` on non-Windows platforms, which keeps `probeScript` and the
  marker `parseProbeOutput` looks for from drifting apart.

### How it finds the Linux binary

`make install` puts `enclave` in `~/.local/bin`, which is only on `PATH` via
`~/.profile`, and a non-login shell does not read that file. So the launcher
resolves the path first and then executes it:

1. `wsl.exe [-d <distro>] --cd / -e /bin/sh -lc '<fixed probe>'` runs a login
   shell that tries `command -v enclave` and then `~/.local/bin/enclave`,
   `/usr/local/bin/enclave`, and `/usr/bin/enclave`.
   - The probe interpolates nothing.
   - It prints its answer behind an `enclave-bin=` marker, so a shell profile
     that writes to standard output is not mistaken for the path.
   - Passing `--cd` here also detects support for it, at no extra cost.
2. `wsl.exe [-d <distro>] --cd <path> -e <absolute path> <your arguments…>`
   runs the binary directly, with no shell to re-parse anything.

This costs one extra round trip, roughly 100–300 ms on a cold distribution. The
result is not cached. On a `wsl.exe` old enough to lack `--cd`, the launcher
retries and changes directory through a shell instead, still passing the
arguments as positional parameters so they are never re-split.

### Argument quoting

Windows passes a single command-line string and `wsl.exe` re-parses it, so the
launcher builds that string itself and hands it to `CreateProcess` unchanged.
This is the riskiest part of the launcher; two layers check it.

1. A golden table in `internal/wslshim/quote_test.go` round-trips every argument
   shape through a reimplementation of `CommandLineToArgvW`. It runs in CI on
   every push. The table is exported to
   `internal/wslshim/testdata/wsl-quoting-golden.json`; regenerate it after
   changing the escaping or the table, and commit it:

   ```bash
   ENCLAVE_UPDATE_GOLDEN=1 go test ./internal/wslshim
   ```

2. `scripts/wsl-shim-verify.ps1` feeds the same command lines to a real
   `wsl.exe` and compares the NUL-separated argv the Linux side receives. It
   cannot run in GitHub-hosted CI, because `windows-latest` runners have no
   WSL2. Run it manually on a Windows host with WSL2 before publishing Windows
   artifacts:

   ```powershell
   pwsh -File scripts/wsl-shim-verify.ps1
   ```

   Without PowerShell 7, use Windows PowerShell. A script reached through
   `\\wsl.localhost` counts as remote, so the execution policy refuses it
   unless the run bypasses it:

   ```powershell
   powershell -ExecutionPolicy Bypass -File .\scripts\wsl-shim-verify.ps1
   ```

## Packaging

| Environment | Setup |
|-------------|-------|
| Ubuntu host | `sudo apt install debhelper rpm` |
| Fedora host | `sudo dnf install git go make rpm-build` |
| Inside Enclave | Add the `deb-dev` and `rpm-dev` features from [enclave-extensions](https://github.com/eclipse-enclave/enclave-extensions) (`enclave features add eclipse-enclave/enclave-extensions --name deb-dev`, same for `rpm-dev`) and run with `--features +golang-dev,+deb-dev,+rpm-dev` |

- `make deb-quick` skips dpkg's build dependency check, for a Go installed
  outside APT; the package is the same.
- Package builds need network access: `go mod vendor` fetches modules, and a
  distribution Go older than the `go.mod` `toolchain` downloads it.
- The `go` directive fixes the language version; `go vet` in `make lint`
  rejects newer standard-library APIs, and CI tests the minimum Go without
  toolchain switching.

## Dependency updates

```bash
make check-versions
```

This reports pinned versions, lockstep consistency, available updates, and the
Debian and Ubuntu versions that cap the minimum supported Go and git.

- The script (`scripts/check-versions.sh`) only reads. `make lint` runs its
  `--offline` mode, which fails when a lockstep group disagrees or a new pin is
  not in its inventory.
- The `dependency-updates` agent skill
  (`.agents/skills/dependency-updates/SKILL.md`) turns the output into a
  prioritized plan and describes how to apply each update group.
- Direct Go module dependencies are listed in
  [DEPENDENCIES.md](DEPENDENCIES.md).

## Docker weaving scripts

Build composition scripts live in `runtime-assets/build-scripts/` and are
invoked from `Dockerfile`.

- Keep `Dockerfile` structural; move feature and tool composition logic into
  these scripts.
- Script contracts are documented in `runtime-assets/build-scripts/README.md`.
- Validate script changes with `make lint`, which runs `shellcheck` on all
  `*.sh` files and the `build-scripts/bin/` helpers.

## Extensions

Every tool or feature extension must include a `README.md` in its directory.
See [extensions/README.md](extensions/README.md) for the extension architecture
and [Testing extensions](#testing-extensions) for manual checks.

## Releases

PRs do not edit `CHANGELOG.md` (except the release-prep PR); they describe
user-visible changes in the `changelog` block of the PR description, as the
[PR checklist](../CONTRIBUTING.md#pull-request-checklist) explains.
Versioning, tagging, publishing, and release acceptance testing are in
[RELEASING.md](RELEASING.md).

## Docs and diagrams

- Keep docs concise and aimed at experienced developers.
- Diagrams are Mermaid code blocks embedded in the docs; update them together
  with the behavior they document.

## Commits

Use Conventional Commits: `type: short imperative summary`.

Allowed types: `feat`, `fix`, `docs`, `refactor`, `test`, `chore`, `build`,
`ci`, `perf`, `revert`.

Work is tracked in GitHub Issues.
