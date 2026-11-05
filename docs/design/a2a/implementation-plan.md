# A2A implementation plan (parallel edition)

Companion to `proposal.html` (draft 6, revision 1). The proposal is the contract; this
plan turns it into work packages that independent agents can execute in
parallel from separate worktrees. Nothing here changes a proposal decision.

## How parallel work is organized

- **Interfaces are frozen before fan-out.** Wave 0 produces one document,
  `docs/design/a2a/interfaces.md`, that fixes every format two packages share:
  file layouts, environment variable names, labels, log markers, Go package
  APIs. After Wave 0, a package may not change an interface; it files a change
  request in that document and the coordinator decides.
- **Each package owns a disjoint set of files.** The ownership table below is
  the rule. A package that needs a change outside its files asks the owning
  package or the coordinator instead of editing.
- **Stubs first.** Wave 0 commits compiling Go stubs (types, interfaces,
  `panic("not implemented")` bodies) for every package boundary, so packages
  compile and test against each other's signatures before the bodies exist.
- **One worktree per package.** Agents run with worktree isolation. Never use
  bare `git stash`. Each package ends in a branch, a passing `make build`,
  `make test`, `make lint-changed`, and a short report in
  `docs/design/a2a/reports/<package>.md` (findings, deviations, open items).
- **The coordinator** (the main session) does Wave 0, assigns packages, reviews
  reports, merges in the order given per wave, and runs the integration
  packages in Wave 3.
- **Prototypes run alongside code**, not before it. Code packages build
  against the frozen interfaces; prototype results feed in as bound values and
  as corrections through the change-request path.

## Wave 0: freeze (coordinator, two agents in parallel)

### W0-A: spec pin and carrier check

Output: `docs/design/a2a/spec-pin.md` and proposal section 4 filled in.
Pick the latest tagged A2A release; cite the spec field that carries each row
of the carrier table; list every rename of method names, card path, task
states, message and artifact shapes. A missing carrier stops everything and
goes back to the proposal.

### W0-B: interface document and stubs

Output: `docs/design/a2a/interfaces.md` and a stub commit. Must define:

1. **Snapshot layout**: `<state>/a2a/<group>/snapshots/<revision>/` with
   `membership.json`, `secrets.json` (0400, proxy uid), `manifest.json`
   (revision, group generation, file hashes); `current` symlink; retention.
2. **Status files**: `<state>/a2a/<group>/status/supervisor.json` and
   `proxy.json` with applied revision, rejected revision, error, timestamp.
3. **Group record** and **cleanup inventory** JSON shapes.
4. **Gateway overlay**: `<state>/projects/<hash>/<tool>/a2a-overlay/<session>/overlay.json`
   with hub address, hub port, adapter port, DNS name; reload log markers
   `A2A overlay applied (generation=...)` and the failure variants.
5. **Context table**: `<config store>/a2a/` layout: `contexts.json`,
   `tasks/<id>.json`, `dedupe.json`, `restrictions.json`, transcripts path.
6. **Environment variables** for the adapter and client: outbound token,
   inbound token, principal fields, hub address and port, adapter port,
   role, bounds.
7. **Labels**: `enclave.a2a.hub`, `enclave.a2a.group`,
   `enclave.a2a.group_generation`, `enclave.a2a.member`, `enclave.a2a.role`.
8. **Go APIs** as stubs:
   - `internal/a2a`: JSON-RPC, SSE, card, message, task, handle types.
   - `internal/peergroup`: `GroupStore`, `SnapshotPublisher`, `Inventory`,
     `Tokens`, `WorktreeGuard`, `StatusWaiter`.
   - `internal/backend`: `PeerGroupBackend` interface (`ConnectHub`,
     `DisconnectHub`, `StartHub`, `ReloadHub`, `StopHub`, `SignalWorker`).
   - `internal/a2aagent` (adapter library): `Driver` interface for harnesses,
     `ContextTable`, `Executor`, `Server`, `MCPClient`.
   - `internal/tools`: `A2ASpec` struct for the `a2a` block in `spec.yaml`.
9. **`ps --json` a2a object** field list and tri-state encoding.
10. **CLI surface**: option names, flag names, verbs, exit codes.

W0-B does not depend on W0-A except for the `internal/a2a` type names, which
it leaves as placeholders that W0-A fills in on merge.

Merge order: W0-B stubs, then W0-A type names.

## File ownership

