# A2A interfaces (Wave 0, W0-B)

Frozen interface document for the parallel A2A implementation. The contract
is `proposal.html` (draft 6, revision 3, with CR-1 accepted); every item here
cites the proposal section it implements, as `P<n>`. The plan is
`implementation-plan.md`; package IDs (A1 to A15, I1, I2) refer to its
ownership table. After Wave 0 merges, nothing here changes except through the
change-request procedure at the end.

The compiling stubs live in `internal/a2a/`, `internal/peergroup/`,
`internal/a2aagent/`, `internal/backend/peergroup.go`,
`internal/tools/a2aspec.go` and `cmd/enclave-a2a-agent/`. Where this
document and a stub disagree, this document wins and the stub is a bug.

## Conventions

- **Spec-derived names follow `spec-pin.md`** (A2A v1.0.1, wire version
  `1.0`, JSON-RPC binding only). JSON-RPC method strings, card path, task
  state and role spellings, object field names, A2A error codes and the
  protocol version all live in `internal/a2a/wire.go`. There are no `kind`
  discriminators in 1.0; oneofs are discriminated by member name. Carriers
  for the custom representations of P4 are fields of `a2a.EnclaveMeta`
  under `metadata["https://enclave.eclipse.dev/a2a/ext/v1"]` (section 11).
- Everything else (files, env vars, labels, headers, paths, markers, error
  code numbers in the enclave range, exit codes) is enclave-owned and fixed
  by this document.
- Every JSON file carries `"schemaVersion": "1"`. Timestamps are RFC 3339 in
  UTC with second precision. Readers reject unknown `schemaVersion` values.
- Every host-written and hub-written file is replaced atomically: write a
  temp file in the same directory, fsync, rename. Directories that hold such
  files are bind-mounted as directories, never as single files (P6).
- **Generation** (member, group, host principal) and **hub epoch** are
  opaque strings of 16 lowercase hex characters from `crypto/rand` (P7).
  **Revision** is a decimal `uint64`, monotonic per group generation,
  starting at 1, never reused (a rollback publishes a new revision).
- **Group names** match `^[a-z0-9][a-z0-9-]{0,31}$`; invalid names are
  rejected, not sanitized, because they appear in paths, labels and container
  names. **Member names** are the sanitized session name
  (`model.SanitizeSessionName`). The host principal's member name is `_host`,
  which no sanitized session name can equal.
- **Tokens** are 32 bytes from `crypto/rand`, base64url without padding.
  **Token hashes** are `sha256:<lowercase hex>` of the token string.
  Comparison is constant time.
- `<state>` is `config.HostStateRootDir(home)` (for example
  `~/.local/state/enclave`); `<config store>` is
  `config.HostStoreConfigDir(home, tool, projectHash, key)`. The peergroup
  path helpers take these roots as arguments so that `internal/peergroup`
  does not import `internal/config` and stays light enough for the hub binary.

## 1. Snapshot layout (P6 "Revisioned snapshot publication", P16, P24 "Snapshot publication")

Owner: A2 (`peergroup.SnapshotPublisher`, `peergroup.LoadCurrent`).
Consumers: A3 (proxy reader), A4 (supervisor reader), A7 (hub mounts, join and
leave), I1.

```text
<state>/a2a/<group>/snapshots/            mounted read-only at /run/enclave-a2a/snapshots
  current -> 7                             relative symlink, swapped by rename
  6/                                       previous committed revision (retained)
  7/
    membership.json                        0644, host user
    secrets.json                           0400, owner HubProxyUID (65532)
    manifest.json                          0644, host user, written last
  .staging-8/                              in-progress publication, never read
```

Publication of revision N (`SnapshotPublisher.Publish`):

1. Create `.staging-N/`, write `membership.json` and `secrets.json`, make
   `secrets.json` 0400 and owned by the proxy uid through the
   `SecretsOwner` hook (the engine backend implements it, since the host
   user cannot chown on rootful engines), write `manifest.json` last.
2. Rename `.staging-N` to `N`.
3. Create a temporary symlink `.current-N -> N` and rename it over `current`.
4. Record N as `pendingRevision` in the group record (section 3), then nudge
   the hub (`PeerGroupBackend.ReloadHub`, SIGHUP). The nudge is never an
   acknowledgment; commit is decided by section 2.

Readers resolve `current`, read `manifest.json`, verify that both files hash
to the manifest values, that the directory name, the manifest, membership and
secrets all carry the same `revision`, and that `groupGeneration` matches the
hub's `ENCLAVE_A2A_GROUP_GENERATION`. Any mismatch: keep the last applied
revision and report the rejected one (section 2). The supervisor never loads
`secrets.json` contents beyond verifying the hash (it is root and can read
it); the proxy loads both.

Retention: after revision N commits,
`Prune(group, committed, previous Revision)` removes every revision
directory except N (`committed`), the last committed revision before it
(`previous`, skipped when 0) and the revision `current` points at, plus any
`.staging-*` and `.current-*` leftovers. The caller passes as `previous` the
committed revision it read from the group record before recording N; a
failed revision is never retained as the rollback target (CR-A2-1). On group creation the snapshot root
is emptied, so revision numbers never span group generations.

### membership.json

```json
{
  "schemaVersion": "1",
  "group": "review",
  "groupGeneration": "3f9c0a1b2c3d4e5f",
  "revision": 7,
  "hubPort": 41820,
  "members": [
    {
      "name": "worker-1",
      "role": "worker",
      "generation": "a1b2c3d4e5f60718",
      "projectPath": "/home/u/src/app",
      "mountMode": "rw",
      "network": "enclave-claude-ab12cd34ef56-worker-1-net",
      "subnet": "172.18.0.0/28",
      "hubAddress": "172.18.0.14",
      "gatewayAddress": "172.18.0.2",
      "adapterPort": 41821,
      "inboundTokenHash": "sha256:...",
      "description": "reviews diffs",
      "tool": "claude",
      "skills": [ { "id": "...", "name": "...", "description": "...", "tags": [] } ]
    }
  ],
  "hostPrincipal": {
    "name": "_host",
    "generation": "0011223344556677",
    "credentials": [ { "hash": "sha256:...", "expiresAt": "2026-11-05T12:15:00Z" } ]
  }
}
```

