---
title: Host lifecycle hooks continuation handoff
project: enclave
feature: host-lifecycle-hooks
worktree: null
branch: feat/host-lifecycle-hooks
created: 2026-10-16
tool: codex
---

# Continuation handoff

## Workspace and authorization

- Repository: `$HOME/Git/OpenSource/enclave/enclave`; main checkout, not a linked
  worktree. Branch `feat/host-lifecycle-hooks`; pre-commit HEAD was
  `671cca2c2eea68266bd3d5238043dfa40e35d8c0`.
- This handoff was prepared with implementation, examples, tests, docs and setup
  script uncommitted. The user subsequently authorized committing everything,
  including this repository copy of the handoff. Check current `git status` and
  history before editing; do not assume the original uncommitted state persists.
  No pushes or remote issue/PR changes were authorized or performed.
- Read repository `AGENTS.md` and `README.md`. Before an authorized commit, use
  `write-commit-message`; run the repository's required build/test/lint checks.
- No implementation task is currently outstanding. The latest request is to
  commit everything including this handoff; no next-session focus was supplied.

## Read existing sources rather than reconstructing the design

1. [Implementation plan](../../../AI/artifacts/enclave/host-lifecycle-hooks/implementation-plan.md): full approved scope, contracts,
   ordering and validation matrix. Its opening status has been updated.
2. [Implementation and validation result](../../../AI/artifacts/enclave/host-lifecycle-hooks/implementation-result.md): delivered
   core work, check commands, live Docker/PTY evidence and coverage limits.
   This report predates the later title POC and local setup script below.
3. Repository `docs/extensions/host-hooks.md`: implemented v1 contract. Relevant
   production code is `internal/hosthooks/`, `internal/hosthooks/builtin/`, app
   composition/replay and the runtime terminal callback. Inspect current diffs
   for implementation details rather than duplicating them into this handoff.
4. `examples/host-hooks/features/session-title/README.md`: runnable title POC,
   direct-copy and Git installation instructions, protocol sketch and limitations.
5. Root `setup-host-hooks.sh`: executable setup for the existing `enclave-test`
   wrapper. Its implementation is the source of truth for backup/update behavior.

## Follow-up work since the main implementation report

- User requested a title extension demonstrator. Added the external-only
  `session-title` feature beside Herdr and tint, with Python provider tests.
  Core was not extended for this example. The README documents terminal support,
  one-shot labeling and potential interference from agent/shell title updates.
- Extended `internal/hosthooks/external_test.go` with
  `TestExternalTitleAndTintHaveIndependentCleanup`, and extended the real temporary
  Git installation fixture in `internal/extinstall/host_hooks_test.go` to cover all
  three example packages. The provider README and diffs carry the details.
- User then requested a setup script in the checkout for Herdr **and title**
  (not external tint) using `enclave-test` on PATH. Root `setup-host-hooks.sh`
  discovers the wrapper's global config, checks the known test layout, refreshes
  its binary from `bin/enclave`, copies working-tree user providers and merges
  explicit activation while preserving unrelated configuration/settings.
  It supports `--dry-run`, backups, rerun idempotence and config-file symlinks.
- No commit is needed to try these providers through the script's direct-copy
  path. Normal Git installation still requires the files to exist as Git content;
  tests use disposable Git fixtures, not a commit of this checkout.

## Current enclave-test state: verified read-only at handoff

The wrapper at `$HOME/.local/bin/enclave-test` executes
`$HOME/temp/enclave-test/enclave`, with XDG config/state/cache redirected under
`$HOME/temp/enclave-test/`. `make build` alone does not update that target.

An earlier check found its binary stale; the setup script was written to address
that. **At handoff time the setup is now applied**:

- Test binary bytes match this checkout's `bin/enclave`.
- `enclave-test config --json --view effective` reports host-hook support and
  `user:herdr.enabled=true`, `user:session-title.enabled=true`.
- Both copied provider directories exist under the test config's `hooks/host/`.

The agent executed only dry-run against the real setup plus isolated write tests;
activation happened subsequently outside those agent actions. Do not reinstall or
change user settings merely to resume. This proves installation/activation, not
actual upstream Herdr recognition or graphical terminal restoration. Herdr policy
and eligibility are documented in its provider README; preserve an existing
`HERDR_AGENT`, including the intentional empty opt-out.

## Verification for the follow-ups

- Title provider: nine Python tests passed, including the executable JSON boundary,
  settings/context handling, control-character sanitization and Unicode size bounds.
- `make test` and `make lint-changed` passed after adding title integration and
  installer coverage. Full core build/lint/race/Darwin compile and live Docker
  checks are documented in the existing implementation result; they are not
  evidence of a real graphical terminal or Herdr acquisition check.
- Setup script: `bash -n`, ShellCheck, license-header check and `git diff --check`
  passed. Real-wrapper `--dry-run` resolved the intended isolated paths.
- Isolated setup fixture passed actual binary replacement/provider installation,
  activation/settings preservation, config symlink/mode preservation, recovery
  backups, no-op rerun and rejection of malformed config before writes. No test
  fixture code was added to the repo for this local setup utility.
- Temporary repository-pinned lint tools are at
  `/tmp/enclave-host-hooks-tools`; prepend that directory to PATH if still present.
- Before the authorized commit, `make generate`, `make build`, `make test`,
  full `make lint` (including license headers and ShellCheck), all 14 Python
  provider tests, `bash -n setup-host-hooks.sh` and `git diff --cached --check`
  passed with all implementation and follow-up files present.

## Next-session orientation

Follow the next user request. Possible useful follow-up is controlled live Herdr
recognition/title behavior debugging now that the test setup is active. Do not
claim that acquisition was verified simply because the hook executes. Consult the
existing report for unverified Podman/QEMU/macOS/WSL coverage rather than repeating
those caveats or provisioning infrastructure automatically.

The final conversational idea was **per-project session time tracking** via
terminal-enter state and terminal-leave bookkeeping. It was only a suggestion,
not requested or implemented. It would measure attachment/session duration, not
agent busy time or individual task completion, and leave notifications are not a
crash-proof audit mechanism. Do not start implementing it without a new request.

## Suggested skills

The next agent should invoke these through its Skill tool when applicable (or
read their `SKILL.md` if that is how the environment exposes skills):

- `artifacts`: before writing/updating reports, plans or handoffs; keep them in
  the artifacts archive by default; this repository copy was explicitly requested by the user.
- `herdr`: only if explicitly asked to inspect/control live Herdr panes or agents;
  check its `HERDR_ENV=1` prerequisite and instructions first. Discussing or editing
  the provider itself does not require operating Herdr.
- `write-commit-message`: mandatory before any explicitly authorized repository
  commit; the skill does not itself grant permission to commit or push.
- `writing-for-agents`: when modifying `AGENTS.md`, `CLAUDE.md` or skills.
- `handoff`: for another continuation handoff; update this document instead of
  creating a duplicate on the same topic.