| Package | Owns | Must not touch |
| --- | --- | --- |
| P1-D, P1-P | `/tmp` only, `docs/design/a2a/prototypes.md` | other repo files |
| P2 | `/tmp` only, reports | repo |
| P3 | `/tmp` only, reports | repo |
| A1 | `internal/a2a/` | everything else |
| A2 | `internal/peergroup/`, `internal/config/host_paths.go` (additive) | options_def.go |
| A3 | `cmd/enclave-a2a-hub/`, `internal/a2ahub/` (proxy library) | Dockerfile, backend |
| A4 | `Dockerfile.a2ahub`, `a2ahub-entrypoint.sh`, `internal/gateway/a2ahub_build.go` | proxy code |
| A5 | `gateway-entrypoint.sh`, `runtime-assets/net.sh`, `internal/gateway/` (overlay mount, markers) | bundle package |
| A6 | `internal/docker/network*.go` (connect, disconnect), `internal/docker/signal.go` | backend lifecycle |
| A7 | `internal/backend/docker/a2ahub.go`, `network.go` (ownership, teardown), `a2a_reconcile.go` | app, cli |
| A8 | `internal/a2aagent/contexttable/`, `internal/a2aagent/executor/` | server, driver |
| A9 | `internal/a2aagent/driver/claude/`, `internal/tools/a2aspec.go`, `extensions/tools/claude/spec.yaml` (a2a block) | server |
| A10 | `internal/a2aagent/server/`, `cmd/enclave-a2a-agent/` (serve) | contexttable internals |
| A11 | `internal/a2aagent/mcp/`, `cmd/enclave-a2a-agent/` (mcp subcommand) | server |
| A12 | `internal/config/options_def.go`, generated files, `docs/configuration.md` option rows | cli verbs |
| A13 | `internal/cli/a2a_command.go`, `internal/app/a2a_cmd.go`, `internal/app/command_ps.go` (a2a object) | options_def.go |
| A14 | `runtime-assets/build-scripts/bin/enclave-install-a2a-agent`, `internal/app/dockerfile_gen.go` (install line), `entrypoint.sh` (serve mode branch) | adapter code |
| A15 | `extensions/tools/claude/templates/`, `extensions/tools/claude/skills/a2a-provenance/` | spec.yaml |
| I1 | `internal/app/command_run.go`, `command_stop.go`, `cleanup.go` (join, leave, reconcile wiring) | all package internals |
| I2 | smoke script under `scripts/`, Docker-tagged integration tests | package code |
| D1..D6 | one doc file each | code |

`cmd/enclave-a2a-agent/main.go` is split by subcommand file: A10 owns
`serve.go`, A11 owns `mcp.go`, W0-B owns `main.go`.

## Wave 1: foundations and prototypes (up to 9 agents)

All start after Wave 0 merges. None depend on each other.

| ID | Package | Inputs | Exit criterion |
| --- | --- | --- | --- |
| P1-D | Topology prototype on Docker | interfaces (overlay, snapshot, rules) | Evidence list of proposal 23.P1 on Docker; reconnect and ack timeouts measured |
| P1-P | Topology prototype on rootless Podman, netavark strict | same | Same list on Podman; verdict on v1 support; secrets uid mapping under keep-id |
| P2 | Harness prototype | context table format, Claude headless | Evidence list 23.P2; deadline, queue, result size, confirmation timeout measured |
| P3 | Client prototype | MCP tool shape | Evidence list 23.P3; wait default, credential lifetime measured |
| A1 | Protocol package | spec pin | Types for the pinned version; handle parse and format; golden tests from spec examples; fuzz on handles |
| A2 | Host state | interfaces 1 to 3, 8 | Group store, snapshot publisher with atomic swap and retention, inventory write-before-admission, tokens and credentials, group lock, worktree guard, status waiter; tests with temp home |
| A5 | Gateway overlay and rules | interface 4 | Overlay read on start and reload; INPUT, OUTPUT, NAT, dnsmasq rendering; markers; rendering tests next to `firewall_test.go`; reload wait variant |
| A6 | Engine primitives | none | `NetworkConnect` with static IP, `NetworkDisconnect`, signal helper; Docker-tagged tests |
| A8 | Context table and executor | interface 5, `Driver` stub | Persisted admission, dedupe, bindings, tombstones, restrictions, TTL with active exclusion, process-group execution with descendant confirmation and termination field; fake driver tests for every ending in proposal 12 |

P1-D and P1-P are split by engine so that neither waits for the other's
machine. P1-P runs on this host's podman 4.9 / netavark 1.4 under
`isolate=true` and, if a strict-capable netavark is available, under `strict`
too; a pre-P1 matrix is already in `prototypes.md` and backs CR-1, which is
accepted. A7 and A13 reject `--peer-group` only on a podman without an
isolate option.

Merge order after Wave 1: A1, A2, A6, A5, A8. Prototype reports feed bound
values into A12 and corrections into the interface document.

## Wave 2: components (up to 8 agents)