`gatewayAddress` is the member gateway's address on its bridge: the source the
hub INPUT rule admits on that interface (CR-1) and, for workers, the adapter
endpoint (the tool container shares the gateway's network namespace).
`adapterPort` is 0 for the orchestrator. `skills` are the agent card skills
from the tool's `a2a` block (P20); they use `a2a.AgentSkill` and are
shaped per `spec-pin.md`.

### secrets.json

```json
{
  "schemaVersion": "1",
  "group": "review",
  "groupGeneration": "3f9c0a1b2c3d4e5f",
  "revision": 7,
  "outbound": [ { "member": "worker-1", "generation": "a1b2c3d4e5f60718", "token": "..." } ]
}
```

The host keeps its own copy of the outbound tokens at
`<state>/a2a/<group>/host-secrets/outbound.json` (0600, same shape), because
the published file is proxy-owned and may be unreadable to the host user.

### manifest.json

```json
{
  "schemaVersion": "1",
  "group": "review",
  "groupGeneration": "3f9c0a1b2c3d4e5f",
  "revision": 7,
  "createdAt": "2026-11-05T12:00:00Z",
  "files": { "membership.json": "sha256:...", "secrets.json": "sha256:..." }
}
```

## 2. Status files (P6, P15 join and leave)

Writers: A4 (`supervisor.json`, root), A3 (`proxy.json`, proxy uid), both via
`peergroup.WriteStatus`. Reader and commit decision: A2
(`peergroup.StatusWaiter`). Consumers: A7, I1, A13 (`a2a peers`).

```text
<state>/a2a/<group>/status/               mounted read-write at /run/enclave-a2a/status
  supervisor.json
  proxy.json
```

The status directory is mode 1777 (sticky) so that each reader can replace
only its own file by rename; rootless engines map this as P1-P measures.

```json
{
  "schemaVersion": "1",
  "reader": "proxy",
  "groupGeneration": "3f9c0a1b2c3d4e5f",
  "epoch": "9a8b7c6d5e4f3021",
  "appliedRevision": 7,
  "rejectedRevision": 0,
  "error": "",
  "updatedAt": "2026-11-05T12:00:01Z"
}
```

- `appliedRevision`: last revision verified and fully applied (supervisor:
  rules installed and conntrack entries of removed endpoints flushed; proxy:
  routing switched, streams of revoked principals closed, revoked tokens
  refused). 0 before the first.
- `rejectedRevision`: last revision refused since the last clean load of
  `current`, else 0; a clean reload clears it and `error` (CR-F5a-1).
  `error` says why. Readers write the file on start (after loading
  `current`), on every applied or rejected revision, and never cache across
  restart (P6).
- Commit of revision N: both files show `appliedRevision >= N` with the
  expected `groupGeneration` within the ack timeout (bound
  `a2a_commit_timeout`). Either file showing `rejectedRevision == N` fails
  immediately with `ErrRevisionRefused`; a timeout is `ErrNotCommitted`.

## 3. Group record and cleanup inventory (P7, P15, P16)

Owner: A2 (`peergroup.GroupStore`, `peergroup.Inventory`). Consumers: A7
(join, leave, reconcile), A13 (`ps`, `a2a peers`, `reconcile`), I1.

```text
<state>/a2a/<group>/
  group.json                         group record
  inventory/<groupGeneration>.json   cleanup inventory, one file per group generation
  audit/audit.jsonl                  hub audit log (section 11)
  host-secrets/outbound.json         host copy of outbound tokens (0600)
  snapshots/  status/                sections 1 and 2
<state>/locks/a2a-<group>.lock       group lock (config.HostLockPath)
```

Every mutation of `group.json`, the inventory, snapshots, overlays, hub
attachments and host-secrets happens under the group lock (P15).

### group.json

```json
{
  "schemaVersion": "1",
  "group": "review",
  "groupGeneration": "3f9c0a1b2c3d4e5f",
  "createdAt": "2026-11-05T11:00:00Z",
  "hubContainer": "enclave-a2a-hub-review",
  "engine": { "name": "podman", "root": "/home/u/.local/share/containers/storage" },
  "hostPrincipal": { "name": "_host", "generation": "0011223344556677" },
  "committedRevision": 7,
  "pendingRevision": { "revision": 8, "purpose": "join", "member": "worker-2", "startedAt": "..." },
  "members": [
    {
      "name": "worker-1", "role": "worker", "generation": "a1b2c3d4e5f60718",
      "state": "active",
      "sessionContainer": "enclave-claude-ab12cd34ef56-worker-1",
      "tool": "claude", "projectHash": "ab12cd34ef56", "configStoreKey": "default",
      "projectPath": "/home/u/src/app", "mountMode": "rw",
      "network": "...", "subnet": "172.18.0.0/28",
      "hubAddress": "172.18.0.14", "gatewayAddress": "172.18.0.2", "adapterPort": 41821,
      "inboundTokenHash": "sha256:...", "description": "reviews diffs",
      "joinedAt": "...", "leaveIntentAt": ""
    }
  ],
  "departures": [
    {
      "member": "orch", "generation": "...", "kind": "authoritative", "state": "degraded",
      "observedAt": "...", "updatedAt": "...",
      "acks": [ { "worker": "worker-1", "workerGeneration": "...", "principal": { ... },
                  "admissionStopPersisted": true, "queuedDiscarded": ["t1"],
                  "runningTerminated": ["t2"], "termination": "unconfirmed", "at": "..." } ]
    }
  ],
  "obligations": [ { "kind": "departure-ack", "member": "orch", "generation": "...", "since": "...",
                     "lastAttemptAt": "...", "lastError": "worker-1 unreachable" } ],
  "releases": [ { "kind": "quarantine", "worker": "worker-1", "member": "worker-1",
                  "generation": "...", "releasedAt": "..." } ],
  "credentials": [ { "hash": "sha256:...", "issuedAt": "...", "expiresAt": "..." } ]
}
```

- `engine` (added by F5b, optional, `schemaVersion` stays `"1"`): the engine
  that runs the group, written when the record is created. `name` is
  `docker` or `podman`; `root` is the engine's storage root from its info
  (`DockerRootDir`, Podman `graphRoot`) and tells a rootless daemon from a
  rootful one; empty matches any root. Join, leave, reconcile and host
  credentials refuse a record of another engine or daemon (reconcile reports
  the group skipped), and `enclave a2a` verbs switch to the recorded engine
  unless `--backend` names another one. A record without `engine` (written
  before F5b) is unknown: it adopts the engine where its hub or a member
  session of its generations exists, and is otherwise never torn down, only
  reported.
- `members[].state`: `joining` (inventory written, not yet committed),
  `active`, `leaving` (leave intent recorded). Members are removed from the
  record when the leave completes; the inventory keeps their store.
- `pendingRevision` is null when nothing is in flight. Purposes: `join`,
  `leave`, `credential`, `rollback`.
- `departures[].kind`: `suspected` or `authoritative`; `state`: `pending`,
  `complete` (every worker acked with `termination: confirmed`), `degraded`.
- `obligations[].kind`: `snapshot-commit`, `departure-ack`,
  `quarantine-release`, `engine-disconnect`. Reconcile retries each until it
  succeeds, then removes it (P15).
- `releases[].kind`: `quarantine`, `admission-stop`, `departure-cleared`.
- `credentials` lists live host invocation credential hashes (P7); the
  credential itself is never stored. Expired entries are removed by a new
  revision at the next lifecycle command or reconcile.

### inventory/&lt;groupGeneration&gt;.json

```json
{
  "schemaVersion": "1",
  "group": "review",
  "groupGeneration": "3f9c0a1b2c3d4e5f",
  "entries": [
    {
      "member": "worker-1", "memberGeneration": "a1b2c3d4e5f60718",
      "tool": "claude", "projectHash": "ab12cd34ef56", "configStoreKey": "default",
      "storePath": "/home/u/.local/state/enclave/projects/ab12cd34ef56/claude/config-store/default/a2a/a1b2c3d4e5f60718",
      "state": "member", "writtenAt": "...", "eligibleAt": "", "lastError": "", "lastAttemptAt": ""
    }
  ]
}
```

- Written first in a join (`state: admitting`), before any token or
  attachment exists. Join rollback removes the entry (`Inventory.Remove`).
  Commit moves it to `member`; leave moves it to `eligible`.
- An entry is removed only after its store directory and the transcripts its
  `contexts.json` lists are deleted. The file is deleted when it has no
  entries. A new group generation gets a new file; old files are retried by
  reconcile and never touch another generation's store, because
  `storePath` contains the member generation (section 5).
- The group directory is removed when `group.json` is gone and no inventory
  file remains.

## 4. Gateway overlay (P8, P15, P24 "Gateway overlay delivery")

Type and writer helper: A2 (`peergroup.Overlay`, `peergroup.WriteOverlay`).
Writer in the lifecycle: A7. Gateway reader, rules and markers: A5. Reload
wait: A5 implements the marker matching in `internal/gateway`; A7 exposes it
as `PeerGroupBackend.ReloadGatewayOverlay`.

Host path: `<state>/projects/<hash>/<tool>/a2a-overlay/<session container>/overlay.json`.
The per-session directory is created before the gateway is created and
mounted read-only at `/etc/enclave-a2a-overlay` in the gateway (directory
mount, so atomic replacement is visible). Only sessions started with
`--peer-group` get the mount; other sessions are unchanged.

```json
{
  "schemaVersion": "1",
  "generation": 3,
  "active": true,
  "group": "review",
  "member": "worker-1",
  "hubAddress": "172.18.0.14",
  "hubPort": 41820,
  "adapterPort": 41821,
  "dnsName": "a2a.enclave"
}
```

- `generation` increases on every write for that session.
- `active: true` installs, in addition to the bundle rules: INPUT
  `-i <bridge if> -s <hubAddress> -p tcp --dport <adapterPort> -j ACCEPT`
  (omitted when `adapterPort` is 0), then INPUT `-s <hubAddress> -j DROP`
  (CR-P1D-2), both after the ESTABLISHED,RELATED accept and ahead of the
  published-port accepts, so the hub reaches only the adapter port and
  replies to member-opened connections; OUTPUT filter accept to
  `<hubAddress>:<hubPort>/tcp`, a nat OUTPUT `RETURN` for `<hubAddress>`
  before the REDIRECT rules, and a dnsmasq record `dnsName -> hubAddress`.
  No ip6tables exception (P8 IPv6 guarantee).
- `active: false` removes them; the host writes it at leave, waits for the
  cleared marker, then deletes the file. A missing file means no A2A rules
  and logs no marker.
- The gateway reads the file at start and on every SIGHUP reload, and logs
  exactly one of these lines per read, with the decimal generation (or the
  literal `unknown` when the file cannot be parsed):

```text
A2A overlay applied (generation=<n>)
A2A overlay cleared (generation=<n>)
A2A overlay validation failed (generation=<n>)
A2A overlay apply failed (generation=<n>)
```

- A validation or apply failure follows the existing reload failure path
  (fail-closed gateway shutdown) after logging its marker; the host treats
  either failure marker, or a gateway exit, as a failed step. The overlay
  marker is logged before the bundle's `Reload completed (generation=...)`.

Static hub address: the last usable host address of the session subnet (for
the default `/28`, `.14`), computed by A7 from `backend.SessionNetwork.Subnet`
and recorded in the group record. See open question Q4. When a container
already holds that address, A7 takes the next lower free one (never the
bridge gateway), so consumers must read `hubAddress` from the overlay or the
group record instead of deriving it from the subnet.

## 5. Context table (P9, P10, P13, P16)

Owner: A8 (`internal/a2aagent/contexttable`, implementing
`a2aagent.ContextTable`); file shapes are in `internal/a2aagent/table.go`.
Readers: A13 (`ps`, `a2a peers`), A7 and I1 (reconcile), A2 (inventory
deletion reads `contexts.json` transcripts).

```text
<config store>/a2a/<memberGeneration>/      in the container: <tool configDir>/a2a/<memberGeneration>/
  contexts.json        contexts, bindings, harness session IDs, last activity, tombstones
  tasks/<taskID>.json  one record per task: state, reason, termination, seq, result
  dedupe.json          dedupe records
  restrictions.json    admission stops, quarantine, heartbeat
  transcripts/         reserved for drivers that can place transcripts here
```

The directory is per member generation so that a rejoined worker never sees
the previous generation's contexts and inventory deletion is scoped by
construction (P16). The adapter is the only writer while it runs; host
writes happen only through `enclave-a2a-agent ctl` (section 11) or, when the
worker container is not running, under the config store lock.

Every file starts with the table header:

```json
{ "schemaVersion": "1", "group": "review", "groupGeneration": "...",
  "worker": "worker-1", "workerGeneration": "...", "updatedAt": "..." }
```

- `contexts.json`: header plus `contexts[]` (`contextId`, `principal`
  {`member`, `role`, `generation`}, `harnessSessionId`, `createdAt`,
  `lastActivityAt` (updated at enqueue), `transcripts[]` (config-store
  relative)) and `tombstones[]` (`contextId`, `principal`, `expiredAt`).
- `tasks/<taskID>.json`: header plus `taskId`, `contextId`, `principal`,
  `messageId`, `contentHash` (`sha256:` of the canonical request content),
  `seq` (admission order; the queue is `submitted` tasks by `seq`), `epoch`,
  `state` (A2A task state, `TASK_STATE_*` spelling), `reason`, `termination`,
  `pgid`, `admittedAt`, `startedAt`, `endedAt`, `deadlineAt`, `result`
  (final message, truncated to the result bound), `truncated`.
- `dedupe.json`: header plus `records[]` keyed by (`principal.member`,
  `principal.generation`, `messageId`), with `contentHash`, `taskId`,
  `admittedAt`.
- `restrictions.json`: header plus `admissionStops[]` (`principal`,
  `reason`: `suspected` or `authoritative`, `since`) and `quarantine` (null or
  {`since`, `taskId`, `pgid`, `reason`}). The adapter rewrites it at least
  every `ENCLAVE_A2A_HEARTBEAT_INTERVAL` (default 60 s); `updatedAt` is the
  heartbeat the host compares with the staleness bound.
- Admission (dedupe record plus task record) is persisted before `Admit`
  returns and before anything executes (P10, P23 P2).

## 6. Environment variables (P9, P15 "Start the session", P17)

Names are constants in `internal/a2a/enclave.go`. Writers: I1 and A14
(session env), A7 (hub env). Readers: A10 and A11 through
`a2aagent.LoadConfig`, A3 (hub). Durations use Go `time.ParseDuration`
syntax; sizes are bytes; counts are decimal.

Session containers (orchestrator and worker):

| Variable | Value | Roles |
| --- | --- | --- |
| `ENCLAVE_A2A_ROLE` | `orchestrator` or `worker` | both |
| `ENCLAVE_A2A_GROUP` | group name | both |
| `ENCLAVE_A2A_GROUP_GENERATION` | group generation | both |
| `ENCLAVE_A2A_MEMBER` | member name | both |
| `ENCLAVE_A2A_MEMBER_GENERATION` | member generation | both |
| `ENCLAVE_A2A_HUB_HOST` | `a2a.enclave` | both |
| `ENCLAVE_A2A_HUB_PORT` | `41820` | both |
| `ENCLAVE_A2A_INBOUND_TOKEN` | session-to-hub token | both |
| `ENCLAVE_A2A_OUTBOUND_TOKEN` | hub-to-worker token the adapter checks | worker |
| `ENCLAVE_A2A_ADAPTER_PORT` | `41821` | worker |
| `ENCLAVE_A2A_TABLE_DIR` | container path of section 5's directory | worker |
| `ENCLAVE_A2A_DESCRIPTION` | card description | worker |
| `ENCLAVE_A2A_QUEUE_DEPTH` | queue depth | worker |
| `ENCLAVE_A2A_TASK_DEADLINE` | execution deadline | worker |
| `ENCLAVE_A2A_SEND_WAIT` | blocking send wait | orchestrator |
| `ENCLAVE_A2A_MAX_REQUEST_BYTES` | request size bound | both |
| `ENCLAVE_A2A_MAX_RESULT_BYTES` | result size bound | worker |
| `ENCLAVE_A2A_RECORD_LIMIT` | dedupe, task and tombstone count bound | worker |
| `ENCLAVE_A2A_RECORD_MAX_AGE` | same, age bound | worker |
| `ENCLAVE_A2A_RECONNECT_WINDOW` | reconnect window | both |
| `ENCLAVE_A2A_CONTEXT_TTL` | context idle TTL | worker |
| `ENCLAVE_A2A_TERMINATION_TIMEOUT` | termination confirmation timeout | worker |
| `ENCLAVE_A2A_HEARTBEAT_INTERVAL` | restrictions.json heartbeat | worker |
| `ENCLAVE_A2A_HARNESS` | the tool's `sandbox.a2a` block as JSON plus `"yoloFlag"` (the profile's `sandbox.yoloFlag`) | worker |
| `ENCLAVE_YOLO` | existing session yolo switch; with `1` the adapter appends `yoloFlag` after the harness command | worker |

