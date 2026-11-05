# P2-codex: Codex harness prototype

Date: 2026-11-05. Host Linux; installed `codex-cli 0.166.0`. Verdict:
**worker and orchestrator**, with the requirements below. All probes used a
unique `/tmp/enclave-p2-codex-*` tree with temporary HOME, CODEX_HOME and XDG
roots. Only an auth file was copied from the host, without printing it. Host
config was neither imported nor changed. Live responses used `gpt-6.1-sol`,
low reasoning and one-word outputs; the process probes deliberately ran only
owned `sleep` processes, then killed and reaped them.

## Headless events and resume

New turn:

```sh
codex exec --json --dangerously-bypass-approvals-and-sandbox \
  -c features.daemon_auto_start=false -c features.memories=false -
```

Resume: append `resume <thread-uuid> -` after the exec options instead of the
final `-`. Both commands read the prompt from stdin; it never appears in
argv. `thread.started.thread_id` is the Codex-chosen UUID: there is no
caller-chosen session flag in exec help. NewSessionID therefore returns empty.

Observed successful stream, in order:

1. `thread.started` with `thread_id`.
2. `turn.started`.
3. `item.completed` whose `item.type` is `agent_message`, with `item.text`.
4. `turn.completed` with token usage.

The final agent message is not itself an outcome: require turn completion
and EOF, and honor the subprocess exit. Tool invocation produces
`item.started` / `item.completed` for `command_execution` or `mcp_tool_call`.
Unknown events must be logged and skipped, and killed streams fail without
a terminal outcome. Recordings sanitized to a fixed UUID are in the driver's
`testdata/` directory. New + resume retained the marker ORCHID and the exact
same UUID. The final driver was also exercised by its opt-in real test.

## Cancellation

Two process probes started Codex in a new process group, with the probe
parent a Linux child subreaper. A shell ran `sleep 90 & setsid sleep 91 &
wait`; the probe waited for an owned marker before signalling Codex.

| Signal to Codex process group | Observed after Codex exit |
| --- | --- |
| SIGTERM | Two live sleep descendants and two zombies remained |
| SIGKILL | Two live sleep descendants and two zombies remained |

The probe's descendant-tree SIGKILL and waitpid sweep removed all survivors.
Process-group signalling alone is insufficient for Codex, just as for Claude.
The existing executor subreaper/tree sweep is required on success as well as
cancellation. This was a host process-tree probe, not a container lifecycle
or quarantine test.

## Retention: SQLite contains conversation content

Per-thread files observed under CODEX_HOME:

- `sessions/YYYY/MM/DD/rollout-<local-timestamp>-<uuid>.jsonl`.
- `shell_snapshots/<uuid>.<timestamp>.sh` after shell tools run.
- Archived rollouts use `archived_sessions/`.

Rollout names cannot be derived from `thread.started` alone. Inventory scans
only the known transcript roots for the exact UUID; it does not follow
symlinks or include another thread's files.

**Deleting those files alone is insufficient.** SQLite inspection found
conversation content in `state_5.sqlite` (`threads`) and
`thread_history_1.sqlite` (`thread_items`), even with memories disabled.
The `sqlite_home` override successfully redirected all six observed databases
(state, thread_history, logs, goals, queue and memories) to a private root.
Resuming the same UUID with a fresh SQLite root reconstructed its history from
the retained rollout and returned ORCHID.

The implementation allocates one config-store-relative
`a2a-codex-sqlite/turn-*` directory per invocation. It sets both `sqlite_home`
and CODEX_SQLITE_HOME, inventories the whole directory, and exposes it through
InvocationTranscripts before spawn. No shared database is deleted or edited.
Disabling the daemon avoids detached writers; disabling memories prevents
cross-context generation. Dynamic rollout/snapshot discovery still has the
existing CR-A9-2 limitation if the adapter crashes before recording the
harness-generated UUID; the deterministic SQLite inventory has no such gap
once the executor's pre-spawn binding succeeds.

## Orchestrator MCP

Invocation-only `-c` overrides for
`mcp_servers.enclave-a2a.command="enclave-a2a-agent"` and
`mcp_servers.enclave-a2a.args=["mcp"]` were accepted by `codex mcp list --json`.
A small local stdio server completed initialize, tools/list and a real
model-driven tools/call; Codex returned A2A_OK.

**Codex filters MCP subprocess environments.** Without `env_vars`, an
ENCLAVE_A2A canary was absent from the server. With an explicit `env_vars`
allowlist it was present (checked at server startup, then the invocation was
terminated). The shipped spec forwards connection identity/token, journal and
client-bound variables by name, never token values. Overrides do not persist
into ordinary sessions. The detached daemon is disabled for orchestrators too.

Official Codex documentation gives MCP tools a default timeout of 60 seconds,
shorter than A2A's 90-second default send wait plus admission grace. The spec
raises the server-specific timeout to 150 seconds. Long-wait behavior was not
live-probed here; custom A2A send waits above that native timeout need a matching
tool-spec timeout override or shorter per-call waits.

This proves native Codex stdio MCP transport and configuration, not the full
mixed-group acceptance scenario or long-wait/reconnection semantics. Those
belong to I3 and existing MCP client integration tests.

## References

Local `codex exec --help`, `codex exec resume --help` and the installed
0.166.0 behavior are authoritative for the pin. Official OpenAI documentation
also describes [noninteractive JSON output](https://learn.chatgpt.com/docs/non-interactive-mode),
[MCP configuration](https://learn.chatgpt.com/docs/extend/mcp?surface=cli) and
the [sqlite_home setting](https://learn.chatgpt.com/docs/config-file/config-reference).
