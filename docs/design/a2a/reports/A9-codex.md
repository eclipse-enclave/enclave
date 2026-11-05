# A9-codex: Codex driver and spec

Implemented on `feat/a2a-multi-harness`, separate from `a2a`. Scope:
`internal/a2aagent/driver/codex/`, the Codex extension spec/README, and these
Codex reports. No commits or remote writes.

The Codex extension also ships the group-only `a2a-provenance` skill, adapted
from the established Claude guidance using native name/description frontmatter.
It distinguishes peer task handles from Codex thread/command IDs and includes
the safe epoch-unconfirmed resend rule.

## Behavior

- `codex.New(spec, Options)` implements Driver for `codex-exec-json`.
  The shared registry and serve factory are coordinator-owned.
- NewSessionID is empty because Codex chooses the thread UUID.
- Command preserves prompt-on-stdin, validates resume UUIDs, appends the
  spec's resume subcommand and a final stdin `-`, and leaves process
  creation/signals to the executor.
- The command allocates a fresh SQLite directory for each turn, overrides
  sqlite_home and CODEX_SQLITE_HOME, and uses the same resolved CODEX_HOME as
  transcript inventory. InvocationTranscripts returns the private directory
  for the executor to persist before spawn; Decode includes it on every
  outcome, including an interrupted stream without a thread ID.
- Decode emits session and progress events, reads through EOF, and succeeds
  only with a valid UUID, completed turn and final agent message. A failed
  turn, malformed required event, read error or unfinished subsequent turn
  fails. Top-level reconnect `error` diagnostics are logged; a later completed
  turn can still succeed. The executor separately requires process exit zero.
- Rollouts and snapshots are discovered by exact UUID beneath their known
  config-store roots. Symlinks and shared databases are excluded. Whole private
  SQLite directories are retained/deleted with their owning A2A context.

The private SQLite root is necessary because native databases contain prompt
and response text, not just a disposable file index. Resume with a fresh
database was verified against the installed harness; see P2-codex.

## Spec

Worker command uses `codex exec --json`, disables the daemon and memories,
and receives the existing profile yolo flag from the serve factory. Version
pin `>=0.166.0 <0.167.0` intentionally covers only the probed pre-1.0 minor;
an unprobed minor must be checked before widening it.

Orchestrator arguments configure the `enclave-a2a` stdio MCP server and its
explicit environment allowlist using `-c` overrides. They disable detached
daemon startup and write no persistent MCP config. The generic runtime's
orchestrator-only argument placement is unchanged. No new packages or image
assets are required by exec or native MCP support.
The native MCP tool timeout is raised from 60 to 150 seconds to cover the
default A2A send wait plus admission grace; longer custom waits need a matching
tool-spec override or shorter per-call waits.

## Verification

- `go test -race ./internal/a2aagent/driver/codex`: pass.
- `go test -tags codex ./internal/a2aagent/driver/codex -run TestRealVersion -v`:
  pass against 0.166.0.
- `CODEX_A2A_TEST_AUTH_FILE=<temporary copied auth> go test -tags codex
  ./internal/a2aagent/driver/codex -run TestReal -v`: pass (new context and
  exact-ID resume; marker retained). Real tests isolate HOME/CODEX_HOME/XDG
  and never implicitly import host auth. API-key auth is also supported;
  without explicit credentials, billed tests skip.
- `make lint-changed`: pass over the shared branch at the time of the run.
- Fixtures cover observed success, resume and a killed tool call; protocol
  tests cover missing outcomes, failed/malformed events, changed IDs, unknown
  event compatibility, reconnect diagnostics and read failures.
- Other tests cover argv/stdin separation, invalid UUIDs, version bounds,
  invocation-only MCP config and required environment forwarding, exact-file
  transcript selection, symlink exclusion, fresh SQLite roots and inventory
  preservation on errors.

Full branch build/test/lint, extension surface golden regeneration and the
Docker/Podman mixed-harness acceptance scenario are coordinator-owned. A host
probe is not a claim that the container acceptance scenario has passed.
