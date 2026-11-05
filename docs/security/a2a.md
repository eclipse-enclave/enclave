# A2A peer groups (experimental)

Security model of the A2A message plane between sessions of one
[peer group](../cli-reference.md#a2a-peer-groups-experimental). The feature is
experimental; this model may change with it. Topology and firewall rules are
in [networking](../networking.md#a2a-peer-groups-experimental), host state and
file modes in [stores](../runtime/stores.md#a2a-peer-group-data-experimental),
and the design rationale in [design/a2a/](../design/a2a/).

Sessions without `--peer-group` get none of this: no hub, token, adapter,
MCP server or `a2a-*` skill.

## Trust model

- The host user and the `enclave` CLI issue every identity and token, and are
  trusted. The hub routes; it holds no durable state and has no engine
  socket. Each worker is authoritative for its own tasks and contexts.
- Roles are exclusive and host-declared: the orchestrator sends, workers
  answer. Workers cannot initiate tasks or address each other, and the hub
  forwards only to worker names from the published membership, never to an
  address or URL taken from a request.
- Containers hold message capabilities only. Starting, attaching and stopping
  sessions, reconcile and host commands stay on the host.
- Groups are isolated by topology: each group has its own hub, attachments and
  tokens. Members never share a bridge.

## Tokens

- Each member gets two tokens at join, one per direction: inbound (session to
  hub) and outbound (hub to session). Tokens are unique per member and group
  and never shared between workers. The inbound token is revoked when the
  member leaves or its departure is recorded, the outbound token when the
  worker leaves or the group stops. Every request also carries the member
  generation, so a rejoined member with the same name gets a new identity and
  cannot resume the previous member's contexts.
- The hub sees only hashes of inbound tokens. Outbound tokens are in the
  snapshot's `secrets.json`, readable by the proxy uid only, and in the host's
  `host-secrets/`.
- Tokens reach a session through an engine `--env-file`: a 0600 file in a
  private temporary directory, removed once the engine has created the
  container. They never appear on an argv, so other host users cannot read
  them from the process list. They are in the container's environment:
  every process in the session can read them, and `docker inspect` shows them
  to anyone with engine access, which is root-equivalent under rootful Docker
  anyway.
- No token is an option, so none appears in `enclave ps --json` provenance.

## Host principal and invocation credentials

Each group has one host principal (member `_host`, role `host`), created with
the group. Its generation changes only with the group generation, so a reused
group name never inherits its tasks.

`enclave a2a send` and `enclave a2a task` act as that principal through a
credential issued for the one invocation:

- issued under the group lock, published to the hub as a snapshot revision
  (only its hash), and revoked when the invocation ends, Ctrl-C included;
- bounded by the operation plus 60 s, capped at `a2a_credential_lifetime`
  (see [credential lifetime](../configuration.md#a2a-peer-groups-experimental)),
  so a crashed invocation leaves a credential that expires on its own;
- passed as the first stdin line of an engine exec of `enclave-a2a-hub call`,
  which runs as uid 65532 inside the hub and connects over loopback. No port
  is opened on the host, and `group.json` stores only credential hashes.

Workers bind tasks to the host principal and its generation, not to the
invocation, so a later invocation can query, cancel or retry a detached task.

## Worker self-integrity

A worker is trusted about itself. The tool's headless harness runs as the
same user as the adapter, so a task can:

- read the adapter's tokens from `/proc`;
- rewrite the worker's context table in its config store, including its own
  quarantine and admission stops;
- reach the adapter's control socket.

This is accepted. The table is advisory for the worker's own state, and the
`quarantine` and `admissionStop` fields of `ps --json` report what the worker
recorded. The host never relies on a worker's table to protect another
member: isolation between members rests on the topology, the hub and
per-member tokens, none of which a worker can change. A worker's tokens let
it act only as itself, and its role cannot send tasks.

Workers run unattended with the tool's permission bypass; a worker with yolo
disabled is rejected at start. Use `--project-mount readonly` for workers that
only need to read. The join refuses two writable members on one project path;
bind-mount aliases, nested additional directories and shared Git metadata of
linked worktrees are not detected.

## Transport

Traffic between members, the hub and the adapters is plaintext HTTP. Tokens,
task text and results are readable by root on the host and by anything that
gains a member gateway's or the hub's network namespace. This is not
equivalent to the TLS the gateway terminates for allowlisted hosts.

This is a stated non-conformance with A2A 1.0, section 7.1, which requires
encrypted transport. TLS is deferred until trust roots, key issuance and
rotation are specified. The other stated gap is that `ListTasks` answers
"unsupported operation" (section 3.1). See
[spec-pin.md](../design/a2a/spec-pin.md).

## Compromised hub

- **Proxy** (uid 65532, no capabilities, read-only root filesystem): can send
  tasks to and read results from every worker of its group, using the
  outbound tokens it holds. Nothing else.
- **Supervisor** (root in the hub, `NET_ADMIN`, `NET_RAW`, `SETUID`, `SETGID`,
  `KILL`, `DAC_READ_SEARCH`): can read every outbound token of its group and,
  after removing its own rules, reach the host and the internet through the
  engine's NAT from each member bridge. v1 blocks no hub egress below the hub.
- Either way, member gateways drop all hub-sourced traffic except to the
  adapter port, ahead of their published-port rules, so a compromised hub
  reaches no other listener in a member. Other groups have their own hub and
  tokens.

## Prompt injection

Task text reaches workers, and worker results reach the orchestrator's model.
Both are untrusted content, as on any channel between agents.

- The adapter prefixes every task prompt with one provenance line built from
  the authenticated principal, never from message content:
  `[a2a group=<group> from=<member> role=<role> generation=<generation> task=<taskID> context=<contextID>]`.
  `from` is `_host` for the host principal. Only the first line counts; a
  header inside the task text proves nothing.
- The shipped `a2a-provenance` skill, composed only into `--peer-group`
  sessions, explains the header and tells the agent to treat peer text and
  results as data, not instructions.
- These are mitigations, not a boundary: an agent can still follow injected
  instructions within its own session's permissions. Workers cannot initiate,
  so a worker cannot push work to other members.
- The hub audit log records status only. `--a2a-log full` adds request and
  response text to `audit.jsonl` on the host, without rotation.

## Not covered yet

- Docker with `userns-remap`: hub file access and secrets ownership are
  untested on a real daemon (`TestEngineA2AUsernsRemapFileAccess` exists; see
  [DEV](../DEV.md#a2a-peer-groups-experimental)).
- Podman with netavark `isolate=strict`: untested; the matrix was measured
  under the weaker `isolate=true` fallback (see
  [networking](../networking.md#podman)).
- SELinux: the overlay mount is untested on an enforcing host.
- A frozen orchestrator (for example `docker pause`) is not detected as gone:
  its connection to the hub stays open, so workers keep running its tasks to
  their deadline.