Hub container:

| Variable | Value |
| --- | --- |
| `ENCLAVE_A2A_GROUP`, `ENCLAVE_A2A_GROUP_GENERATION`, `ENCLAVE_A2A_HUB_PORT`, `ENCLAVE_A2A_RECONNECT_WINDOW` | as above |
| `ENCLAVE_A2A_LOG` | `coarse` or `full` |
| `ENCLAVE_A2A_HUB_RETAINED_RESULTS` | retained terminal summaries per worker |
| `ENCLAVE_A2A_PROXY_UID` | `65532` |
| `ENCLAVE_A2A_HUB_SNAPSHOT_DIR` | `/run/enclave-a2a/snapshots` |
| `ENCLAVE_A2A_HUB_STATUS_DIR` | `/run/enclave-a2a/status` |
| `ENCLAVE_A2A_HUB_AUDIT_DIR` | `/var/log/enclave-a2a` |

Rules: tokens never enter session provenance (P18; the options carrying them
do not exist, the env is injected by I1 directly). The executor removes every
`ENCLAVE_A2A_*` variable from the harness environment before spawning it
(see Q1 for why this is not sufficient on its own). The fixed ports
`peergroup.HubPort` and `peergroup.AdapterPort` are carried in env, snapshot
and overlay so they can change without format changes.

## 7. Labels (P8, P15 teardown, P7 reconciliation)

Constants in `internal/peergroup/labels.go`. Set by A7 (hub) and I1 or A7
(sessions). Read by A7 (teardown, GC, reconcile), A13 (`ps`), I1.

| Label | On | Value |
| --- | --- | --- |
| `enclave.a2a.hub` | hub container | `true` |
| `enclave.a2a.group` | hub, member session containers | group name |
| `enclave.a2a.group_generation` | hub, member session containers | group generation |
| `enclave.a2a.member` | member session containers | member name |
| `enclave.a2a.member_generation` | member session containers | member generation |
| `enclave.a2a.role` | member session containers | `orchestrator` or `worker` |

`sessionOwnsEndpoints` treats an endpoint whose container carries
`enclave.a2a.hub=true` as owned (A7). Hub container name:
`enclave-a2a-hub-<group>`. `enclave.a2a.member_generation` is an addition to
the plan's list (deviation D1).

## 8. Go APIs

All stub bodies are `panic("not implemented")`. Nothing is wired into
existing code paths.

### `internal/a2a` (owner A1; consumers A3, A8, A10, A11, A13, A2, A7)

Standard library only; imported by the host and by both container binaries.

- `wire.go`: every spec-derived name, per `spec-pin.md`:
  `ProtocolVersion`, `ProtocolBinding`, `CardPath`, `HeaderVersion`,
  `HeaderExtensions`, `ExtensionURI`, `Method*`, `TaskState*`,
  `RoleUser`/`RoleAgent`, A2A error codes `Code*` (-32001 to -32009),
  `EnclaveMeta`, `CardExtensionParams`, and the object types `Part`,
  `Message`, `TaskStatus`, `Artifact`, `Task`, `TaskStatusUpdateEvent`,
  `TaskArtifactUpdateEvent`, `SendMessageConfiguration`,
  `SendMessageRequest`, `GetTaskRequest`, `CancelTaskRequest`,
  `SubscribeToTaskRequest`, `AgentInterface`, `AgentExtension`,
  `AgentCapabilities`, `AgentSkill`, `SecurityScheme`,
  `SecurityRequirement`, `AgentCard`.
- `jsonrpc.go`: `Request`, `Response`, `Error`, `ErrorInfo`, standard
  JSON-RPC codes, enclave codes (section 11), `Result` with
  `DecodeResult`/`EncodeResult` for the member-discriminated oneof.
- `sse.go`: `Event`, `SSEWriter` (flush per event, no buffering, P6),
  `SSEReader`.
- `handle.go`: `TaskHandle`, `ContextHandle`, `String`, `ParseTaskHandle`,
  `ParseContextHandle` (section 11).
- `enclave.go`: `Role`, `Principal`, headers, hub paths, control events,
  `PeerListing`, `Termination`, `EndingReason`, delivery reasons,
  `ProvenanceFormat`, env names, `CtlRequest`, `DepartureAck`, `CtlResponse`.

### `internal/peergroup` (owner A2; consumers A3, A4, A7, A13, I1)

- `GroupStore` (`Lock`, `Load`, `Create`, `Save`, `Delete`, `List`),
  `NewGroupStore(stateRoot, locksDir)`.
- `SnapshotPublisher` (`Publish`, `Current`, `Prune`),
  `NewSnapshotPublisher(stateRoot, SecretsOwner)`, and `LoadCurrent` for hub
  readers.
- `StatusWaiter` (`WaitCommitted`, `Read`), `NewStatusWaiter`, `WriteStatus`.
- `Inventory` (`Add`, `SetState`, `Remove`, `Load`, `Generations`,
  `DeleteEligible`), `NewInventory`.
- `Tokens` (`NewMember`, `NewInvocation`, `Hash`, `Verify`,
  `LoadOutbound`, `SaveOutbound`), `NewTokens`.
- `WorktreeGuard` (`Check`), `NewWorktreeGuard`.
- File types of sections 1 to 4, `Overlay`, `WriteOverlay`, marker formats,
  labels, path helpers, `NewGeneration`, `ValidateGroupName`, constants
  (`HubPort`, `AdapterPort`, `HubProxyUID`, hub mount paths, `HostMemberName`).

