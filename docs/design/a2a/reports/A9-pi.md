# A9-pi — Pi worker driver and orchestrator wiring

Implemented `internal/a2aagent/driver/pi` and `sandbox.a2a` in Pi's spec.
The tested version gate is `>=1.3.0 <1.4.0`; normal image installs remain
unpinned according to the dependency-update policy. Unsupported worker
versions fail explicitly during adapter startup.

- UUIDs are chosen and bound before spawn; the prompt is exclusively stdin.
- `--session-id` creates a new session; `--session` resumes an existing one.
- Every invocation forces a UUID-private `--session-dir` below the canonical
  `$HOME/.pi` config store, regardless of ambient Pi directory overrides.
  HOME must be absolute. `InvocationTranscripts` exposes this directory before
  spawn, including cancellation before the first session event.
- Decode reads to EOF, bounds events at 64 MiB, logs/skips unknown events,
  rejects invalid/changed IDs and malformed terminal events, and requires a
  non-retrying successful assistant outcome followed by `agent_settled`.
  Explicit empty text/content is a valid final result. Malformed content,
  truncation, abortion and EOF without settlement are errors.
- Outcome inventory owns only `agent/a2a-sessions/<UUID>`; cleanup cannot
  remove another A2A context's transcript or the ordinary session tree.
- The baked `pi-a2a-mcp.js` registers the stdio A2A server directly for the
  orchestrator process, with a 120-second request timeout. Runtime appends
  the explicit built-in MCP and registration extension args only to
  orchestrators. Worker commands receive the existing `--approve` flag
  through the adapter's yolo handling.
- The group-only `a2a-provenance` skill explains authenticated headers,
  untrusted peer text, task/context handles and retry/wait semantics.

Validation: normal fixture/command tests and `go test -tags pi
./internal/a2aagent/driver/pi` passed against Pi 1.3.0, including real bash,
resume, inventory and MCP handshake. `make lint-changed` passed after initial
implementation; final full-project checks are coordinator-owned.

See [P2-pi](P2-pi.md) for cancellation and prototype evidence. This report
claims no real remote-model or Docker/Podman mixed-group acceptance result.
