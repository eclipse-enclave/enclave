# Developer Guide

Concise notes for contributors working on the enclave codebase.

## Requirements

- Go 1.24 or newer. Releases and most CI jobs build with the `toolchain`
  version in `go.mod`, which the `go` command downloads when the local Go is
  older. A separate CI job builds and tests with the exact `go` directive
  version and `GOTOOLCHAIN=local`.
- Docker daemon running (for runtime testing)
- Linux or macOS host. Native Windows is unsupported; use WSL2, where the Linux
  instructions apply. `make cross-build` compiles every published cross-target:
  `darwin/arm64`, `darwin/amd64`, `linux/arm64`, `windows/amd64`, and
  `windows/arm64`. It builds `./...` rather than just `./cmd/enclave`, so the
  windows targets also keep the whole tree portable even though the published
  windows binary is only the WSL launcher.

## Common Commands

Build the CLI (plain `make` is equivalent to `make build`):

```bash
make build
```

Install to your PATH:

```bash
make install
```

Build distribution packages:

```bash
make deb
make rpm
```

The default Enclave image includes both packaging toolchains. On Ubuntu, install
them with `sudo apt install debhelper rpm`. On Fedora, install the
RPM build dependencies with `sudo dnf install git go make rpm-build`. Use
`make deb-quick` when Go was installed outside APT and therefore does not
satisfy dpkg's build dependency check; it builds the same binary package
without that check.

Package builds need network access: `go mod vendor` fetches the modules, and a
distribution Go older than the `toolchain` in `go.mod` downloads that
toolchain and builds with it. The `go` directive fixes the language version,
`go vet` in `make lint` rejects standard-library APIs newer than it, and CI
builds and tests with the minimum Go version without toolchain switching.

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
uses its own atomically published directory. The tree is reproducible from the
binary, so deleting it is safe; the next run extracts it again. A binary
refreshes its entry's mtime at most once a day, under the entry's lock. Whenever
it extracts or refreshes its entry, it also removes other entries no binary has
used for 30 days, skipping any whose lock is held (`removeUnusedAppAssets` in
`internal/config/embedded_assets.go`). A refresh that fails, as in a cache this
user can read but not write, still leaves the valid entry in use.

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

### Env-only credential acceptance testing