`internal/peergroup` must not import `internal/config`; the hub binary links
it. A2 may add `config.HostA2A*` helpers in `host_paths.go` that call these.

### `internal/backend` (owner A7 for the Docker implementation; interface here)

`PeerGroupBackend` in `internal/backend/peergroup.go`:

```go
StartHub(ctx, HubSpec) (HubRef, error)
StopHub(ctx, HubRef) error
ReloadHub(ctx, HubRef) error
ConnectHub(ctx, HubRef, HubAttachment) error
DisconnectHub(ctx, HubRef, network string) error
SignalWorker(ctx, SessionRef, a2a.CtlRequest) (WorkerSignalResult, error)
ReloadGatewayOverlay(ctx, SessionRef, generation uint64) error
ListHubs(ctx) ([]HubInfo, error)
```

`ReloadGatewayOverlay` and `ListHubs` are additions to the plan's six
methods (deviation D2). `PeerGroupSession` describes what a member session's
start needs; adding it to `backend.Request` is change request CR-W0B-1.
Consumers: I1, A13 (`stop --peer-group`, `reconcile`). A6 provides the
engine primitives A7 builds on.

### `internal/a2aagent` (interfaces here; implementations in subpackages)

- `Driver` (`Name`, `CheckVersion`, `NewSessionID`, `Command`, `Decode`)
  with `Invocation`, `HarnessEvent`, `Outcome`. A driver never starts or
  kills a process. For a new context the executor calls `NewSessionID`,
  binds a non-empty result in the context table before spawn and passes it
  as `Invocation.NewHarnessSessionID` (CR-A9-1); until the harness reports
  that session, a later turn creates it again instead of resuming.
  Implemented by A9 in `driver/claude`; consumed by A8 and A10.
- `ContextTable` and the table file types (section 5). Implemented by A8 in
  `contexttable`; consumed by A10, A13.
- `Executor` (`Run`, `Subscribe`, `Cancel`, `EpochChanged`, `Depart`,
  `DescendantsAlive`). Implemented by A8 in `executor`; consumed by A10.
- `Server` (`Serve`). Implemented by A10 in `server`.
- `MCPClient` (`Serve`) and the four tool names. Implemented by A11 in `mcp`.
- `Config` and `LoadConfig` (section 6). Implemented by A10; consumed by A11.

### `internal/tools` (owner A9)

`A2ASpec` and `A2ASkillSpec` in `internal/tools/a2aspec.go`: `command`
(argv, prompt on stdin), `resumeArgs` (with `{sessionId}`), `eventFormat`
(driver name, `claude-stream-json` for v1), `versionCommand`, `versions`,
`skills`. Struct only; parsing is A9 (see CR-W0B-2).

### `cmd/enclave-a2a-agent` (main.go owned by W0-B)

`main.go` dispatches `serve` (`runServe`, `serve.go`, A10), `ctl` (`runCtl`,
`ctl.go`, A10), `mcp` (`runMCP`, `mcp.go`, A11), `client` (`runClient`,
`client.go`, A11). Exit 2 is a usage error. The four subcommand files exist
as one-function stubs so the package compiles; `ctl` and `client` are
additions to the plan (deviation D3).

## 9. `ps --json` a2a object (P20, P24 "ps --json status")

Owner: A13. Doc only in Wave 0; `command_ps.go` is unchanged.

Each `ps --json` entry gains `a2a`: `null` for sessions without the
`enclave.a2a.group` label, else:

```json
"a2a": {
  "group": "review",
  "groupGeneration": "3f9c0a1b2c3d4e5f",
  "role": "worker",
  "generation": "a1b2c3d4e5f60718",
  "port": 41821,
  "hub": { "container": "enclave-a2a-hub-review", "address": "172.18.0.14", "port": 41820 },
  "snapshotRevision": 7,
  "quarantine": "no",
  "quarantineAt": "2026-11-05T12:00:00Z",
  "quarantineSource": "worker-table",
  "admissionStop": "unknown",
  "admissionStopAt": null,
  "admissionStopSource": null,
  "departure": "no",
  "departureAt": "2026-11-05T12:00:00Z",
  "departureSource": "group-record"
}
```

- `group`, `groupGeneration`, `role`, `generation` come from labels;
  `port` is the adapter port (null for orchestrators); `hub` and
  `snapshotRevision` (`committedRevision`) come from the group record, null
  when it cannot be read.
- Tri-state fields are strings `yes`, `no`, `unknown`. Each has `<field>At`
  (RFC 3339 of the source record, null when unknown) and `<field>Source`
  (`worker-table`, `group-record`, or null when unknown). Absence of data is
  `unknown`, never `no`. A worker-table record whose `updatedAt` is older than
  `a2a_status_staleness` is `unknown`.
- `quarantine`: worker: `restrictions.json` `quarantine` non-null, source
  `worker-table`. Orchestrator: `no`, source `group-record` (orchestrators run
  no adapter; see Q6).
- `admissionStop`: worker: `yes` if `admissionStops` is non-empty.
  Orchestrator: `yes` if any active worker's table holds an admission stop
  for this principal and generation, `unknown` if any worker table is
  unreadable or stale and none says `yes`; source `worker-table`.
- `departure`: `yes` if the group record has a departure for this member and
  generation that is not `complete`, or an open `departure-ack` obligation;
  source `group-record`.
- The array shape of `ps --json` is unchanged; `a2a` is an additive field.
  See Q5 for the "schema update".

## 10. CLI surface (P20, P15, P8)

Owners: A12 (options), A13 (verbs), I1 (run-path rejections). Doc only in
Wave 0; no `options_def.go` change.

### Options

| Option (config key) | Flag | Kind | AffectsSession | Env (section 6) |
| --- | --- | --- | --- | --- |
| `peer_group` | `--peer-group <name>` | string | affected | `ENCLAVE_A2A_GROUP` |
| `a2a_role` | `--role orchestrator\|worker` (default `worker`) | string | affected | `ENCLAVE_A2A_ROLE` |
| `a2a_description` | `--a2a-description <text>` | string | affected | `ENCLAVE_A2A_DESCRIPTION` |
| `a2a_log` | `--a2a-log coarse\|full` (default `coarse`) | string | unaffected (hub) | `ENCLAVE_A2A_LOG` |
| `a2a_context_ttl` | `--a2a-context-ttl <duration>` (default `24h`) | string | affected | `ENCLAVE_A2A_CONTEXT_TTL` |
| `a2a_queue_depth` | none | string | affected | `ENCLAVE_A2A_QUEUE_DEPTH` |
| `a2a_task_deadline` | none | string | affected | `ENCLAVE_A2A_TASK_DEADLINE` |
| `a2a_send_wait` | none | string | affected | `ENCLAVE_A2A_SEND_WAIT` |
| `a2a_max_request_size` | none | string | affected | `ENCLAVE_A2A_MAX_REQUEST_BYTES` |
| `a2a_max_result_size` | none | string | affected | `ENCLAVE_A2A_MAX_RESULT_BYTES` |
| `a2a_hub_retained_results` | none | string | unaffected (hub) | `ENCLAVE_A2A_HUB_RETAINED_RESULTS` |
| `a2a_record_limit` | none | string | affected | `ENCLAVE_A2A_RECORD_LIMIT` |
| `a2a_record_max_age` | none | string | affected | `ENCLAVE_A2A_RECORD_MAX_AGE` |
| `a2a_reconnect_window` | none | string | affected | `ENCLAVE_A2A_RECONNECT_WINDOW` |
| `a2a_commit_timeout` | none | string | unaffected (host) | none |
| `a2a_departure_ack_timeout` | none | string | unaffected (host) | none |
| `a2a_credential_lifetime` | none | string | unaffected (host) | none |
| `a2a_status_staleness` | none | string | unaffected (host) | none |
| `a2a_termination_timeout` | none | string | affected | `ENCLAVE_A2A_TERMINATION_TIMEOUT` |

Defaults are the P17 placeholders. Bound options are config-only in v1 (one
option per bounds row, as P20 asks); A12 may add flags through a change
request. `--role` keeps the proposal's flag name with the namespaced config
key `a2a_role`. Hub-level options (`a2a_log`, `a2a_hub_retained_results`)
take effect from the member that creates the group; a later member with a
different value gets a warning (see Q7). No option carries a token.

Run-path rejections (I1): `--peer-group` with `--allow-all-network`, with the
`qemu` backend, and on Podman whose netavark accepts no isolate option (CR-1;
one constant). `--role` or other `a2a_*` session options without
`--peer-group` are a usage error. `--peer-group` without `--name` is a usage
error (see Q3).

### Verbs

| Verb | Flags | Notes |
| --- | --- | --- |
| `enclave a2a peers` | `--peer-group <name>`, `--json` | members, cards, tri-state status as in section 9 |
| `enclave a2a log` | `--peer-group <name>`, `--tail <n>` | prints `audit.jsonl` lines |
| `enclave a2a send <peer> <text>` | `--peer-group <name>`, `--detach`, `--context <handle>`, `--json` | host principal; `<text>` `-` reads stdin |
| `enclave a2a task get <handle>` | `--peer-group <name>`, `--json` | fresh invocation credential |
| `enclave a2a task cancel <handle>` | `--peer-group <name>`, `--json` | |
| `enclave a2a task retry <handle>` | `--peer-group <name>`, `--json` | flagged retry with the original message ID |
| `enclave a2a reconcile` | `--peer-group <name>` (all groups if omitted) | |
| `enclave stop --peer-group <name>` | existing stop flags | every member, then the hub; no prompt |