| ID | Package | Depends on | Exit criterion |
| --- | --- | --- | --- |
| A3 | Hub proxy | A1, A2 (status, snapshot readers) | Loads `current`, verifies manifest, refuses mixed snapshots and keeps last revision, status file, cards and listing, name-based forwarding with SSE passthrough, header stripping, inbound hash check, outbound token injection, principal forwarding, liveness connection, delivery errors, advisory owner cache, status-only audit; tests with a fake adapter |
| A4 | Hub image and supervisor | A2 (snapshot layout), P1 verdict | Dockerfile from gateway base; entrypoint installs OUTPUT rules from snapshot, forwarding off, conntrack flush on removal, status file, SIGHUP reload, drops to proxy user; build-input hashing like `gateway_proxy_build_inputs.go`; rule rendering tests |
| A7 | Backend lifecycle | A2, A6, A5, A4 | `PeerGroupBackend` for Docker; `sessionOwnsEndpoints` treats labeled hub endpoints as owned; teardown disconnects hub first; GC disconnects orphaned hubs and removes empty hubs; join and leave sequences of proposal 15 as reversible steps under the group lock; reconcile; Podman strict check; Docker-tagged tests |
| A9 | Claude driver and spec block | A8 (`Driver`), P2 | `a2a` block parsed and validated in `internal/tools`; Claude headless driver mapping stream-json to task events; resume; version pin and unknown-event error; tests behind a build tag needing `claude` |
| A10 | Adapter server | A1, A8 | HTTP server on IPv4, card, send and stream, control connection with epoch handshake, outbound token check, generation check, departure acks, quarantine reporting; `serve` subcommand; tests with the fake driver |
| A11 | MCP client | A1, P3 | Stdio MCP server with four tools, liveness connection, blocking send with handle and actual state, explicit retry flag preserved across restart, CLI fallback; `mcp` subcommand; tests against a fake hub |
| A12 | Options | prototype bounds | All options in `options_def.go` with correct `AffectsSession`, `make generate`, `docs/configuration.md` rows, parse tests |
| A14 | Image install and serve-mode launch | A10 binary name | Install script and Dockerfile line; `entrypoint.sh` runs the adapter when role is worker; env wiring; image hash includes the binary |

A12 and A14 are small and can be given to one agent sequentially.

Merge order after Wave 2: A12, A3, A4, A7, A9, A10, A11, A14.

## Wave 3: integration (3 agents, then docs fan-out)

| ID | Package | Depends on | Exit criterion |
| --- | --- | --- | --- |
| A13 | CLI verbs and `ps` | A2, A7 | `a2a peers|log|send --detach|task get|cancel|retry|reconcile`, `stop --peer-group`, `ps --json` a2a object with tri-state fields from worker table and group record; schema bump; golden JSON |
| A15 | MCP wiring and skill | A11 | Settings template or patch adding the stdio server for orchestrator sessions only; provenance skill; fixed header format recorded in the proposal |
| I1 | App wiring | A7, A12, A13 | Join before session start, leave on stop, reconcile on start, stop, cleanup; rejections for unrestricted, qemu, non-strict Podman; host-principal invocations via engine exec |
| I2 | Smoke and integration | I1, A15 | Orchestrator plus two workers: send, cancel, orchestrator stop with acks, degraded path with a quarantined worker, reconcile, teardown with inventory deletion; Docker-tagged; Podman manual matrix recorded |

A13 and A15 run in parallel; I1 after A13; I2 after I1.

Docs fan-out after I2, one agent per file, all in parallel:

| ID | File |
| --- | --- |
| D1 | `docs/networking.md`: hub, rules, overlay, Podman matrix, plaintext risk |
| D2 | `docs/cli-reference.md`: options and verbs |
| D3 | `docs/ARCHITECTURE.md`: diagram, layout, container assets |
| D4 | `docs/runtime/stores.md`: context table, retention, inventory |
| D5 | `docs/extensions/README.md` and `adding-a-tool.md`: the `a2a` spec block |
| D6 | `docs/DEV.md`, `docs/security/`: option checklist, message-plane note |

Then the coordinator removes the experimental marker, runs
`make check-license-headers`, `make generate`, `make lint`, and writes the
changelog block.

## Critical path

W0 → A2 → A7 → I1 → I2 → docs. Everything else hangs off it. A7 is the
package to staff first and best in Wave 2; A8 and A10 together are the largest
volume and should start the moment Wave 0 merges (A8) and Wave 1 merges (A10).

## Agent brief template

Every package agent gets the same brief shape:

1. Package ID, owned files, forbidden files.
2. Links: proposal sections, interface document sections, spec pin.
3. Exit criterion verbatim from the table above.
4. Rules: worktree isolation, no bare stash, stdlib only, license headers,
   `gofmt`, `make lint-changed`, comment style from `CLAUDE.md`.
5. Report path and what to put in it: deviations from the interface document
   (as change requests, not edits), measured values, open items.
6. Stop conditions: an interface gap, a failing gate, or a needed edit outside
   owned files.

## Risks specific to parallel execution

- Interface drift. Mitigated by the frozen document and the change-request
  path; the coordinator reviews every report for silent deviations.
- Generated files. Only A12 runs `make generate`; everyone else leaves
  generated files alone, including when they add options-adjacent code.
- `entrypoint.sh` and `gateway-entrypoint.sh` are shell with many callers.
  A5 and A14 each own one; neither edits the other.
- Podman results arriving late. A7 and A13 implement the rejection path with
  the threshold as one constant (any isolate option, per CR-1); tightening
  it is a one-line change if P1-P contradicts the pre-P1 matrix.
- Merge order matters within a wave because stubs are replaced by bodies.
  The coordinator merges in the listed order and rebases the rest.