Unit tests cover credential resolution, placeholder shapes, host release rules,
and auth-file reconciliation without a real engine or account. In a dedicated
Docker/Podman environment, test each variable in
[Env-only bearer tokens](auth.md#env-only-bearer-tokens) independently:

- Make an authenticated tool request, then restart and repeat. Confirm the
  container gets a fresh placeholder and no placeholder reaches shared auth
  through reconciliation.
- Test `codex exec` with only `OPENAI_API_KEY`, then only `CODEX_API_KEY`;
  neither should require onboarding. Check API-key precedence when a ChatGPT
  login already exists.
- Test the Codex TUI separately: an empty store requires onboarding and a stored
  login wins over the env API key. Accept the prefilled placeholder for one
  session, then restart with an empty shared store and confirm the old local
  `auth.json` is removed. Repeat with project auth scope. With valid shared auth,
  confirm startup restores its symlink instead. Verify persistent TUI login
  with `--no-api-key`.
- With `codex exec`, test API-key model-list refresh under `--network-log=requests` and
  coarse logging with a PAT also injected. Expect a denial at `chatgpt.com` and
  built-in model-list fallback; OpenAI API requests should still work.
- Confirm `CODEX_ACCESS_TOKEN` PATs work and Agent Identity JWTs fail clearly
  with gateway protection active.
- Write a placeholder into a local auth file, then exercise foreground exit,
  background stop/remove, and `exec` reconciliation. Expect a visible warning,
  no contaminated copy, and existing shared credentials unchanged.

Use fake values for the Anthropic tokens. Upstream's error message tells a
released `sk-ant-oat01-` value apart from a placeholder, and a real
subscription token in a third-party client is subject to Anthropic's terms.

Record tool versions with the results. In particular, Claude Code is closed
source: its env-only setup-token behavior needs runtime verification, not just
changelog evidence. Keep test credentials out of logs and review artifacts.

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
  - `cmd/enclave/tool_imports.go` (tool extension Go imports, `//go:build !windows`)

## Dependency Updates

Report pinned versions, lockstep consistency, available updates, and the
Debian and Ubuntu versions that cap the minimum supported Go and git:

```bash
make check-versions
```

The script (`scripts/check-versions.sh`) only reads; `make lint` runs its
`--offline` mode, which fails when a lockstep group disagrees or a new pin is
not in its inventory. The `dependency-updates` agent skill
(`.agents/skills/dependency-updates/SKILL.md`) turns the output into a
prioritized plan and describes how to apply each update group.

## Windows Launcher

On Windows, `cmd/enclave` builds `main_windows.go`, a launcher that forwards to
the Linux binary inside WSL2 (see [windows.md](windows.md) for the user-facing
behavior). It links neither the embedded runtime assets nor the tool extensions,
because it never builds an image — which is why `cmd/enclave/tool_imports.go` is
generated with a `//go:build !windows` constraint. The result is around 3 MB
against roughly 14 MB for the Linux binary.

All the logic lives in `internal/wslshim`, and everything except the two
drive-letter queries (`GetDriveType` and `WNetGetConnection`) is
host-independent, so it builds and is tested on Linux:

```bash
go test ./internal/wslshim/...
GOOS=windows go build ./... && GOOS=windows go vet ./...
```

`make lint` runs `go vet` and `gosec` a second time with `GOOS=windows`, because
neither one looks at a file its build constraints exclude — without that pass the
`unsafe.Pointer` code in `drivetype_windows.go` and its `#nosec` annotations are
checked by nothing. `golangci-lint` is not repeated that way, so the
Windows-only files remain outside it.

The two shell scripts the launcher sends into the distribution are Go constants,
so `internal/wslshim/script_exec_test.go` executes them against the host's `sh`
on non-Windows platforms. That is what keeps `probeScript` and the marker
`parseProbeOutput` looks for from drifting apart.

Argument quoting is the risky part. Windows passes a single command-line string
and `wsl.exe` re-parses it, so the launcher builds that string itself and hands it
to `CreateProcess` unchanged. Two layers check it:

1. A golden table in `internal/wslshim/quote_test.go` round-trips every argument
   shape through a reimplementation of `CommandLineToArgvW`. This runs in CI on
   every push. The same table is exported to
   `internal/wslshim/testdata/wsl-quoting-golden.json`; regenerate it after
   changing the escaping or the table, and commit it:

   ```bash
   ENCLAVE_UPDATE_GOLDEN=1 go test ./internal/wslshim
   ```

2. `scripts/wsl-shim-verify.ps1` feeds those same command lines to a real
   `wsl.exe` and compares the NUL-separated argv the Linux side receives. This
   **cannot** run in GitHub-hosted CI, because `windows-latest` runners have no
   WSL2. Run it manually on a Windows host with WSL2 before publishing Windows
   artifacts:

   ```powershell
   pwsh -File scripts/wsl-shim-verify.ps1
   ```

   Without PowerShell 7, use Windows PowerShell. A script reached through
   `\\wsl.localhost` counts as remote, so the execution policy refuses it unless
   the run bypasses it:

   ```powershell
   powershell -ExecutionPolicy Bypass -File .\scripts\wsl-shim-verify.ps1
   ```

## Adding or Updating Options

1) Add fields in `model.Options` (and related embedded struct).
2) Add defaults in `config.Defaults` if configurable.
3) Add the option entry in `internal/config/options_def.go`, including the
   required `AffectsSession` decision and, if the option changes the session
   allowlist, `AffectsNetwork` (see the checklist below).
4) Run code generation (`make generate`).
5) Run tests (`go test ./...`).
6) Update docs for user-visible flag behavior (`docs/cli-reference.md`,
   `docs/configuration.md`, `docs/ARCHITECTURE.md`, and related command docs).

For options not configurable via config files, omit `DefaultsField`
and use `Apply: ApplyNone` in `options_def.go` (for example:
`--force-base-image` and `--no-rebuild`).

