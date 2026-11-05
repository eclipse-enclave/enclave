# A9-copilot: Copilot driver and extension wiring

Date: 2026-11-05. Branch `feat/a2a-multi-harness`, based on `a2a`.
P2-copilot's real-CLI offline prototype supports a worker driver and additive
orchestrator MCP config. Live GitHub authentication is not claimed.

## Implementation

- `internal/a2aagent/driver/copilot/`: spec validation, installed-version gate,
  random prebound UUID, stdin-only command construction, new/resume arguments,
  private per-context `COPILOT_HOME`, and JSONL decoding.
- Successful result requires `exitCode: 0`, a UUID and a completed main
  assistant message. The last main non-tool message is the answer; an empty
  message is valid. Missing result, incomplete/tool-only stream, malformed
  required fields and session errors fail. Unknown events are logged once and
  skipped. Subagent output cannot replace the main answer.
- `Outcome.Transcripts` and `InvocationTranscripts` inventory the complete
  private home, including Copilot's SQLite prompt/answer index. The optional
  pre-spawn inventory contract (CR-MH-4) covers cancellation before a result.
- The Copilot spec adds `copilot-json`, new/resume arguments, coding card
  skills, a version range `>=1.0.105 <1.1.0`, and orchestrator arguments.
  Worker CLI auto-update, file logging and account session sync are disabled.
  `statePaths` preserves the private-home tree across config overlays.
- `templates/a2a-mcp.json` starts `enclave-a2a-agent mcp` as a local stdio
  server, enabled only through the runtime's orchestrator argument append.
  It augments existing MCP config and writes no persistent server entry.
- Group-only `skills/a2a-provenance/` explains peer provenance, untrusted
  results, context reuse, handles and recovery after interrupted waits.
- The extension README documents support, isolation and the plugin-inheritance
  boundary. Shared format aliases/factory and spec goldens are coordinator-owned.

## Validation

Passed:

```sh
go test ./internal/a2aagent/driver/copilot
go test -race ./internal/a2aagent/driver/copilot
go vet ./internal/a2aagent/driver/copilot
golangci-lint run ./internal/a2aagent/driver/copilot/...
gosec -quiet ./internal/a2aagent/driver/copilot/...
make check-license-headers
PATH=/tmp/enclave-copilot-probe.dcjfAA/install/node_modules/.bin:$PATH \
  go test -tags copilot ./internal/a2aagent/driver/copilot
```

The tagged offline test executes CLI 1.0.105 against an in-process scripted
provider, checks new/resumed conversation history, verifies the private
database/transcript paths, and verifies deletion prevents resume. The tagged
live GitHub test skips without an explicitly exported `COPILOT_GITHUB_TOKEN`;
it does not read the host's OAuth config or invoke login. Unit fixtures are
reduced real CLI success/resume/interruption streams; protocol-error,
subagent-filtering and forward-compatible unknown-event cases are synthetic.

See P2-copilot for measured cancellation behavior and outstanding engine/live
MCP checks. No commits or remote changes were made by this package.
