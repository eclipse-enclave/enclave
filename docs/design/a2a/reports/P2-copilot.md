# P2-copilot: Copilot CLI harness prototype

Date: 2026-11-05. Linux, official npm package `@github/copilot@1.0.105`
(build commit `845c22879d4`). Verdict: worker and orchestrator protocols are
supported. GitHub-backed authentication and engine-level Copilot sessions
remain unverified; the successful prototype used the real CLI with a scripted
loopback OpenAI-compatible provider in offline mode, not a live model.
No billed calls or host-config writes.

## Isolation and evidence

`/usr/bin/copilot` is only an installation wrapper on this host. Installed the
published package, without install scripts, beneath a unique temporary root:

```sh
npm install --prefix /tmp/enclave-copilot-probe.dcjfAA/install \
  --no-audit --no-fund --ignore-scripts @github/copilot@1.0.105
```

All probes set `HOME`, `COPILOT_HOME` and all four XDG roots beneath that
temporary directory, stripped inherited Copilot/GitHub token variables, and
ran in its empty `work/`. Provider calls used `COPILOT_OFFLINE=true`,
`COPILOT_PROVIDER_BASE_URL=http://127.0.0.1:19843/v1`,
`COPILOT_PROVIDER_TYPE=openai`, and model `gpt-4.1`. The provider supplied
deterministic text or a controlled Bash tool call. Raw help, events, HTTP
requests and probe scripts remain in that disposable directory; concise
recorded stream fixtures are checked in under `driver/copilot/testdata/`.

Primary references: the installed CLI's `--help`, `help environment` and
`help config`; GitHub's [programmatic CLI reference](https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-programmatic-reference)
and [SDK event reference](https://github.com/github/copilot-sdk/blob/main/docs/features/streaming-events.md).
Observed 1.0.105 output, rather than an older example event schema, determines
the decoder.

## Observed protocol

| Concern | Evidence |
| --- | --- |
| Headless prompt | Nonempty piped stdin selects prompt mode without `-p`; no prompt text in argv. Empty stdin exits 1 with a diagnostic. |
| Event stream | `--output-format json` emits JSONL; `assistant.message.data.content` is the complete assistant message. Deltas and tool requests are separate events. |
| Finality | Terminal `{"type":"result","sessionId":"<UUID>","exitCode":0,...}` has no answer text. The preceding main assistant message supplies the answer. EOF alone is not success. |
| New context | `--session-id <caller-selected UUID>` creates the chosen session. The UUID appears in the terminal result; `session.start` is in the persisted log but was absent from stdout. |
| Resume | `--resume <UUID>` retained the same ID and prior turn. The tagged offline test verifies both prompts are in the resumed provider request. Unknown IDs exit 1, no terminal result; they are not silently created. |
| Permissions | `--yolo` is supported, granting tools/paths/URLs; a real CLI Bash call ran without an approval prompt. `--no-ask-user` removes the question tool. |
| Main vs subagents | Current SDK events distinguish subagents via envelope `agentId`; legacy `data.parentToolCallId` is also filtered. This filtering is fixture-tested, not a billed subagent observation. |
| Orchestrator MCP | `--additional-mcp-config @<file>` adds a local stdio server. A temporary server received initialize and tools/list; stdout reported it connected and the provider request included `enclave-a2a-list_peers`. This was a protocol probe, not a live peer-group task. |

## Retention: a shared database is not safe to inventory

Native files include `session-state/<UUID>/events.jsonl`, `workspace.yaml`,
checkpoints and rewind snapshots. In addition, **shared** `session-store.db`
contains `turns.user_message`, `turns.assistant_response`, summaries,
checkpoints and a full-text search index. SQL inspection confirmed that the
probe prompt and `OK` answer were actually duplicated there. Inventorying
only `session-state/<UUID>` would leave task content after context expiry;
deleting the shared database would remove unrelated contexts.

The driver therefore assigns every worker context a private `COPILOT_HOME`
inside `<config store>/a2a-harness/copilot/<UUID>/` and inventories that whole
directory. This scopes the native database, WAL/SHM sidecars, operation locks
and native session state together. Only allowlisted configuration, auth,
instructions, skills and MCP inputs are linked from the normal config root;
installed plugins, plugin data and other session history are not inherited.
Worker logging, auto-update and account session sync are disabled by flags.
Ordinary sessions and orchestrators keep their usual config root.

The real offline integration test verifies the database and native transcript
exist under the private home, resumes the context, confirms no database was
created at the shared config root, removes the private home, then checks that
resume fails. Optional pre-spawn inventory (CR-MH-4) covers an interruption
before the terminal result supplies the ID.

## Cancellation

Controlled tool: a Python process starts `setsid sleep 120` and waits. With
the real CLI in its own process group, observed `(pid, pgid, sid)`:

| Process | PID | PGID | SID |
| --- | ---: | ---: | ---: |
| CLI | 2712073 | 2712073 | 2712073 |
| Python Bash tool | 2712201 | 2712201 | 2712201 |
| Detached sleep | 2712202 | 2712202 | 2712202 |

SIGTERM to the CLI group exited the CLI with `-15` and removed the Python
tool, but the detached sleep remained running. It was explicitly SIGKILLed
by the probe cleanup. The existing executor's descendant sweep/subreaper
behavior remains necessary; process-group signalling alone is insufficient.
The interrupted stream lacks a result and is rejected by the fixture test.

## Remaining validation

- Live GitHub routing, OAuth-config inheritance, and a real provider's errors
  are not validated: no `COPILOT_GITHUB_TOKEN` was exported.
- The stdio MCP lifecycle was probed with a small temporary server. A real
  Enclave MCP send/wait/retry plus Copilot UI liveness is still an engine smoke
  check, not established by this prototype.
- Docker/Podman Copilot image launch and mixed-harness peer calls are not
  claimed here; the image installs the same npm CLI without extra headless
  dependencies. The runtime's generic descendant tests supply the executor
  guarantee beyond the process-group failure measured above.