`--peer-group` on `a2a` verbs may be omitted when exactly one group exists.
`--json` output of `a2a peers`, `send` and `task` is a new integration
contract: an object with `schemaVersion: "1"` and `kind` (`a2a-peers`,
`a2a-task`); A13 specifies the fields with golden tests and records them in
`docs/cli-reference.md` (D2).

### Exit codes

| Code | Meaning |
| --- | --- |
| 0 | success; for `send` and `task get`, the task completed |
| 1 | error: usage, rejection, delivery error, lock or engine failure |
| 3 | degraded: leave, stop or reconcile finished with an open obligation (unconfirmed termination, unreachable worker, uncommitted removal) |
| 4 | `send` wait bound elapsed: a handle with the actual state was printed; the task continues |
| 5 | the task reached a terminal state other than completed (`failed`, `canceled`, `rejected`) |
| 130 | interrupted (existing convention) |

`--detach` exits 0 once the task is admitted and prints its handle.

## 11. Wire contract (supporting items 6 and 8)

Owners: A1 (constants), A3 (hub), A10 (adapter), A11 (client), A13 (host
verbs).

**Hub HTTP** (IPv4, port `ENCLAVE_A2A_HUB_PORT`, host `a2a.enclave`):

| Path | Method | Caller | Purpose |
| --- | --- | --- | --- |
| `/peers/<name>` | POST | orchestrator client, host | JSON-RPC to the named worker, SSE passthrough |
| `/peers/<name>` + `CardPath` | GET | orchestrator client, host | worker card with hub-facing URL (P4) |
| `/_enclave/peers` | GET | orchestrator client, host | `a2a.PeerListing` |
| `/_enclave/control` | GET (SSE) | worker adapter | control connection (P9) |
| `/_enclave/liveness` | GET (SSE) | orchestrator client | liveness connection (P13) |

**Adapter HTTP** (IPv4, port `ENCLAVE_A2A_ADAPTER_PORT`): `POST /` JSON-RPC,
`GET CardPath` card.

**Authentication.** Every hub request carries
`Authorization: Bearer <inbound token or invocation credential>`. The hub
strips `Authorization` and every `X-Enclave-A2A-*` header from the request,
then forwards to the adapter with `Authorization: Bearer <outbound token>`
and `X-Enclave-A2A-Group`, `-Group-Generation`, `-Member`, `-Role`,
`-Generation`, `-Epoch`, `-Revision` describing the authenticated principal.
Bodies and IDs are never rewritten (P6). The adapter rejects a request
without its outbound token or with a principal generation unknown to it.

**Control stream.** First event `hello` (`a2a.ControlHello`: epoch, revision,
group generation, `principals`), then `orchestrator` events
(`a2a.OrchestratorState`) on every change of the orchestrator liveness
connection, a `membership` event (`a2a.ControlMembership`: revision,
`principals`) for every revision the hub applies after `hello`, and
keepalive comments every 15 s. `principals` lists every current principal of
that revision as `a2a.MemberPrincipal` (`member`, `role`, `generation`):
all members sorted by name, then the host principal `_host` with role
`host` (CR-A10-4). The liveness stream sends `hello` and keepalives only. A
missing keepalive for 2 intervals counts as connection loss.

The adapter answers `-31017` for a principal whose member, role and
generation are not in the newest membership it holds. A request forwarded
under a revision (`X-Enclave-A2A-Revision`) newer than that membership waits
up to the epoch wait (30 s) for it, since the event and the forwarded
request race; a request that arrives before any `hello` gets `-31019`. A
membership older than the one held is ignored; every `hello` replaces it.

**Host access.** `enclave a2a` verbs reach the hub through engine exec (P19):
`enclave-a2a-hub call <path>` inside the hub container, reading the
invocation credential as the first stdin line and the request body after it,
writing the response body (JSON or SSE) to stdout, and connecting to the
proxy on loopback. Worker signals use engine exec of
`enclave-a2a-agent ctl` in the worker: an `a2a.CtlRequest` JSON on stdin, an
`a2a.CtlResponse` on stdout (ops `depart`, `release-admission`,
`release-quarantine`, `status`). Neither puts a secret on argv.

**Protocol rules** (coordinator decisions on the open items of `spec-pin.md`):

- Every request carries `A2A-Version: 1.0` and `A2A-Extensions:
  <ExtensionURI>`. Adapter and hub answer a missing or other version with
  `-32009` and a missing extension with `-32008`. The card declares the
  extension with `required: true`; only enclave clients exist.
- The client sends with `SendStreamingMessage` (the first event carries the
  task, so a dropped wait still has a task ID) and falls back to `GetTask`.
  Dropping a stream never cancels. `SubscribeToTask` on a terminal task is
  `-32004`; callers then use `GetTask`, so `Task.metadata` carries the same
  termination fields as the terminal status event.
- Per-peer card URLs, not `tenant`: the hub serves
  `/peers/<name>/.well-known/agent-card.json` with
  `supportedInterfaces[0] = {url: "http://a2a.enclave:<port>/peers/<name>",
  protocolBinding: "JSONRPC", protocolVersion: "1.0"}` (P4, spec 8.2 direct
  configuration). Cards are served with `Cache-Control: max-age=0`.
- Cards declare `securitySchemes: {"bearer": {"httpAuthSecurityScheme":
  {"scheme": "Bearer"}}}` and the matching `securityRequirements`.
- Every JSON-RPC error, including hub errors, is HTTP 200 with the error
  body. An error after a stream started is one final SSE `data:` event
  holding a JSON-RPC error response with the request `id`, then close.
- `TASK_STATE_REJECTED` is never emitted in v1. Refusals before admission
  create no task and are JSON-RPC errors.
- `ListTasks`, `GetExtendedAgentCard` and the push-config methods
  (`a2a.Method*TaskPushNotificationConfig*`) answer `-32004`, `-32007` and
  `-32003` respectively.
- Known non-conformances, accepted for v1: plaintext HTTP (spec 7.1 MUST,
  P18 transport) and no `ListTasks` (spec 3.1 core operation).

**Errors.** Hub delivery errors and adapter refusals are JSON-RPC errors in
`-31000..-31099`, outside both the JSON-RPC reserved range and the A2A range.
`data` is one `google.rpc.ErrorInfo`: `[{"@type":
"type.googleapis.com/google.rpc.ErrorInfo", "domain": "enclave.eclipse.dev",
"reason": "<UPPER_SNAKE>", "metadata": {"member": "..."}}]`. Unknown tasks,
including tasks bound to another principal, are `-32001`; "context expired"
is only answered to the binding's own principal, otherwise "context unknown".
A2A-range codes (-32001 to -32009) are sent as a plain `a2a.Error` without
`data` in v1; `a2a.NewError` (domain `enclave.eclipse.dev`) is only for the
enclave range. A JSON-RPC `-32602` message is fixed text, never the Go
decoder's error.

| Code | Name | Raised by |
| --- | --- | --- |
| -31001 | adapter unreachable | hub |
| -31002 | unknown member | hub |
| -31003 | revoked principal | hub |
| -31004 | unauthenticated | hub |
| -31005 | role forbidden (worker sending, target not a worker) | hub |
| -31010 | context unknown | adapter |
| -31011 | context expired (tombstone) | adapter |
| -31012 | message ID reused with different content | adapter |
| -31013 | retry ambiguous (original evicted or foreign generation) | adapter |
| -31014 | admission stopped for this principal | adapter |
| -31015 | worker quarantined | adapter |
| -31016 | queue full | adapter |
| -31017 | principal generation unknown | adapter |
| -31018 | request too large | hub, adapter |
| -31019 | epoch unconfirmed (nothing admitted; safe to resend unchanged) | adapter |

ErrorInfo reasons are constants in `internal/a2a`: `Delivery*` for the hub
codes and `-31018` (`ADAPTER_UNREACHABLE`, `UNKNOWN_MEMBER`, `REVOKED`,
`UNAUTHENTICATED`, `ROLE_FORBIDDEN`, `REQUEST_TOO_LARGE`), `Refusal*` for
the adapter codes (`CONTEXT_UNKNOWN`, `CONTEXT_EXPIRED`,
`MESSAGE_ID_CONFLICT`, `MESSAGE_ID_REUSED`, `RETRY_AMBIGUOUS`,
`ADMISSION_STOPPED`, `QUARANTINED`, `QUEUE_FULL`, `GENERATION_UNKNOWN`,
`EPOCH_UNCONFIRMED`). `-31019` answers a send whose epoch the control stream
has not confirmed within the adapter's epoch wait (30 s), and any request
before the first `hello`; nothing was admitted, so clients resend unchanged
and without `retry` (a flagged retry of an unknown message ID is `-31013`).

