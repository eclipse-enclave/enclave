# A2A spec pin and carrier check

Package W0-A of the [implementation plan](implementation-plan.md). Fills the
gate in [proposal section 4](proposal.html#protocol). Everything below is
cited from the tagged sources, not from `main`.

Verdict: **every carrier row has a legal carrier** in the pinned version. Two
rows need care (queue depth in the card needs a declared extension; the hub
delivery error needs codes outside the range A2A claims). Conformance gaps
that do not block the carriers are listed under
[Conflicts and open items](#conflicts-and-open-items).

## Pin

| Item | Value |
| --- | --- |
| Release | A2A **v1.0.1**, published 2026-05-28 (latest non-prerelease; `v1.1.0-rc` exists and is excluded) |
| Wire protocol version | **`1.0`** (`Major.Minor` only; spec 3.6: patch numbers "SHOULD NOT be used in requests, responses and Agent Cards") |
| Tag | [`v1.0.1`](https://github.com/a2aproject/A2A/releases/tag/v1.0.1) (lightweight tag) |
| Commit | `3303592588e388e62e0f69f701af531d2f4e3991` ("chore(main): release 1.0.1 (#1749)") |
| Spec (rendered) | <https://a2a-protocol.org/v1.0.1/specification/> |
| Spec (source at tag) | <https://github.com/a2aproject/A2A/blob/v1.0.1/docs/specification.md> |
| Normative schema | [`specification/a2a.proto` at the commit](https://github.com/a2aproject/A2A/blob/3303592588e388e62e0f69f701af531d2f4e3991/specification/a2a.proto), package `lf.a2a.v1` |
| JSON Schema | <https://a2a-protocol.org/v1.0.1/spec/a2a.json>, **non-normative** build artifact generated from the proto (spec 1.4); sha256 at fetch time `ba4b702b5edbdcbfe972c272484eb38e1fcd72af3c0b7eaa114b50e97455d8dd` |
| JSON-RPC 2.0 | <https://www.jsonrpc.org/specification> |

Spec 1.4: "`spec/a2a.proto` is the single authoritative normative definition
of all protocol data objects". Where the prose and the proto disagree (for
example the sample card's `security` versus the proto's
`security_requirements`), the proto wins. The v1.0.1 page still shows a
"Latest Released Version 1.0.0" box; this does not change the wire version.

v1.0.1 changes over v1.0.0: `application/a2a+json` preferred in the HTTP+JSON
binding (not ours), transcoding error fixes, TaskStatus value fixes in the
prose. No JSON-RPC wire change.

## Methods and paths

JSON-RPC binding (spec 9): JSON-RPC 2.0 over HTTP, `Content-Type:
application/json`, PascalCase method names, SSE (`text/event-stream`) for
streaming. Method table from spec 5.3.

| Need | Method | `params` | `result` |
| --- | --- | --- | --- |
| Send | `SendMessage` | `SendMessageRequest` | `SendMessageResponse` (`{"task": …}` or `{"message": …}`) |
| Stream | `SendStreamingMessage` | `SendMessageRequest` | SSE of `StreamResponse` |
| Get task | `GetTask` | `{"id", "historyLength"?}` | `Task` |
| Cancel task | `CancelTask` | `{"id", "metadata"?}` | `Task` |
| Resubscribe | `SubscribeToTask` | `{"id"}` | SSE of `StreamResponse`, first event is the `Task` |
| List tasks | `ListTasks` | `ListTasksRequest` | `ListTasksResponse` (not in the proposal; see open items) |
| Extended card | `GetExtendedAgentCard` | none | `AgentCard`; not needed, we declare `capabilities.extendedAgentCard` absent and must answer `-32004` |
| Push config | `CreateTaskPushNotificationConfig`, `GetTaskPushNotificationConfig`, `ListTaskPushNotificationConfigs`, `DeleteTaskPushNotificationConfig` | | Deferred; must answer `-32003` while `pushNotifications` is absent (spec 3.3.4) |

All request messages also carry an optional `tenant` string (see open items).

Card: `GET /.well-known/agent-card.json` at the server origin (spec 8.2,
14.3; RFC 8615 registration, status Permanent). Spec 8.2 also allows
"Direct Configuration: Pre-configured Agent Card URLs".

Headers (service parameters, spec 3.2.6 and 9.2):

- `A2A-Version: 1.0`. Clients MUST send it (3.6.1). Agents "MUST interpret
  empty value as 0.3" and return `VersionNotSupportedError` for unsupported
  versions (3.6.2), so a 1.0-only adapter rejects a missing header with
  `-32009`.
- `A2A-Extensions: <uri>[,<uri>]`: extensions the client opts into.

## Task states

`TaskState` enum, serialized as the ProtoJSON enum name (spec 5.5):

| Wire value | Kind | Emitted by us |
| --- | --- | --- |
| `TASK_STATE_UNSPECIFIED` | proto zero value | never; reject on input |
| `TASK_STATE_SUBMITTED` | "submitted and acknowledged" | yes (queued) |
| `TASK_STATE_WORKING` | in progress | yes |
| `TASK_STATE_COMPLETED` | terminal | yes |
| `TASK_STATE_FAILED` | terminal | yes |
| `TASK_STATE_CANCELED` | terminal | yes, only with confirmed termination |
| `TASK_STATE_REJECTED` | terminal | yes (see open items) |
| `TASK_STATE_INPUT_REQUIRED` | interrupted | no (v1) |
| `TASK_STATE_AUTH_REQUIRED` | interrupted | no (v1) |

Role enum: `ROLE_USER` (client to server), `ROLE_AGENT` (server to client).

## Object shapes

Field presence from the proto's `google.api.field_behavior` (spec 5.7):
**R** = REQUIRED ("MUST be present"; required arrays need at least one
element), otherwise optional. JSON names are camelCase (spec 5.5). `Struct` is
a JSON object; `Value` any JSON value; `Timestamp` an RFC 3339 UTC string with
`Z`, millisecond precision recommended (spec 5.6.1). Unknown fields SHOULD be
ignored (5.7). There is **no `kind` discriminator** anywhere in 1.0: oneofs
are discriminated by which member is present (spec A.2.1).

```text
SendMessageRequest   tenant string; message Message R; configuration SendMessageConfiguration; metadata Struct
SendMessageConfiguration
                     acceptedOutputModes []string; taskPushNotificationConfig TaskPushNotificationConfig;
                     historyLength int32 (optional, presence matters); returnImmediately bool (default false = blocking)
SendMessageResponse  oneof: task Task | message Message
StreamResponse       oneof: task Task | message Message | statusUpdate TaskStatusUpdateEvent | artifactUpdate TaskArtifactUpdateEvent

Task                 id string R; contextId string; status TaskStatus R; artifacts []Artifact; history []Message; metadata Struct
TaskStatus           state TaskState R; message Message; timestamp Timestamp          (no metadata field)
Message              messageId string R; contextId string; taskId string; role Role R; parts []Part R;
                     metadata Struct; extensions []string; referenceTaskIds []string
Part                 oneof content: text string | raw bytes(base64) | url string | data Value;
                     metadata Struct; filename string; mediaType string
                     text part on the wire: {"text": "..."} (optionally "mediaType": "text/plain")
Artifact             artifactId string R (unique within the task); name string; description string;
                     parts []Part R; metadata Struct; extensions []string
TaskStatusUpdateEvent
                     taskId string R; contextId string R; status TaskStatus R; metadata Struct   (no "final" flag)
TaskArtifactUpdateEvent
                     taskId string R; contextId string R; artifact Artifact R; append bool; lastChunk bool; metadata Struct
GetTaskRequest       tenant; id R; historyLength int32 (optional)
CancelTaskRequest    tenant; id R; metadata Struct
SubscribeToTaskRequest tenant; id R

AgentCard            name R; description R; supportedInterfaces []AgentInterface R; provider AgentProvider;
                     version R; documentationUrl; capabilities AgentCapabilities R;
                     securitySchemes map<string,SecurityScheme>; securityRequirements []SecurityRequirement;
                     defaultInputModes []string R; defaultOutputModes []string R; skills []AgentSkill R;
                     signatures []AgentCardSignature; iconUrl                    (no metadata field)
AgentInterface       url R; protocolBinding R ("JSONRPC"); tenant; protocolVersion R ("1.0")
AgentCapabilities    streaming bool?; pushNotifications bool?; extensions []AgentExtension; extendedAgentCard bool?
AgentExtension       uri string; description string; required bool; params Struct
AgentSkill           id R; name R; description R; tags []string R; examples; inputModes; outputModes; securityRequirements
SecurityScheme       oneof: apiKeySecurityScheme | httpAuthSecurityScheme | oauth2SecurityScheme |
                     openIdConnectSecurityScheme | mtlsSecurityScheme
HTTPAuthSecurityScheme description; scheme R (e.g. "Bearer"); bearerFormat
SecurityRequirement  schemes map<string, {list []string}>
```

Minimal JSON-RPC send:

```json
{"jsonrpc": "2.0", "id": 1, "method": "SendStreamingMessage",
 "params": {"message": {"messageId": "…", "contextId": "…", "role": "ROLE_USER",
                        "parts": [{"text": "…"}]}}}
```

ID rules (spec 3.4):

- `taskId` is server-generated; "Client-provided `taskId` values for creating
  new tasks is **NOT** supported"; an unknown `taskId` MUST yield
  `TaskNotFoundError`.
- `contextId`: agents MAY generate one when the message has none and MUST
  return it; MAY accept client-provided values; "If an agent cannot accept a
  client-provided `contextId`, it **MUST** reject the request with an error and
  **MUST NOT** generate a new `contextId`". A `contextId` without `taskId`
  starts a new task in an existing context (3.4.3), which is our per-turn model.
  Mismatched `contextId` and `taskId` MUST be rejected.
- `messageId` is REQUIRED and "created by the message creator" (proto).

Execution mode (spec 3.2.2): `SendMessage` is **blocking by default** and
returns only on a terminal or interrupted state. `returnImmediately: true`
returns right after task creation. Closing a stream does not end the task:
"The task lifecycle is independent of any individual stream's lifecycle"
(3.5.2).

## Errors

JSON-RPC error object: `code` (int), `message` (string), `data` (array of
objects, each with an `@type` key in ProtoJSON `Any` form; `google.rpc.ErrorInfo`
and `google.rpc.BadRequest` SHOULD be used where they fit) (spec 9.5, 3.3.2).

| Code | Name |
| --- | --- |
| `-32700` | JSONParseError |
| `-32600` | InvalidRequestError |
| `-32601` | MethodNotFoundError |
| `-32602` | InvalidParamsError |
| `-32603` | InternalError |
| `-32001` | TaskNotFoundError |
| `-32002` | TaskNotCancelableError |
| `-32003` | PushNotificationNotSupportedError |
| `-32004` | UnsupportedOperationError |
| `-32005` | ContentTypeNotSupportedError |
| `-32006` | InvalidAgentResponseError |
| `-32007` | ExtendedAgentCardNotConfiguredError |
| `-32008` | ExtensionSupportRequiredError |
| `-32009` | VersionNotSupportedError |

Range rules: JSON-RPC 2.0 reserves `-32768` to `-32000` ("Any code within
this range, but not defined explicitly below is reserved for future use"),
with `-32000` to `-32099` "Reserved for implementation-defined server-errors",
and "The remainder of the space is available for application defined errors".
A2A 1.0 claims "codes in the range `-32001` to `-32099`" (spec 9.5). Unlike
0.3, 1.0 no longer says servers MAY define more codes in that range.
Authentication and authorization failures are "JSON-RPC custom error" with no
assigned code (3.3.2). Not-found and not-authorized SHOULD NOT be
distinguishable (3.3.2, 13.1).

The binding does not specify the HTTP status for JSON-RPC errors.

## Streaming

`SendStreamingMessage` and `SubscribeToTask` answer HTTP 200,
`Content-Type: text/event-stream`. Each SSE event is a single `data:` line
holding a full JSON-RPC response with the request's `id` and a
`StreamResponse` as `result` (spec 9.4.2):

```text
data: {"jsonrpc": "2.0", "id": 1, "result": {"task": {…}}}

data: {"jsonrpc": "2.0", "id": 1, "result": {"statusUpdate": {…}}}
```

- No `event:` names, no SSE `id:` and no `Last-Event-ID` resume. Reconnect is
  `SubscribeToTask`, whose first event is the current `Task` (3.1.6).
- Order: the `Task` (or a single `Message`, then close), then status and
  artifact updates; the stream MUST close when the task reaches a terminal
  state (3.1.2). Termination is signalled by the state and the close, not by a
  flag.
- `SubscribeToTask` on a terminal task returns `-32004`; use `GetTask`.
- Events MUST NOT be reordered; all streams of a task get the same events
  (3.5.2).
- An error after the stream started is not specified for JSON-RPC (see open
  items).

## Renames versus the proposal

| Proposal (0.3 memory) | Pinned 1.0 |
| --- | --- |
| send `message/send` | `SendMessage` |
| stream `message/stream` | `SendStreamingMessage` |
| get task `tasks/get` | `GetTask` |
| cancel `tasks/cancel` | `CancelTask` |
| resubscribe `tasks/resubscribe` | `SubscribeToTask` (errors on terminal tasks) |
| `agent/getAuthenticatedExtendedCard` | `GetExtendedAgentCard`; capability moved to `capabilities.extendedAgentCard` |
| card "under `/.well-known/`" | exactly `/.well-known/agent-card.json` (unchanged since 0.3) |
| states `submitted`, `working`, … (kebab-case) | `TASK_STATE_SUBMITTED`, `TASK_STATE_WORKING`, … ; `TASK_STATE_UNSPECIFIED` added |
| role `user`/`agent` | `ROLE_USER`/`ROLE_AGENT` |
| `kind` discriminators (`text`, `task`, `status-update`, `artifact-update`) | removed; member name discriminates (`{"text": …}`, `{"statusUpdate": …}`) |
| file part `{"kind":"file","file":{…}}` | flat `raw`/`url` plus `filename`, `mediaType` |
| `TaskStatusUpdateEvent.final` | removed; stream closes at terminal state |
| `MessageSendParams` | `SendMessageRequest` |
| card `url`, `preferredTransport`, `additionalInterfaces`, `protocolVersion` | `supportedInterfaces[]` of `{url, protocolBinding, protocolVersion, tenant}` |
| card `security` | `securityRequirements` (prose and sample still say `security`; proto wins) |
| security scheme `{"type": "http", …}` | oneof wrapper `{"httpAuthSecurityScheme": {"scheme": "Bearer"}}` |
| send blocking flag (`blocking`) | `configuration.returnImmediately`, blocking by default |
| no version header | `A2A-Version` required; empty means 0.3 |
| `-32000..-32099` open to servers | A2A claims `-32001..-32099`; new codes `-32008`, `-32009` |

Unchanged and matching the proposal: worker issues `taskId` (mandatory) and
`contextId` (MAY); `messageId` is client-set and required; metadata is
available on `Task`, `Message`, `Part`, `Artifact`, both update events,
`SendMessageRequest` and `CancelTaskRequest`, but **not** on `TaskStatus` or
`AgentCard`; extensions are URIs declared in `capabilities.extensions`,
opted into with `A2A-Extensions`, and listed in `Message.extensions` /
`Artifact.extensions`.

## Carrier table

Metadata is legal free-form: spec 3.2.5 "Metadata keys … are strings and
values can be any valid value that can be represented in JSON. Extensions can
be used to strongly type metadata values". Using metadata does not require an
extension; the card area does (the card has no metadata field). Proposal (not
spec): declare one extension and key all our metadata by its URI, the
convention shown in spec 4.6.1 and 4.6.2.

- Extension URI (proposal): `https://enclave.eclipse.dev/a2a/ext/v1`.
- Card: `capabilities.extensions: [{"uri": "<uri>", "required": true, "params": {…}}]`.
  `required: true` makes the agent return `-32008` to clients that do not
  send `A2A-Extensions: <uri>` (3.3.4), which keeps clients that cannot read
  termination status out.
- Metadata: `"metadata": {"<uri>": {…}}`; `Message.extensions` and
  `Artifact.extensions` list the URI when used.

| Proposal row | Carrier (pinned spec) | Proposed key or code | Verdict |
| --- | --- | --- | --- |
| Termination status (confirmed, unconfirmed, not-applicable) | `Task.metadata` (`Struct`, proto field 6, "A key/value object to store custom metadata about a task") for `GetTask`, `CancelTask` and the first stream event; `TaskStatusUpdateEvent.metadata` (`Struct`, field 4, "Metadata associated with the task update") on the terminal event. Free-form metadata. `TaskStatus` itself has no metadata. | `metadata["<uri>"].termination`: `"confirmed"` \| `"unconfirmed"` \| `"not-applicable"` | Pass |
| Structured ending reason (cancel, deadline, hub-lost, epoch-changed, orchestrator-lost, outcome-unknown) | Same carriers as termination. A human-readable explanation may also go in `TaskStatus.message` (role `ROLE_AGENT`, text part). | `metadata["<uri>"].reason`: `"cancel"` \| `"deadline"` \| `"hub-lost"` \| `"epoch-changed"` \| `"orchestrator-lost"` \| `"outcome-unknown"`; the cancel-unconfirmed row uses `["cancel","outcome-unknown"]` as `reasons` or a separate `outcomeUnknown: true` (pick one in W0-B) | Pass |
| Queue depth and queue position | Card: only `AgentCapabilities.extensions[].params` (`Struct`, "Extension-specific configuration parameters"), so this **requires the declared extension**. Task: `Task.metadata` and `TaskStatusUpdateEvent.metadata` while `TASK_STATE_SUBMITTED`. | Card `params.queueDepth` (int); task `metadata["<uri>"].queuePosition` (int, 0 = next) | Pass, with caveat: the card is cacheable (8.6, SHOULD send `Cache-Control` with a `max-age`); serve it with `max-age=0` and treat depth as advisory |
| Client message ID and retry flag | `Message.messageId` (string, REQUIRED, "created by the message creator"); spec 3.3.1 "Agents may utilize the messageId to detect duplicate messages". Retry flag in `Message.metadata` (`Struct`, "Any metadata to provide along with the message"). | `messageId` as is; `metadata["<uri>"].retry: true` | Pass |
| Hub delivery error (adapter unreachable, unknown member, revoked) | JSON-RPC error object (`code`, `message`, `data[]` with `@type`). A2A claims `-32001..-32099`, so those are off limits; `-32000` is free but a single code. Legal non-colliding space: outside `-32768..-32000` ("available for application defined errors"). | Proposal: block `-31000..-31099` for enclave. Hub: `-31001` adapter unreachable, `-31002` unknown member, `-31003` member revoked, `-31004` unauthenticated, `-31005` not permitted (role check). `data: [{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "domain": "enclave.eclipse.dev", "reason": "ADAPTER_UNREACHABLE", "metadata": {"member": "…"}}]` | Pass |
| Principal and provenance | Hub to adapter: transport layer, spec 7: "Identity information is handled at the protocol layer, not within A2A semantics", so an HTTP header set by the hub after stripping inbound identity headers is legal (custom headers must not use the `A2A-` prefix, 3.2.6). Adapter to harness: the text prefix in the `text` part (content, not a protocol concern). Recorded provenance: `Message.metadata` of the message as stored in `Task.history`. | Header `Enclave-A2A-Principal` (group, member, role, generation); `metadata["<uri>"].principal: {group, member, role, generation}` written by the adapter only. A client-supplied `principal` key is rejected with `-32602`. | Pass |

## Conflicts and open items

None of these remove a carrier. They need a decision before or during W0-B
and A1.

1. **HTTPS MUST.** Spec 7.1: "Production deployments **MUST** use encrypted
   communication"; `AgentInterface.url` "Must be a valid absolute HTTPS URL in
   production". The proposal accepts plaintext HTTP in v1 (section 18,
   Transport). This is a stated non-conformance, not a carrier problem; the
   proposal should name it as such.
2. **Blocking send.** `SendMessage` blocks until a terminal or interrupted
   state by default. If the client drops a blocking call at its wait bound it
   never learns the `taskId`. The client must use `SendStreamingMessage`
   (first event carries the `Task`) or `returnImmediately: true` plus
   `GetTask`. Dropping a stream is legal and does not cancel (3.5.2), which
   matches "a wait timeout is never a cancellation".
3. **Resubscribe after the end.** `SubscribeToTask` on a terminal task is
   `-32004`; clients fall back to `GetTask`. The terminal event carries the
   termination metadata only on live streams; `Task.metadata` must hold the
   same values so `GetTask` returns them.
4. **Error mid-stream.** The JSON-RPC binding defines no in-stream error. A
   reasonable reading is a final SSE `data:` event holding a JSON-RPC error
   response with the same `id`, then close. Applies to the hub losing the
   adapter mid-stream. Spec-silent; decide in A1/A3.
5. **HTTP status for JSON-RPC errors** is unspecified. Proposal: HTTP 200 with
   the error body for every JSON-RPC error, including hub errors, so clients
   parse one shape.
6. **Adapter rejections without an A2A code.** Unknown task: `-32001`
   (mandated by 3.4.2), also for tasks bound to another principal (13.1).
   Unknown or foreign context, expired context (tombstone), message ID reused
   with different content, ambiguous retry, admission stopped, quarantined:
   no A2A code exists. Proposal: same enclave block, `-31010` context not
   found, `-31011` context expired, `-31012` message ID conflict, `-31013`
   ambiguous retry, `-31014` admission stopped, `-31015` worker quarantined,
   each with an `ErrorInfo` reason. "Expired" may only be answered to the
   binding's own principal, otherwise "not found".
7. **`TASK_STATE_REJECTED` versus errors.** The proposal lists `rejected` as
   emitted but does not say when. Pre-admission refusals (admission stop,
   quarantine, dedupe conflicts) create no task and fit a JSON-RPC error
   better; `REJECTED` would need a task ID. Decide which events, if any, emit
   `REJECTED`.
8. **Cancel results.** `CancelTask` returns the updated `Task`, success "is
   not guaranteed" (3.1.5), so returning `TASK_STATE_FAILED` with
   `termination: unconfirmed` for an unconfirmed kill is legal. Cancel on a
   terminal task: `-32002`. Cancel is idempotent (3.3.1).
9. **ListTasks.** Listed among the core operations "all A2A implementations
   must support" (3.1) and absent from the proposal. Either implement it
   scoped to the caller's principal (13.1) or answer `-32004`. Cheap given the
   context table.
10. **Version header.** Clients send `A2A-Version: 1.0`; the adapter and hub
    reject a missing or other version with `-32009` (empty means 0.3).
11. **Hub-served cards and routing.** The well-known URI is per origin; the
    hub serves many agents at one origin. Legal options: per-peer card URLs
    such as `/peers/<name>/.well-known/agent-card.json` used as "Direct
    Configuration" (8.2), with `supportedInterfaces[0].url =
    http://a2a.enclave:<port>/peers/<name>`; or one URL with
    `AgentInterface.tenant = <name>`, which spec 8.3.2 makes clients echo in
    every request's `tenant` field and which exists for "multiple agents …
    served behind a single A2A endpoint". The proposal's path form is legal;
    `tenant` is an alternative to weigh in W0-B.
12. **Security scheme declaration.** Optional in the card. Declare
    `securitySchemes: {"bearer": {"httpAuthSecurityScheme": {"scheme":
    "Bearer"}}}` and `securityRequirements: [{"schemes": {"bearer": {"list":
    []}}}]`; servers "MUST authenticate every incoming request" (7.4), which
    the token model already does.
13. **No `input-required`, no invented states.** The spec never requires an
    agent to emit `INPUT_REQUIRED` or `AUTH_REQUIRED`; `SUBMITTED` legally
    covers queued work. No conflict.
14. **Card `required` extension.** Proposed `required: true` means any
    non-enclave client gets `-32008`. Intended (only enclave clients exist),
    but it is a choice; flag it in W0-B.
