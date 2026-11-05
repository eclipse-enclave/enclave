# P2-opencode — OpenCode harness prototype

**Verdict: upstream orchestrator-capable; neither built-in role is wired by
this change.** A worker would violate the current per-context retention
contract. Automatic orchestrator-only wiring would require separating
orchestrator metadata from the worker `sandbox.a2a` block; that schema change
is deliberately not introduced here. Manual CLI orchestration remains the
existing generic fallback and must keep `client hold` alive.

## Evidence (2026-11-05)

Installed `opencode-ai@1.19.5` only under
`/tmp/enclave-a2a-pi-opencode.91q7zA/opencode/`. No host package installation
or host config/auth change was performed. All probes used private HOME and
XDG roots and a deterministic local OpenAI-compatible SSE endpoint, with
`OPENCODE_CONFIG_CONTENT` defining a disposable provider. No remote model
was billed; this is harness evidence, not live peer-group acceptance.

| Requirement | Measured result |
| --- | --- |
| Headless/stdin | `opencode run --format json --auto` reads the prompt from stdin. |
| Session/resume | Stream events carry `sessionID:ses_...`; `--session <id>` resumed the same ID. Unknown ID exits 1, Session not found. No supported CLI argument chooses a new session ID before spawn. |
| Final/event format | `step_start`, `text` with `part.text`, then `step_finish` with `part.reason:stop`. Terminal errors and model/tool continuation would need explicit decoder treatment; no driver is claimed. |
| Approval | `--auto` auto-approves permissions unless explicitly denied. The isolated probe configured permission allow; no user permission config was reused. |
| Inventory blocker | Session/message/part rows share `$XDG_DATA_HOME/opencode/opencode.db` (plus WAL/SHM). Snapshots are project-scoped, not session-scoped. Deleting the database as one context's `Outcome.Transcripts` would delete unrelated contexts. The existing extension links the data root into its config store, so moving the root alone does not solve selective cleanup. |
| MCP | Config `mcp.<name>={type:local,command:[...]}` in OPENCODE_CONFIG_CONTENT connected a real stdio server: initialize (2025-11-25), initialized, tools/list, then EOF on shutdown. This proves upstream MCP feasibility, not automatic Enclave orchestration. |
| Cancellation | A real bash `sleep 300` ran in a detached group. Both SIGTERM and SIGKILL to the harness group left that tool process alive at the immediate post-exit observation. Each probe cleaned only its own remaining process; Linux subreaper/descendant cleanup would be mandatory in any future worker. |

A supported worker needs a deliberate follow-up: e.g. a per-context stable
storage key distinct from the harness-chosen session ID, or a selective
session cleanup API rather than path deletion. Do not advertise a worker by
returning an empty inventory or by assigning the whole database to one task.

Raw captures (new/resume/unknown/MCP/cancellation) are under the unique `/tmp`
directory above. No OpenCode `sandbox.a2a`, event registry entry or incomplete
driver was added. Upstream reference:
[CLI](https://dev.opencode.ai/docs/cli/).

## Theia check

The shipped Theia profile starts `sleep infinity` and attaches a host IDE.
It exposes neither a headless stdin/event/resume harness nor an orchestrator
MCP CLI argument. This implementation therefore wires neither role for
Theia. That is a current integration verdict, not a claim that Theia AI can
never use MCP; IDE-specific configuration and lifecycle integration would be
a separate design.