**Metadata** (`a2a.EnclaveMeta` under `metadata[ExtensionURI]`, carriers per
`spec-pin.md`): `termination` and `reason` on `Task.metadata` and the
terminal `TaskStatusUpdateEvent.metadata` (`TaskStatus` has no metadata);
`queuePosition` on task metadata while submitted; `retry` on
`Message.metadata` (the client message ID is `Message.messageId`);
`principal` on the message as stored by the adapter, never accepted from a
client (`-32602`). `truncated: true` rides with `termination` when the
result exceeded `ENCLAVE_A2A_MAX_RESULT_BYTES`; the result artifact then
ends with `a2a.TruncatedMarker`, `"\n\n[enclave: result truncated]"`, which
counts toward the bound. A completed task always carries the `result`
artifact with one text part, empty when the harness replied with nothing.
Queue depth is `params.queueDepth` of the extension entry
in `capabilities.extensions`, since the card has no metadata. "Cancel,
outcome unknown" (P12) is `reason: "cancel"` with `termination:
"unconfirmed"`; `reason: "outcome-unknown"` is reserved for the adapter
restart row.

**Handles** (P7, P10): `task:<worker>:<workerGeneration>:<taskID>` and
`ctx:<worker>:<workerGeneration>:<contextID>`. The adapter issues context
IDs with at least 128 random bits; that is the handle's random part. The ID
is last so it may contain any character. Handles authorize nothing.

**Provenance line** (P7, final, settled by A10 and A15; `a2a.ProvenanceFormat`):
`[a2a group=<group> from=<member> role=<role> generation=<generation> task=<taskID> context=<contextID>]`,
then `\n\n`, then the message text parts joined with `\n`. Fields in that
fixed order, single spaces, no quoting. Values come from the authenticated
principal and the adapter, never from the request: `group` is the group name,
`from` the sender's member name (`_host` for the host principal), `role`
`orchestrator` or `host`, `generation` the sender's member generation (16 hex
characters), `task` and `context` the worker's raw task and context IDs (not
`task:`/`ctx:` handles). No value contains whitespace or `]`. Only the first
line of the prompt is the header; the shipped skill
(`extensions/tools/claude/skills/a2a-provenance/`) explains it.

**Audit** (`audit/audit.jsonl`, A3 writes, A13 reads): one JSON object per
line with `ts`, `group`, `epoch`, `revision`, `principal` (member),
`role`, `generation`, `target`, `method`, `taskId`, `contextId`, `outcome`
(JSON-RPC error code or 0), `reason`, `termination`. With `a2a_log=full` an
additional `content` member carries the request and response text. Nothing
else is ever logged (P6).

**Hub binary** (`cmd/enclave-a2a-hub`, owner A3; caller of `render-rules`
A4, caller of `call` A13 through engine exec). Subcommands:

- `enclave-a2a-hub proxy`: the unprivileged proxy; reads its configuration
  from the `ENCLAVE_A2A_HUB_*` environment (section 6), installs its SIGHUP
  handler before the first status write, reloads `current` on SIGHUP.
- `enclave-a2a-hub render-rules [--snapshot-dir <dir>]`: loads and verifies
  `current` exactly like the proxy, prints `iptables-restore` input for the
  hub's filter table (INPUT, OUTPUT, FORWARD) on stdout and the applied
  revision on stderr as `revision=<n>`, and exits non-zero with the reason
  on stderr when the snapshot is refused. Interface names are resolved from
  addresses at render time. It never runs iptables itself.
- `enclave-a2a-hub call <path>`: host access (see Host access above).

**Hub image** (owner A4): `internal/gateway` exports the image build and
lookup the way the gateway image does; A4 names the function in its report
and I1 wires it into `backend.HubSpec.Image`.

## Open questions and gaps

- **Q1. Worker harness can read adapter secrets and edit its own table.**
  The adapter and the headless harness run as the same container user, so the
  harness can read the adapter's environment from `/proc` and can write the
  config store, where the context table lives (P9 puts it there). A harness
  could then forge requests to its own adapter with any principal headers, or
  clear its own quarantine and admission stops. Removing `ENCLAVE_A2A_*` from
  the harness env does not prevent this. Options: run the harness under a
  separate uid, keep the table outside the harness-writable mount, or accept
  and document it. Decision (maintainer): accept and document; see CR-W0B-3.
- **Q2. Handle generation.** P7 says a handle carries "member generation"
  without saying whose. This document uses the worker's (issuer's)
  generation; the principal's generation is checked against the binding
  anyway. Decision (coordinator): accepted.
- **Q3. Member name for unnamed sessions.** P5 requires unique names within
  a group but not where the name comes from. This document requires `--name`
  with `--peer-group`. Decision (coordinator): accepted.
- **Q4. Static hub address under default IPAM.** Session networks fall back to
  default IPAM when the `/28` request fails; Docker refuses `--ip` on networks
  without a user-configured subnet. P1-D must confirm, and A7 must reject
  `--peer-group` on such a network. Decision (coordinator): A6 did not
  reproduce this; both engines accept a static `--ip` inside an
  engine-allocated subnet and refuse it only on Docker's default `bridge`.
  A7 rejects `--peer-group` only when `docker.IsStaticAddressRejected` matches.
- **Q5. `ps --json` schema update.** P20 says the a2a object "ships with the
  schema update", but `ps --json` is a bare array with no `schemaVersion`.
  This document adds `a2a` as an additive field and does not wrap the array;
  a wrapper would break every consumer. Decision (coordinator): accepted.
- **Q6. Tri-state fields per role.** P20 lists `quarantine`, `admissionStop`
  and `departure` for every session; this document defines their meaning per
  role (section 9). Orchestrator `quarantine` is `no` from the group record.
  Decision (coordinator): accepted.
- **Q7. Group-level options.** `a2a_log` and the hub's retained-results bound
  are per group, but options are per session. This document takes them from
  the member that creates the group and warns on mismatch. Decision
  (coordinator): accepted.
- **Q8. Spec parser location.** spec.yaml is parsed in `internal/config`
  (`spec.go`, `specSandbox`), not `internal/tools`. A9 needs an edit to
  `internal/config/spec.go` and the model projection to carry the `a2a`
  block; see CR-W0B-2 (accepted).
- **Q9. Hub INPUT and loopback.** `enclave-a2a-hub call` connects on loopback;
  A4's INPUT policy must accept `lo`, which P6 does not mention. Decision
  (coordinator): accepted; loopback is inside the hub's own namespace.
- **Q10. Hub supervisor reading snapshots.** A4's supervisor is shell in the
  gateway pattern; verifying manifest hashes and rendering rules from JSON
  is easier in Go. Suggest A3's binary offer a `render-rules` subcommand that
  A4's entrypoint calls; A3 and A4 to agree through a CR. Decision
  (coordinator): accepted; A3 owns `render-rules`, A4 calls it.

## Deviations from the plan's ten items

- **D1.** Sixth label `enclave.a2a.member_generation`, needed by reconcile to
  compare a container's generation with the group record (P7, P14).
- **D2.** `PeerGroupBackend` gains `ReloadGatewayOverlay` (join step "write
  overlay and reload the gateway", P15) and `ListHubs` (reconcile, P15).
- **D3.** `enclave-a2a-agent` gains `ctl` (host signaling by engine exec, P19,
  P24) and `client` (CLI fallback, P9) next to `serve` and `mcp`.
- **D4.** The context table lives in `<config store>/a2a/<memberGeneration>/`
  instead of `<config store>/a2a/`, so inventory deletion is generation
  scoped by construction (P16).
- **D5.** Section 11 (wire contract) is not one of the ten items but is
  shared between A3, A10, A11 and A13 and had to be frozen here.

## Change requests

Packages file change requests here; the coordinator decides and records the
decision. Until accepted, a package builds against the current text and notes
the CR in its report. Format: `CR-<package>-<n>`, date, requester, section,
proposed change, reason, decision.

- **CR-W0B-1** (2026-11-05, W0-B, section 8 `internal/backend`): add a
  `PeerGroup *PeerGroupSession` field to `backend.Request` so the Docker
  backend can mount the overlay directory into the gateway and set labels and
  env at session start. Decision (coordinator): accepted; A7 owns the
  additive `types.go` change.
- **CR-W0B-2** (2026-11-05, W0-B, section 8 `internal/tools`): extend A9's
  ownership to `internal/config/spec.go` (an `A2A *tools.A2ASpec` field on
  `specSandbox`) and the model projection, since spec parsing lives in
  `internal/config`. Decision (coordinator): accepted.
- **CR-W0B-3** (2026-11-05, W0-B, section 5 and Q1): run the adapter as a
  dedicated `a2a` uid owning the table directory 0700, so the worker's own
  harness cannot read adapter tokens or rewrite quarantine and admission
  stops. Affects A8, A10, A14 and the executor. Decision (maintainer,
  2026-11-05): rejected. Accept and document instead: a worker is trusted
  about itself and its table is advisory for its own state (P18 "Worker
  self-integrity"). No uid change.
- **CR-A6-1** (2026-11-05, A6, P5 and A7): neither engine can connect a
  container started with `--network none` (rootless Podman also refuses
  slirp4netns), so the hub starts on the first member's bridge with
  `--network <bridge> --ip <hub address>` and connects further members
  afterwards; it may later drop that bridge like any other. Decision
  (coordinator): accepted as an A7 implementation detail. The hub still has
  no network of its own, so P5 is unchanged.
- **CR-P2-1** (2026-11-05, P2, section 8 `Executor`, P9 and P12): Claude
  Code starts every Bash tool command in a new session, so killing the
  harness process group leaves the tool tree running, and a `setsid` child
  survived a successful run. The executor sets `PR_SET_CHILD_SUBREAPER`,
  SIGTERMs the harness group, then SIGKILLs its whole descendant tree (from
  `/proc` ppid links) and confirms termination only when that tree is empty;
  it also sweeps after successful runs. The pgid is still recorded and is the
  first SIGTERM target. Decision (coordinator): accepted; the proposal's goal
  (every descendant confirmed gone) is unchanged, only the mechanism. Sent to
  A8 while running.
- **CR-P2-2** (2026-11-05, P2, section 8 `Driver`): Claude emits
  `rate_limit_event` and new `system` subtypes, so "an unknown event is an
  error" would fail real runs. Decode requires `system/init` (or an error
  `result`) first and a `result` before EOF, and logs and skips other types.
  The last `result` before EOF is final. Decision (coordinator): accepted;
  `driver.go` comment updated. A9 also sets
  `CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1` and creates contexts with
  `--session-id <uuid>` chosen and persisted before spawn.
- **CR-P2-3** (2026-11-05, P2, sections 3 and 5, P16): the transcript cannot
  be redirected into the table's `transcripts/`; Claude writes
  `<configDir>/projects/<enc-cwd>/<sid>.jsonl`, `session-env/<sid>/` and
  `file-history/<sid>/` in the config store. The driver reports these paths
  per harness session in `Outcome.Transcripts` (config-store-relative, all
  of them, not only the transcript), the context record stores them, and inventory
  deletion removes them. Paths outside the config store die with the
  container. Decision (coordinator): accepted; A9 adds the driver method
  through a CR on `Driver` when it starts, A2 and A8 consume the paths.
- **CR-A2-1** (2026-11-05, A2, section 1): `Prune` could not tell which
  earlier revision committed. Decision (coordinator): accepted as
  `Prune(group, committed, previous Revision)`; A7 passes the committed
  revision read from the group record before the update. Also accepted: no
  `config.HostA2A*` helpers (they would tie the gateway image hash to A2A
  code), and `audit/` created 1777 like `status/`.
- **CR-P3-1** (2026-11-05, P3, sections 6 and 10, P17): interactive Claude
  Code moves any MCP call to the background after 120 s
  (`CLAUDE_CODE_MCP_AUTO_BACKGROUND_MS`), after which the model holds a
  Claude task ID instead of our handle. Decision (coordinator): accepted.
  The P17 send-wait row splits in two: MCP send wait 90 s (also bounds a
  blocking `get_task`; the model's `wait_seconds` is clamped to it) and host
  `enclave a2a send` wait 10 min. The client sends MCP progress at most every
  30 s. A12 defines both bounds; A13 owns the host flag.
- **CR-P3-2** (2026-11-05, P3, section 8 `MCPClient`, P9): a crashed stdio
  server leaves the model with only "Connection closed", so retry identity
  cannot survive a client restart in memory. Decision (coordinator):
  accepted. A11 keeps a durable send journal in the container (message ID,
  peer, context, content hash, handle once known), written before each send.
  `send_task` takes `{peer, text, context?, wait_seconds?, retry?,
  message_id?}`; `retry: true` without `message_id` resolves the unique
  matching journal entry or errors listing candidates. `get_task` without a
  handle lists recent handles. Still four tools. Hard-timeout
  `notifications/cancelled`, idle abort and session exit mean "stop
  waiting", never `CancelTask`. Tool definitions currently cost about 1,000
  tokens against "a few hundred" in P21; A11 trims descriptions and reports
  the measured cost.
- **CR-P3-3** (2026-11-05, P3, P7 and P17): host invocation credential
  lifetime is the operation bound plus 60 s, capped at 15 min: 2 min for
  non-blocking verbs, host wait plus 60 s for a blocking send; a host wait
  above the cap minus 60 s is rejected. A credential expiring mid-stream
  prints the handle from the first stream event and exits 4. Decision
  (coordinator): accepted for A12 and A13.
- **CR-A8-1, CR-A8-2, CR-A8-3** (2026-11-05, A8, section 8 `Executor`, P12,
  P13). Decision (coordinator): accepted. `Executor` gains `HubLost` (fails
  running and queued tasks with reason `hub-lost`) and `Submit` (the bare
  task text, in memory only; tasks still queued at an adapter restart fail
  with termination `not-applicable`). `Depart(authoritative=false)` is the
  suspected path: admission stop and queued discard, running work continues.
  Interpretations also accepted: canceling a queued task yields
  `TASK_STATE_CANCELED` with termination `not-applicable` (nothing ran, so
  nothing needs confirming); a departure ack with nothing running carries
  `not-applicable`, which the host accepts next to `confirmed`; an unflagged
  reuse of a known message ID is `-31012`.
- **P1 change requests** (2026-11-05, P1-D and P1-P; evidence in
  `prototypes.md`). Decisions:
  - **CR-P1D-1** (A4, A3): on member removal or revocation the supervisor
    installs the deny rules, aborts hub sockets to the removed address with
    `ss -K dst <addr>`, then flushes conntrack; a flush alone stalls streams
    without closing them. The proxy also closes streams of a revoked
    principal itself. Accepted.
  - **CR-P1D-2** (A5 follow-up, P6, P18): member gateways drop hub-sourced
    traffic except to the adapter port, ahead of the published-port
    accepts; the proposal now states the compromised-supervisor blast
    radius (all outbound tokens, hub egress to host and internet). Accepted
    by the maintainer; no engine-level hub egress block in v1.
  - **CR-P1D-3** (A3): the hub sets `TCP_USER_TIMEOUT` of 10 s on its
    listener so accepted control and liveness connections detect silent
    loss well inside the reconnect window. Accepted.
  - **CR-P1P-1** (A4): every hub restart and reconnect changes the hub's
    MAC, and member gateways kept the stale neighbor entry for 8 to 54 s on
    Podman. The supervisor sends a gratuitous ARP (`arping -U -c1 -I <if>
    <addr>`) for every hub address on start and after every apply. Chosen
    over a fixed MAC because it needs no engine API change and covers both
    engines. Accepted.
  - **CR-P1P-2** (A4, A7): the hub runs with `--cap-drop ALL` plus
    `NET_ADMIN, NET_RAW, SETUID, SETGID, KILL, DAC_READ_SEARCH`,
    `--read-only`, `no-new-privileges`, `net.ipv4.ip_forward=0`, the default
    user namespace mapping on rootless Podman (not keep-id). Accepted.
  - **CR-P1P-3** (A7): a failed `connect --ip` leaves engine state
    inconsistent on both engines. A7 picks a free address up front, always
    disconnects on rollback ignoring its error, then verifies with inspect.
    Accepted.
  - **Hub start and reconcile** (A7): create the hub on the first member's
    bridge with `--ip`; before starting a stopped hub, disconnect network
    references whose network no longer exists; a restarted hub is ready
    only when both status files show a new epoch; interface names are never
    cached (A4 maps address to interface on every apply). Supervisor loads
    fixed DROP policies before reading `current`, waits for the proxy's
    first status write before signalling it (A4); the proxy installs its
    SIGHUP handler before writing status (A3).
  - **Secrets ownership**: on rootless Podman, `chmod 0400` then
    `podman unshare chown 65532:65532` on the staged `secrets.json` (needs a
    subordinate uid range of at least 65532 entries). Rootful Docker needs
    an engine-side hook; A7 proposes it (stop condition if none is clean).
  - **Q4 refined**: A7 computes the hub address as the last usable address
    of the inspected IPv4 subnet (`/28` on Docker 29, `/24` on Podman),
    rejects `--peer-group` only when inspect reports no IPv4 subnet, and
    treats a failed `connect --ip` as a failed join with rollback.
  - **Bounds for A12**: snapshot commit ack timeout 5 s (measured 15 ms to
    0.41 s); reconnect window stays 60 s (hub restart outage about 1.3 s
    with gratuitous ARP); Podman `strict` remains untested (netavark 1.4.0),
    `isolate=true` is supported per CR-1.
- **Wave 2 decisions on A12, A14, A9** (2026-11-05):
  - **CR-A12-1**: accepted; adding options needs the model, defaults and
    registry fixture edits A12 made.
  - **CR-A12-2, CR-A12-3**: `docs/cli-reference.md` flags and value
    completions for `--role` and `--a2a-log` go to A13 and D2.
  - **Provenance**: every `SessionAffected` option is recorded for every
    session, so the `a2a_*` keys appear additively in `ps --json` `options`
    for all sessions. Accepted as an additive change; I1 detects
    `--role` without `--peer-group` from option sources, not values.
  - **Heartbeat interval**: a constant of 30 s in the adapter, no option.
  - **Agent in the image (maintainer, A14 rework)**: the agent builder stage
    and binary go only into images built for sessions with `--peer-group`,
    as a separate image variant with its own hash; images of other sessions
    are unchanged. The variant hash covers only the agent's real build
    inputs (the agent drops its `internal/model` import if it has one).
    CR-A14-1 (embed the agent sources for packaged builds: `assets_embed.go`,
    `debian/rules`, `packaging/rpm/enclave.spec`,
    `scripts/verify-package-assets.sh`) is accepted into the A14 rework.
  - **Worker permissions (maintainer)**: serve mode needs bypassed
    permissions because nobody can approve prompts. A worker uses the
    session's yolo setting and passes the bypass flag through the driver's
    extra args; `--role worker` without yolo is rejected at start with a
    clear error (I1). No silent escalation.
  - **CR-A9-1**: accepted. `Driver.NewSessionID()` and
    `Invocation.NewHarnessSessionID`; the executor binds the new session ID
    in the table before `Start`. Follow-up package A8b after A10 merges.
  - **CR-A9-2**: deferred past v1 (transcript paths on `EventSession`).
  - **Empty final message**: a successful harness run with an empty reply
    completes with an empty text artifact instead of failing (A8b).
- **CR-A11-1** (2026-11-05, A11, section 6): orchestrators also get
  `ENCLAVE_A2A_TABLE_DIR` (`<config store>/a2a/<memberGeneration>/`); the
  client keeps `journal.json` there, so generation-scoped cleanup covers it.
  Decision (coordinator): accepted; A10's `LoadConfig` accepts it for the
  orchestrator role, I1 sets it.
- **CR-A11-2** (2026-11-05, A11, P13): an orchestrator without MCP holds
  liveness with a background `enclave-a2a-agent client hold`. Decision
  (coordinator): accepted for tools without MCP; v1 ships Claude Code only,
  so it is not wired in v1.
- **A4 change requests** (2026-11-05). Decisions (coordinator):
  - **CR-A4-1**: accepted; `internal/gateway/a2ahub_build.go` joins the
    docker import allowlist in `internal/arch` (applied at merge).
  - **CR-A4-2**: accepted; `a2a.EnvHubEpoch` (`ENCLAVE_A2A_HUB_EPOCH`) is set
    by the supervisor and adopted by the proxy, so both status files carry
    one epoch.
  - **CR-A4-3**: accepted as the `render-rules` output contract: one filter
    table with DROP policies on INPUT, OUTPUT and FORWARD; peer addresses
    as INPUT `-s <gw>/32` and OUTPUT `-d <gw>/32`; ESTABLISHED,RELATED
    accepts rendered by A3; `lo` may be omitted (the supervisor adds it);
    `revision=<n>` on stderr also on refusal when known.
  - **CR-A4-4**: accepted; packaging of the hub image inputs (embed list,
    `debian/rules`, rpm spec, verify and smoke scripts, `check-versions`
    pins) goes to the packaging follow-up together with CR-A14-1.
  - **For A7**: run the hub with `--tmpfs /run`, set
    `ENCLAVE_A2A_GROUP_GENERATION`, restart policy `no` (a hub that exits 1
    is failed closed and reconcile restarts it), ready when both status
    files carry a new epoch, publish and SIGHUP after each connect.
- **A3 and A10 change requests** (2026-11-05). Decisions (coordinator):
  - **CR-A10-1**: applied by the coordinator; `LoadConfig` lives in package
    `a2aagent` (`config.go`).
  - **CR-A10-2**: accepted. The ctl transport is a unix socket at
    `/tmp/enclave-a2a-agent/ctl.sock` (0600); A7 execs `ctl` as the session
    user. The harness can reach it, which the worker self-integrity row of
    P18 already accepts.
  - **CR-A10-3**: accepted; the push-config method names move to `wire.go`
    (follow-up F1).
  - **CR-A10-4**: accepted. The hub lists every current principal (member,
    role, generation, including the host principal) in `hello` and sends a
    `membership` event on every applied revision; the adapter checks
    principal generations against it. The hub also sends each
    orchestrator's current state after every `hello` (already done by A3).
    Follow-up F1 (A3 and A10 sides).
  - **CR-A10-5**: `ENCLAVE_A2A_HARNESS` (the `sandbox.a2a` block as JSON) is
    accepted; I1 sets it. `ENCLAVE_A2A_HARNESS_EXTRA_ARGS` is rejected: the
    adapter reads the existing `ENCLAVE_YOLO` and appends the spec's yolo
    flag itself (F1).
  - **CR-A10-6**: accepted; `-31019` `EPOCH_UNCONFIRMED` replaces the plain
    `-32603` for a send before the epoch is confirmed (F1).
  - **CR-A3-1**: accepted; the hub reads `ENCLAVE_A2A_MAX_REQUEST_BYTES`,
    which A7 sets.
  - **CR-A3-2**: accepted; non-JSON-RPC hub paths answer HTTP 401, 403, 404
    or 502 with a JSON-RPC error body and `"id": null`; A11 and A13 parse
    it.
  - **CR-A3-3**: accepted; reason constants `UNAUTHENTICATED`,
    `ROLE_FORBIDDEN`, `REQUEST_TOO_LARGE` move to `internal/a2a` (F1).
  - A3's interpretations are accepted: `-31003` covers expired host
    credentials and principals removed in the current epoch; version and
    extension headers are enforced on JSON-RPC POSTs only; card fetch
    failure is `-31001`; a member whose hub address no interface holds gets
    no rules and a warning, and the revision still renders.
- **Follow-up F1** (after Wave 2): CR-A9-1, the empty final message,
  CR-A10-3, CR-A10-4, CR-A10-5 yolo, CR-A10-6, CR-A3-3, and the truncated
  result marker on the wire (P17 "truncated with marker"), which the
  executor records but the adapter does not yet emit.
- **A15 change requests** (2026-11-05). Decisions (coordinator):
  - **CR-A15-1**: accepted with the spec-field variant, not the
    `setup.sh` snippet. `sandbox.a2a.orchestratorArgs` (Claude:
    `[--mcp-config, /usr/local/share/enclave/templates/claude-a2a-mcp.json]`)
    is appended by the runtime to the harness command of orchestrator
    sessions, the way `noMemoryArgs` is, so placement relative to a prompt
    or `--resume` is under runtime control. Owner: I1 (spec field in the
    model block and runtime append). Proposal 9 wording updated.
  - **CR-A15-2**: accepted. `a2a-*` extension skills are composed only into
    sessions with `--peer-group` (I1, `internal/runtime/skills_mount.go`).
    Regular Claude images rebuild once because files under
    `extensions/tools/claude/` changed (already true since A9's spec
    block); their behaviour is unchanged.
  - Provenance header format is final and recorded in P7.
- **A13 change requests** (2026-11-05). Decisions (coordinator):
  - **CR-A13-1, CR-A13-2, CR-A13-3**: accepted. `PeerGroups` gains
    `IssueHostCredential` and `RevokeHostCredential`; `Backend` gains
    `HubCall` (exec `--interactive --user 65532 ... call <path>`);
    `backend.Session` gains the A2A identity read from the labels. The
    interim paths in `internal/app/a2a_host.go` (reconcile per credential,
    engine `os/exec` outside `internal/backend`, identity from group
    records) are replaced. Follow-up F3.
  - **CR-A13-4**: accepted. Host send journal at
    `<state>/a2a/<group>/host-secrets/host-journal.json` (0600, at most 100
    entries, holds the text, deleted with `host-secrets/`).
  - **CR-A13-5**: accepted; `AGENTS.md` External Integrations lists
    `a2a-peers`, `a2a-task` and the `ps --json` `a2a` object at the
    experimental-marker removal.
  - A13 deviations accepted: `--wait` on `send`, `task get` and
    `task retry`; `task retry` accepts a message ID; `--backend` on engine
    verbs; `peers` reads `membership.json` (no queue depth); "At" fields use
    `joinedAt` where section 9 names no source; `task get` on a running
    task without `--wait` exits 4; `cancel` exits 0 when the worker answered.
- **I1 decisions** (2026-11-05). Decisions (coordinator):
  - Worker sessions run in the foreground without a TTY by default
    (one terminal pane per worker, P19); `--background` detaches them.
    Accepted. Starting a worker as a shell is rejected.
  - `orchestratorArgs` are appended after the user's arguments and before
    `--`, because `--mcp-config` is variadic in Claude Code. Accepted.
  - **Plain-session reconcile (F4)**: a session without `--peer-group`
    must not pay A2A latency. Plain `run` performs no reconcile. Plain
    `stop` reconciles only groups that a stopped container belonged to.
    `cleanup` and every A2A command keep `ReconcileAll`.
  - **CR-I1-1**: docs for `stop --peer-group`, worker foreground launch and
    `sandbox.a2a.orchestratorArgs` go to D2, D5 and the configuration docs.
- **F5a change requests** (2026-11-05). Decisions (coordinator):
  CR-F5a-1 accepted (section 2 wording above); CR-F5a-2 accepted, the
  smoke quarantine step needs a SIGTERM-ignoring harness instead of a 1 us
  timeout (docs and smoke cleanup package); CR-F5a-3 accepted, duration
  minimums go to `docs/configuration.md` (D4/configuration docs); CR-F5a-4
  sent to F5b (`stop --peer-group` exit rule).
- **CR-MH-1** (2026-11-05, multi-harness handoff): accepted by the
  maintainer's implementation request. Extends the original Claude-only
  serve-mode scope: `codex-exec-json`, `copilot-json` and `pi-json` join
  `claude-stream-json` in `model.A2AEventFormats` and `newDriver` selects
  the corresponding compiled-in driver. No hub, wire, task-handle or CLI
  schema changes. All four tools declare worker commands, compatible CLI
  version gates and session-local MCP orchestrator arguments. Codex's MCP
  environment allowlist explicitly passes the client configuration; it
  must not depend on the harness inheriting arbitrary variables.
- **CR-MH-2** (2026-11-05, retention probes): accepted implementation
  constraint. A native shared database containing prompt/reply data cannot
  be inventoried as a per-context file. Copilot and Pi use private native
  storage per pre-bound session; deletion must not remove another context.
  OpenCode's shared transcript database blocks worker support until it has
  a safe isolation or per-session deletion strategy. Theia's IDE entrypoint
  has no headless worker protocol. Neither tool ships integrated A2A
  support in this change. CR-A11-2 remains available for manual non-MCP
  orchestrators; this change does not silently wire `client hold` into IDEs.
- **CR-MH-3** (2026-11-05, validation): the mixed review smoke is
  `ENGINE=docker|podman scripts/a2a-smoke.sh mixed_review`, with
  `A2A_SMOKE_ORCHESTRATOR_TOOL=claude|codex`. It exercises both reviewers,
  rebuttals in their original contexts, synthesis, and group teardown.
  Harness prototype evidence and container-engine acceptance are separate
  gates; reports must say which were actually run.
- **CR-MH-4** (2026-11-05, pre-spawn retention): accepted as a focused
  follow-up to CR-A9-2. Optional `a2aagent.InvocationInventory` exposes
  `InvocationTranscripts(Invocation) []string`, called after `Command` and
  persisted before `Start`; a persistence failure prevents the spawn.
  Claude, Copilot and Pi inventory pre-bound session paths this way. Codex
  redirects its SQLite databases into a private per-turn directory and
  inventories that directory before spawn, resuming from the retained
  rollouts with a fresh database. `Outcome.Transcripts` still adds paths
  discovered dynamically. The remaining CR-A9-2 gap for dynamically learned
  paths is not represented as solved by this optional interface.
