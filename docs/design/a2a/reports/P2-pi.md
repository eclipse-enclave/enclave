# P2-pi — Pi harness prototype

**Verdict: both worker and orchestrator**, with native MCP in tested Pi 1.3.0.
The handoff's MCP-less fallback assumption no longer applies to this release.

## Evidence (2026-11-05)

Real `/home/tobias/.local/bin/pi` reports `1.3.0`; the managed package metadata
agrees. Runs used private HOME, all four XDG roots, PI_CODING_AGENT_DIR,
PI_OFFLINE=1, and PI_TELEMETRY=0 under a unique `/tmp` directory. No host config
or auth was read, copied, refreshed or modified. A local deterministic
OpenAI-compatible SSE endpoint supplied responses; no remote model was billed.
This establishes harness mechanics, not model quality or a live peer-group run.

| Requirement | Measured result |
| --- | --- |
| Headless/stdin | `pi -p --mode json --approve` reads the prompt from stdin, not argv. |
| Session | First `session` event carries UUID, cwd, original timestamp. `--session-id <UUID>` creates exactly that ID. |
| Resume | `--session <UUID>` resumes the same ID; an unknown ID exits 1 without an outcome. `--session-id` alone creates missing IDs, so it is not used to resume. |
| Events/final | `message_end` assistant text is progress. `agent_end.messages` contains the final assistant message and `willRetry`; `agent_settled {aborted:false}` follows only after the entire operation settles. `stopReason:stop` is success; error, aborted, length, retry-pending or missing settlement fail. |
| Tools/approval | An actual bash tool wrote a marker without a permission prompt. `--approve` is project-resource trust, not a separate tool permission system. |
| Inventory | Native JSONL names include a timestamp under `agent/sessions/<encoded-cwd>/`. The driver instead sets `--session-dir $HOME/.pi/agent/a2a-sessions/<UUID>`; that whole config-store-relative directory is owned by one context. A live new/resume check confirms files exist there. Explicit CLI location wins over PI_CODING_AGENT_DIR and PI_CODING_AGENT_SESSION_DIR overrides. |
| MCP | Explicit `--extension builtin:mcp --extension <a2a-mcp.js>` loads a session-only registration. Real Pi sent initialize (2025-11-25), initialized, tools/list; the tool was exposed directly, and stdio closed at session shutdown. No user MCP/settings files are changed. |
| Cancellation | A real bash `sleep 300` ran in a detached process group, different from Pi's group. Group SIGTERM to Pi let its handler reap the tool child. Group SIGKILL left the detached child alive. The adapter's Linux subreaper/descendant termination is therefore necessary; process-group signaling alone is insufficient. The probe killed only its own escaped process afterward. |

The MCP registration sets a 120-second per-request timeout, above the 90-second
A2A wait. Pi documents progress resetting this timeout. The built-in extension
must be explicit when `--no-extensions` disables discovery. A conflicting
user-configured server may override a registration; users must not disable or
replace the A2A server in orchestrator sessions.

## Reproduction and fixtures

```sh
go test ./internal/a2aagent/driver/pi
go test -tags pi ./internal/a2aagent/driver/pi -v
```

The tag-gated test uses the real Pi binary, a local model endpoint, actual bash,
new/resume/unknown-resume, retention-directory existence and the shipped MCP
extension. It supplies no credentials and isolates config. Success/resume
fixtures were captured from real harness output with verbose system-prompt
content omitted. Other fixtures deliberately synthesize tools, retry, error,
abort, truncation, unknown event, empty final and malformed terminal payloads.

Raw probes and process evidence: `/tmp/enclave-a2a-pi-opencode.91q7zA/`.
Durable implementation details are in [A9-pi](A9-pi.md).
Upstream references: [CLI](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/cli.md),
[MCP](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/mcp.md).