`--no-rebuild` also has the terminal-scoped `ENCLAVE_NO_REBUILD=1` switch.
`app.Run` applies it after rewriting user-defined session commands and before
defaults resolution. It reuses the existing `NoRebuild` gates and conflict
checks rather than adding a config key; see the [CLI reference](cli-reference.md#image--build).

A new list option uses `ApplyListExtras` (or a selection kind) and needs a
key function in `listEntryKey` and, if entries have a format, a validator in
`listEntryValidators` (`internal/config/list_entries.go`); its flag uses
`appendListFlag`.

For config-only options, omit `CLIFlags` instead (for example:
`host_config_paths`). Project config can set every config-backed option; an
option that only makes sense host-wide belongs in `stripGlobalOnlyKeys` in
`internal/config/config.go`, and an opt-in no config file may grant gets no
config key at all (like `--allow-sensitive-mounts`).

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
  add the definition in `internal/config/options_def.go`, including the
  required `AffectsSession` decision: `SessionAffected` if changing the value
  makes a fresh session differ from a running one (image, features, mounts,
  network, env, auth, backend), so it is recorded as session provenance in
  `enclave ps --json`; `SessionUnaffected` for host-side, UX, or one-shot
  options. Affected values must not carry secrets. Set `AffectsNetwork` when
  the option changes the session allowlist, so `network status|print|diff`
  accept its flags; a test fails when an unmarked option changes the preview.
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
  update `docs/security/README.md` and review whether validation logic needs
  changes, or whether the opt-in should be CLI-only.

Quick rule of thumb:
- CLI-only option: model field, `options_def.go`, generate, tests, docs.
- Config-backed option: model field, `config.Defaults`, `options_def.go`,
  generate, merge/source test fixtures, tests, docs.
- User-facing runtime option: all of the above plus security notes when
  applicable.

Backend option note: `--backend` is config-backed and defaults to `auto`, which `app.resolveBackend` turns into `docker` or `podman` from `backenddocker.DetectCLIs()` (prompting once via `prompt.Choose` and persisting with `config.WriteGlobalDefault` when both are installed and a terminal is present); engine-using management commands register the same option by name so the CLI override takes precedence without taking every Run-group flag. `podman` reuses the Docker backend (`internal/backend/docker`) over the podman CLI by switching the binary in `internal/docker` once at startup (`docker.SetBinary`), with `docker.IsPodman()` gating the few engine differences (`--userns=keep-id`, `info` schema, build cache flags, and `renderEngineDockerfile` stripping home-directory cache mounts from the rendered Dockerfile); experimental `qemu` is available for foreground slim/no-feature unrestricted sessions.

Shared skills note: `--skills-validation strict|agent` is config-backed,
defaults to `strict`, and supports global, project, and selected tool overrides.
Normalize casing and whitespace before validating; runtime callers resolve the
mode through `model.SkillsValidationMode`, which falls back to strict for empty
or unknown values. Apply the mode in both skill composition paths. It affects
shared skills only; source precedence and whole-directory replacement remain
unchanged.

Build option note: `--features` is available on CLI. In devcontainer mode,
unset features default to none, so pass `--features` explicitly when needed.
`--features none` is the explicit "no features" selection.

Root guard note: `--allow-root` is CLI-only and has no config key, so no config
file can grant it; `ENCLAVE_ALLOW_ROOT=1` is its only alternative.
`checkRootGuard` in `internal/app/root_guard.go` runs early in `app.Run`,
before any state is written; tests pin the root check through the
`runningAsRoot` seam. The opt-in only skips the check: images cannot be built
for UID 0 yet. Code that runs before the guard (user command discovery,
parsing, help, version, and shell completion) must not write, not even a file
it removes again: it uses `config.ResolveHostHomeReadOnly` and
`config.ResolvePathsReadOnly`, which check home writability with access(2)
instead of a probe file, so they pick the same home, and never extract the
embedded assets. The tests catch such writes by backdating the
directory mtimes under a temporary HOME (`internal/app/root_guard_test.go`, and
`cmd/enclave/main_test.go`, the only tests that run with the real embedded
assets).

Mount guard note: `--allow-sensitive-mounts` is CLI-only and has no config key;
`ENCLAVE_ALLOW_SENSITIVE_MOUNTS=1` is its only alternative.
`resolveAdditionalMounts` in `internal/app/command_run.go` validates the
additional directories, including those of `--add-sensitive-dir` and
`--add-readonly-sensitive-dir`, and collects their sensitive-dir exceptions.
For run actions only, it then normalizes the configured `sensitive_dir_exceptions`
entries (`configSensitiveDirExceptions`) and runs `checkSensitiveMounts`, both
in `internal/app/mount_guard.go`, before the image build. The rule lives in
`mounts.SensitivePaths` (`internal/mounts/sensitive.go`), and
`SensitivePaths.Assess` weighs a mount against the exceptions. Callers pass
`config.HostAppDataDirs`, the platform's per-user application data
directories, and `config.HostEnclaveRoots`, enclave's own roots, separately so
`mounts` can tag the roots (and the SSH agent socket) hard-protected without
depending on `config`. Hard-protected locations are scanned in full, since
`Reason` stops at the first match. A refusal names the flag or config file
each additional directory came from. Devcontainer bind sources are not checked
separately: they must resolve inside the project, and every subdirectory of a
directory that is not sensitive is not sensitive either
(`TestSensitivePathsSubdirsOfSafeDirsAreSafe`). External linked-worktree
metadata is checked in `mounts.AddWorktree` regardless of the opt-in, because
the `.git` pointer comes from the repository; it uses `Reason`, and exceptions
must never reach it. Tests that touch the rule clear
`SSH_AUTH_SOCK` in their `TestMain`; the `app` and `runtime` tests, which take
the directories from the environment, clear `XDG_*` as well, while `mounts`
tests pass them explicitly.

Self-update notes (user-facing behavior: [CLI reference](cli-reference.md#commands)):

- Read `auto_update` and `update_channel` from `globalDefaults`, not resolved
  project or tool options. Explicit `self-update` uses `config.LoadGlobalDefaults`.
- Keep all updater output on stderr, including verbose diagnostics: use
  `logx.Noticef`, `DebugNoticef`, or `Warnf`, never `Infof` or `Debugf`.
- Both paths hold `config.HostSelfUpdateLockPath`: the periodic check skips a
  busy lock, while explicit `self-update` waits.
- Tests replace `newSelfUpdater` in `internal/app` and use an `httptest` release
  in `internal/selfupdate`. To try a real update, build outside the checkout
  with `make build BUILD_CHANNEL=rolling BIN_DIR=/tmp/enclave-rolling`, then
  run `/tmp/enclave-rolling/enclave self-update` with `ENCLAVE_HOME` unset.
- Both channel sources read a `latest` file on download.eclipse.org and the
  `checksums.txt` beside the artifacts it names; see [Releases](#releases)
  for the layout. Release CI stamps `BUILD_CHANNEL=stable` or `rolling`.

List option note: list options are folded layer by layer in
`internal/config/options_lists.go`, with each tool-override file as its own
layer and the CLI last. A bare `ApplyListExtras` list replaces, `+x`/`-x`
amend and `[]`/`none` clear, except that `--add-dir` and `--add-readonly-dir`
add a bare path (`cliAddsPlainEntries`). The selection kinds
(`features`, `host_config_paths`) replace with a bare list and amend with
`+`/`-`, the more specific layer winning an item. Feature directives apply to
the implicit default-enabled set only when no layer selects features. The
generator emits no `ApplyDefaultsWithSource` for list kinds. See
[List Options](configuration.md#list-options).

`host_config_paths` resolves against the selected tool profile's reviewed
`passthroughPaths` instead of global defaults. Use `default` to include the
built-in allow-list before applying `+`/`-` edits.

## Option Source Precedence

When a value exists in multiple layers, enclave resolves in this order:

1. CLI flags
2. Selected tool override (`tool_overrides.<tool>`; the project file's wins over the global file's)
3. Project config (`~/.config/enclave/projects/<hash>/config.json`; per-project overrides live outside the worktree, keyed by project hash)
4. Global config (`~/.config/enclave/config.json`)
5. Built-in defaults

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

## Releases

There is one version source: the latest release tag, read by
`scripts/version.sh`. `make build` stamps `enclave version` and the image label
with it (`1.0.0` on the tag `v1.0.0`, otherwise `git describe` output such as
`1.0.0-3-gabc1234`), and `make deb`/`make rpm` use `scripts/version.sh --package`
(`1.0.0`, or a snapshot such as `1.0.1~~git.20260421.57.abcdef12` that sorts
above `1.0.0` and below `1.0.1` and all of its pre-releases). Nothing is bumped in a commit; the Debian
changelog entry is written into the build copy. Until the first release tag
exists, versions build on a `0.1.1` baseline, one patch above the `0.1.0` the
packages carried before, so rolling packages keep upgrading. Tag names reach the shell through `make`,
so the script only emits version characters and fails the build on anything
else.

To cut a release, push an annotated tag `vX.Y.Z`, or `vX.Y.Z-alpha.N`,
`-beta.N`, `-rc.N` for a pre-release, on `main`. Only these forms are
accepted: they are the ones the updater, dpkg, and rpm all order the same
way, so a pre-release, the snapshots after it, and the final release upgrade
into each other in that order. The first release tag must be `v0.1.2` or
higher, pre-releases such as `v0.1.2-rc.1` included: the packages published
before it are `0.1.2~~git…` snapshots, and apt and dnf do not upgrade them to
a `0.1.1` or `0.1.0`.

```bash
git tag -a v1.0.0 -m "Enclave 1.0.0"
git push origin v1.0.0
```

`release.yml` builds the same assets as the rolling workflow, verifies that the
binary and packages report the tag's version (the triggering tag wins when a
commit carries several release tags), creates the GitHub release for the tag,
and publishes the files on download.eclipse.org. A pre-release tag produces
a GitHub pre-release and a versioned directory, but does not move
`releases/latest`. Releases run one at a time with later
tags queued behind, and `releases/latest` only moves to a version that sorts
higher, so retrying an old release cannot pull the stable channel back.
Published release files are immutable on both hosts: retries leave identical
files untouched, add missing files after a partial failure, and fail before
adding anything if an existing file differs (including `checksums.txt`).
Rebuilding the same commit may produce different bytes; retry only the failed
publish job with its original artifacts, or publish a new version instead.
The assembled `release-assets` workflow artifact is retained for 30 days for
tagged releases (including pre-releases), as long as GitHub allows re-running
a job, and one day for rolling builds.
GitHub retries verify all completed assets before deleting empty `starter`
entries left by failed uploads and filling gaps; other unfinished states fail
without deleting anything. After uploading, both channels require every
expected GitHub asset to be `uploaded` and to match the workflow's SHA-256
digest, downloading and hashing the asset when GitHub provides no digest.
Rolling releases still replace their assets on every publication.
Tags are mutable refs, so both the GitHub release notes
and `releases/<version>/commit` record the source commit: a re-run from that
commit must pass the file checks, and a tag moved to another commit is refused. A
ruleset protecting `refs/tags/v*` from deletion and force pushes is the
server-side complement.

Layout on `https://download.eclipse.org/enclave/`, which the updater and the
download links depend on:

| Path | Contents |
|------|----------|
| `releases/<version>/` | All assets of one release plus `checksums.txt` and `commit`; versions are plain (`1.0.0`, `1.1.0-rc.1`) |
| `releases/latest` | One line naming the highest final release, written after its directory is complete |
| `rolling/` | The assets of the current `main` build, replaced as a whole on every push; only `main` publishes here |
| `rolling/latest` | One line with the full commit SHA of that build, published together with its assets |

Uploads go through `.github/actions/publish-downloads`, which follows the
[CBI procedure](https://eclipse.dev/cbi/best-practices/github-actions/upload-to-downloads.eclipse.org/):
the `SCP_USERNAME`, `SCP_KEY`, and `SCP_PASSPHRASE` secrets are provisioned by
Otterdog, the server's host key is pinned in the action (update `known-hosts`
when the Eclipse Foundation rotates it), and each directory is uploaded next
to its target. Before publication, remote SHA-256 verification checks every
staged file against the workflow's bytes, including `checksums.txt` itself;
a mismatch or missing file fails before publishing the staged files or
advancing `latest`, and a failed run removes its staging directory. New directories and rolling builds are published as a whole
with `mv`; retries of existing releases validate the staged files, then add
only missing files without replacing existing ones.
After publishing, the action reads `checksums.txt` and the pointer back over
`https://download.eclipse.org` and warns when they do not match within two
minutes. This HTTP propagation check is separate from the mandatory remote
integrity check. The GitHub release keeps a copy of the assets; the Scoop manifest
points at it.

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
