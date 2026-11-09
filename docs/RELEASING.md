# Releasing

How maintainers version, tag, and publish Enclave, plus manual acceptance
tests. Contributors only need the
[changelog block](../CONTRIBUTING.md#pull-request-checklist).

## Cut a release

1. Merge the release-prep PR (see [Changelog](#changelog)).
2. Push an annotated tag on `main`:

   ```bash
   git tag -a v1.0.0 -m "Enclave 1.0.0"
   git push origin v1.0.0
   ```

3. Review the draft release that Prepare Release creates. Edit the notes if
   needed, but keep the provenance marker (an HTML comment at the end).
4. Dispatch Promote Release from `main` with the tag (for example `v1.2.3`)
   within 30 days ([Artifacts and retries](#artifacts-and-retries)).

## Versions

There is one version source: the latest release tag, read by
`scripts/version.sh`. Nothing is bumped in a commit.

| Consumer | Version form | Example |
|----------|--------------|---------|
| `make build`: `enclave version` and the image label | Tag, or `git describe` output | `1.0.0` on `v1.0.0`, otherwise `1.0.0-3-gabc1234` |
| `make deb` / `make rpm` (`scripts/version.sh --package`) | Tag, or a snapshot | `1.0.0`, or `1.0.1~~git.20260421.57.abcdef12` |

- A snapshot sorts above `1.0.0` and below `1.0.1` and its pre-releases. The
  Debian changelog entry is written into the build copy.
- Before the first release tag, versions build on a `0.1.1` baseline (one
  above the old `0.1.0` packages), so rolling packages keep upgrading.
- Tag names reach the shell through `make`, so the script emits only version
  characters and fails on anything else.

## Tags

Only these tag forms are accepted, all on `main`:

| Form | Meaning |
|------|---------|
| `vX.Y.Z` | Final release |
| `vX.Y.Z-alpha.N`, `vX.Y.Z-beta.N`, `vX.Y.Z-rc.N` | Pre-release |

The updater, dpkg, and rpm order these forms identically, so a pre-release,
its later snapshots, and the final release upgrade in that order.

- The first release tag must be `v0.1.2` or higher, pre-releases such as
  `v0.1.2-rc.1` included. The packages published before it are `0.1.2~~git…`
  snapshots, and apt and dnf do not upgrade them to a `0.1.1` or `0.1.0`.
- Final tags must be annotated too. `git describe` prefers annotated tags, so a
  lightweight `v0.3.0` on the commit of an annotated `v0.3.0-rc.1` makes later
  snapshots `0.3.0~rc.1…`. Those sort below `0.3.0`, and apt and dnf stop
  upgrading.

## Changelog

The release-prep PR, together with any last fixes, collects the `changelog`
blocks of the PRs merged since the last tag into `## X.Y.Z - YYYY-MM-DD` at the
top of `CHANGELOG.md`, so the tagged source describes its release:

- Each item becomes a bullet ending with a link to its PR, grouped under
  `### Added`, `### Changed`, `### Fixed`, and `### Breaking changes` (for
  `Breaking:` items) as needed.
- Notes may be merged, reworded, or dropped, for example when a later PR
  changes the same behavior or a PR was reverted.

## Release workflows

Prepare Release (`release.yml`, on the tag push) and Promote Release
(`promote-release.yml`, dispatched from `main`) share a concurrency group.

| Step | What it does |
|------|--------------|
| Prepare | Builds the assets once; verifies that binary and packages report the tag's version (the triggering tag wins when a commit has several); attaches them to a draft release whose notes end with a provenance marker (source commit, run ID, SHA-256 of `checksums.txt`) |
| Promote | In the `release` environment: checks the marker against the exact tag and build run, verifies marker and draft files against that run's `release-assets` artifact, then publishes `releases/<version>/` on download.eclipse.org, moves `releases/latest` (only for a higher final release), and publishes the GitHub release last (its Latest marker also only moves up) |

A pre-release gets a GitHub pre-release and a versioned directory but never
moves `releases/latest`. GitHub asset hashes are rechecked right before
publication; assets can still change afterwards. `rolling.yml` publishes every
push to `main`; it and `release.yml` build through
`reusable-release-assets.yml`.

## Artifacts and retries

Nothing is rebuilt after the tag build, because rebuilding the same commit may
produce different bytes. Tag builds disable `setup-go` cache restoration and
saving; rolling and ordinary CI builds keep caching.

| Artifact | Retention |
|----------|-----------|
| `release-assets` of a tagged release | 30 days |
| `release-assets` of a rolling build | 1 day |

Promotion and retries require the original artifact; without it, publication
fails rather than trust the editable draft, and a new version replaces the
build.

Published release files are immutable on both hosts:

- Retries leave identical files untouched, add missing ones, and fail before
  adding anything if an existing file (including `checksums.txt`) differs.
- A failed promotion may already have published `releases/<version>/` and
  moved `releases/latest` while the GitHub release is still a draft. Running
  Promote Release again finishes it.
- Retrying an older release cannot pull the stable channel back.
- GitHub retries verify all completed assets before deleting empty `starter`
  entries left by failed uploads and filling gaps. Other unfinished states fail
  without deleting anything.
- After uploading, every expected GitHub asset must be `uploaded` and match
  the workflow's SHA-256 (downloaded and hashed when GitHub gives no digest).
- Rolling releases replace their assets on every publication.

Tags are mutable, so the provenance marker and `releases/<version>/commit`
record the source commit and a moved tag is refused; a ruleset protecting
`refs/tags/v*` from deletion and force pushes complements this server-side.

## Deployment prerequisites

The workflow YAML does not establish these. Provision them in Otterdog and on
the download server, and verify the effective configuration, before trusting a
dispatch or approval as a production sign-off.

- The `release` environment must have required reviewers, prevent self-review,
  and allow deployments only from `main`. Referencing a nonexistent environment
  creates an unprotected environment, not an approval gate.
- Stable credentials are scoped to that environment, with equivalent
  organization- or repository-scoped access removed. Rolling uses a distinct
  credential restricted server-side to its area (or an isolated publishing
  service); a shared organization-wide SSH key bypasses the stable approval.
- Protect `main`, workflow changes, and release-tag creation and movement.

## Download layout

The updater and the download links depend on this layout under
`https://download.eclipse.org/enclave/`:

| Path | Contents |
|------|----------|
| `releases/<version>/` | All assets of one release plus `checksums.txt` and `commit`; versions are plain (`1.0.0`, `1.1.0-rc.1`) |
| `releases/latest` | One line naming the highest final release, written after its directory is complete |
| `rolling/` | The assets of the current `main` build, replaced as a whole on every push; only `main` publishes here |
| `rolling/latest` | One line with the full commit SHA of that build, published together with its assets |

The GitHub release keeps a copy of the assets, which the Scoop manifest uses.

## Upload to download.eclipse.org

Uploads go through `.github/actions/publish-downloads`, which follows the
[CBI procedure](https://eclipse.dev/cbi/best-practices/github-actions/upload-to-downloads.eclipse.org/).

| Aspect | Behavior |
|--------|----------|
| Credentials | `SCP_USERNAME`, `SCP_KEY`, and `SCP_PASSPHRASE` secrets, provisioned by Otterdog |
| Host key | Pinned in the action; update `known-hosts` when the Eclipse Foundation rotates it |
| Staging | Each directory is uploaded next to its target; a failed run removes its staging directory |
| Integrity check | Remote SHA-256 verification of every staged file against the workflow's bytes, including `checksums.txt`; a mismatch or missing file fails before publishing or advancing `latest` |
| Publication | New directories and rolling builds are moved into place as a whole with `mv`; retries of existing releases validate the staged files, then add only missing files without replacing existing ones |
| Propagation check | After publishing, reads `checksums.txt` and the pointer back over `https://download.eclipse.org` and warns when they do not match within two minutes; separate from the mandatory integrity check |

GitHub renames uploaded assets, collapsing every run of characters other than
letters, digits, `-`, and `_` into one dot. The assemble job therefore renames
the packages the same way (`scripts/github-asset-name.sh`) before writing
`checksums.txt`: a `1.0.1~~git…` package is published as `1.0.1.git…` on both
hosts, and the assemble job fails on a name that would still change.

## Env-only credential acceptance testing

Unit tests cover credential resolution, placeholder shapes, host release rules,
and auth-file reconciliation without a real engine or account. In a dedicated
Docker or Podman environment, test each variable in
[Env-only bearer tokens](auth.md#env-only-bearer-tokens) independently.

| Area | Test | Expected |
|------|------|----------|
| Any tool | Make an authenticated tool request, restart, repeat | Container gets a fresh placeholder; no placeholder reaches shared auth through reconciliation |
| `codex exec` | Only `OPENAI_API_KEY`, then only `CODEX_API_KEY` | No onboarding needed |
| `codex exec` | API key while a ChatGPT login already exists | Check API-key precedence |
| Codex TUI | Empty store | Onboarding required |
| Codex TUI | Stored login plus env API key | Stored login wins |
| Codex TUI | Accept the prefilled placeholder for one session, then restart with an empty shared store; repeat with project auth scope | Old local `auth.json` is removed |
| Codex TUI | Restart with valid shared auth | Startup restores the symlink instead |
| Codex TUI | `--no-api-key` | Persistent TUI login works |
| `codex exec` | API-key model-list refresh with a PAT also injected, under `--network-log=requests` and coarse logging | Denial at `chatgpt.com`, built-in model-list fallback; OpenAI API requests still work |
| `CODEX_ACCESS_TOKEN` | PAT, then Agent Identity JWT, with gateway protection active | PAT works; JWT fails clearly |
| Any tool | Write a placeholder into a local auth file, then exercise foreground exit, background stop/remove, and `exec` reconciliation | Visible warning, no contaminated copy, existing shared credentials unchanged |

- Use fake Anthropic tokens: upstream's error tells a released `sk-ant-oat01-`
  value from a placeholder, and real subscription tokens in third-party
  clients fall under Anthropic's terms.
- Record tool versions. Claude Code is closed source, so its env-only
  setup-token behavior needs runtime verification.
- Keep test credentials out of logs and review artifacts.
