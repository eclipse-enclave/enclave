---
name: dependency-updates
description: Check Enclave's pinned dependencies, toolchains, images and GitHub Actions for updates and produce a prioritized update plan that respects the LTS floors. Use when asked what needs updating, to refresh pins, or to apply an update group.
---

# Dependency Updates

`scripts/check-versions.sh` gathers every fact deterministically. This skill
turns its output into a plan and, on request, applies one group. Do not
replace the script's lookups with your own; if the script is wrong or
incomplete, fix the script.

## 1. Run the check

```bash
make check-versions        # or: scripts/check-versions.sh
```

It reads the network and never modifies files. `--offline` checks only the
lockstep groups and uncovered pins (also part of `make lint`). Exit status 1
means at least one `PROBLEM` line.

## 2. Report

Turn the output into a report, in this order, one line per item with
current → target and the files to touch:

1. **Security**: `PROBLEM` lines from the Security section and an
   unsupported Go toolchain.
2. **Inconsistencies**: other `PROBLEM` lines (lockstep groups, actions on
   several SHAs or SHAs that do not match their tag, uncovered pins, floors
   above the ceiling, failed lookups).
3. **Routine**: `update` lines for patch and minor versions and digests.
4. **Major**: `update` lines marked `MAJOR` or `major change`. Read the
   upstream release notes between the two versions and summarize what
   affects this repository (runner or Node requirements, removed inputs,
   changed defaults). Check the workflows that use the action.
5. **Held back**: `held back` lines and `not called` vulnerabilities with
   the reason the script gives. If a held-back module has a vulnerability,
   say that fixing it needs a higher Go floor and that this is the
   maintainer's decision.
6. **Ceilings**: if a floor may be raised, say so; never propose a floor
   above the ceiling. For a Docker or buildx ceiling change, check the
   Dockerfile instructions in `Dockerfile`, `Dockerfile.gateway`, and
   `internal/app/dockerfile_gen.go` against it.

## 3. Apply (only when asked)

One group per change, on a branch. Use the targets, SHAs, and digests from
the script output. Then rerun the script until the group reports `ok`, and
run `make build`, `make test`, and `make lint`. Do not commit or push unless
asked.

| Group | Edit |
|---|---|
| Go toolchain | `go.mod` `toolchain`; `Dockerfile.gateway` `golang:<version>-alpine<alpine>@<digest>`. |
| Go floor | Every location the script lists for "Go floor", including the `golang-<minor>-go` alternative and `/usr/lib/go-<minor>/bin` fallback in `debian/`. Only after the ceiling allows it. |
| Go modules | `go get <module>@<version>` then `go mod tidy`; confirm the `go` directive did not change. |
| Devtools | `extensions/features/devtools/install.sh` and the versions in its `README.md`. New gosec or golangci-lint findings must be fixed or justified with `#nosec`/`nolint` and a reason; never weaken the rule set. |
| Container images | The `ARG`/`FROM` lines in `Dockerfile` and `Dockerfile.gateway`. Alpine also: `internal/model/types.go` `AlpineImage` and `runtime-assets/microvm/alpine/build-bundle.sh`, same digest everywhere; check that the packages installed by the gateway and the QEMU bundle exist on the new branch. |
| GitHub Actions | Every `uses:` of the action, `@<sha> # <tag>`. |
| yq | `Dockerfile` `YQ_VERSION`; check that `yq_linux_amd64` and `yq_linux_arm64` are still release assets. |
| Fedora CI | `container:` in `.github/workflows/reusable-rpm-build.yml`. |

### License check (Go modules)

A changed `go.sum` must pass Eclipse Dash before the update is done. Run it
on `go.sum` as the `DEPENDENCIES` header describes. Dash needs Java 11+ and
the Dash version named there; the jar is at
`https://repo.eclipse.org/content/repositories/dash-licenses-releases/org/eclipse/dash/org.eclipse.dash.licenses/<version>/org.eclipse.dash.licenses-<version>.jar`.
Check it against the `.sha1` file next to it.

- Every Go entry must be `approved`. If a new version is not, go back to
  the newest approved version of that module. Report the newer version as
  held back pending IP review.
- Never pass `-review`; requesting a review is the maintainer's decision.
- Then regenerate `DEPENDENCIES` as its header describes, including the
  website section and the date.
- If Java or Dash is unavailable, report the license check as not done.
  Never edit `DEPENDENCIES` by hand.

## Rules the script encodes

Keep these in mind when interpreting results or changing the script.

- **Support set**: Debian stable and every Ubuntu LTS release in standard
  support (`DEBIAN_SUITES`, `UBUNTU_SUITES`). Release, `-updates`, and
  `-security` count; `-backports` does not. Update the suite lists when a
  release enters or leaves standard support.
- **Floors** (`go` directive and packaging Go version, git, Dockerfile
  features) may not exceed the lowest version the support set ships. An
  upstream EOL is not a reason to raise one.
- **Go toolchain** is the latest patch of the oldest supported Go minor:
  the release the go command switches to when it builds devtools that need
  a newer Go. A newer project minor breaks `make lint`, because analysis
  tools built with an older Go cannot load a newer standard library.
- **Go modules** may not raise the `go` directive. **Devtools** may not
  require a Go newer than the toolchain.
- **Lockstep groups** move together. **One SHA per action**, and it must be
  the commit of the tag in the comment.
- **New pins**: when you add a pinned version anywhere, add its location to
  `KNOWN_PINS` in the script and a check for it, or the uncovered-pin check
  fails.

## Not checked

- **Deferred**: the website (`website/`, `.github/workflows/website-*.yml`).
- **Deliberately unpinned**, installed at their latest version on every
  build; never pin or bump them: claude, mistral-vibe, uv and oh-my-zsh
  (curl installers); codex, pi, opencode, playwright MCP and the node-dev
  global packages (npm); glab and nvm (latest-release lookup); gh and the
  Debian packages in the images (distribution repositories).
