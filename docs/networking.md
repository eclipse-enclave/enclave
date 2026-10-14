# Networking

## How It Works

By default, network access is restricted via a gateway sidecar (dnsmasq + transparent proxy). DNS only resolves allowlisted domains and the proxy enforces Host/SNI against the same allowlist. This prevents agents from making arbitrary outbound requests.

Pass/deny audit events are logged to `~/.local/state/enclave/projects/<project-hash>/<tool>/logs/network.log`. Read them with [`enclave network log`](#reading-the-network-log). The default `coarse` mode records one event per TLS connection rather than one per request, so read [Coverage and granularity](#coverage-and-granularity) before drawing conclusions from what the log does or does not contain.

For request-level logging, enable:

```bash
enclave --network-log=requests
```

This forces allowlisted HTTPS traffic through the gateway MITM proxy so the gateway can emit HTTP-style request audit events for both HTTP and HTTPS, instead of one event per TLS connection. Some clients that pin certificates or use custom trust stores may fail in this mode.

`--network-log=full` does the same and also stores the headers and bodies of
every relayed request and its response; see [Full capture](#full-capture).

The container trusts the gateway CA through `SSL_CERT_FILE`,
`REQUESTS_CA_BUNDLE` and `GIT_SSL_CAINFO` (the system bundle plus the gateway
CA, written to `/tmp/enclave-ca/ca-certificates.crt` at startup) and
`NODE_EXTRA_CA_CERTS`. Git needs its own variable because its TLS library
ignores `SSL_CERT_FILE`.
They are set on the container itself, so processes started with `docker exec`,
such as an attached IDE's backend, trust the gateway as well.

Only the gateway CA persists on the host; leaf certificates stay inside each
gateway container. Upgrades may leave an unused legacy cache at
`~/.local/state/enclave/tls/hosts`. On Linux, its old container-owned permissions
can prevent normal removal; delete it with
`sudo rm -rf ~/.local/state/enclave/tls/hosts`, or use
`podman unshare rm -rf ~/.local/state/enclave/tls/hosts` when rootless podman
last owned it.

To disable destination restrictions:

```bash
enclave --allow-all-network
```

Without protected auth, this retains the existing gateway bypass and disables
HTTP credential release. With `--protect-auth`, either `--allow-all-network` or
policy `mode: unrestricted` keeps the gateway, TLS trust and credential mediation:

```bash
enclave --tool claude --auth-name protected --protect-auth --allow-all-network
```

| Session | Destination policy | Credential mediation |
|---------|--------------------|----------------------|
| Default | DNS, firewall and proxy allowlists | Declared env-secret release; OAuth only with `--protect-auth` |
| Unrestricted, unprotected | Gateway bypass | Disabled |
| Unrestricted, protected | Arbitrary IPv4 destinations; IPv6 remains disabled by the gateway | Gateway remains mandatory; OAuth endpoint/field rules and env-secret header rules still apply |

An unrelated IPv4 destination needs no allowlist entry in the protected
unrestricted mode, but it cannot redeem protected credentials. Proxy failure
locks down egress; it never switches the session to a gateway bypass. Docker,
Podman and the real Claude flow still require the
[engine and human verification runs](runtime/oauth-broker-verification.md).

The experimental `qemu` backend currently has no restricted-egress implementation, so it always runs with all outbound network allowed. Selecting it implies `--allow-all-network` automatically and prints a notice; passing `--allow-domain` (which would require restricted egress) is rejected.

## Reading the Network Log

```bash
enclave network log                        # Events of the current project and tool
enclave network log --follow               # Stream new events as they arrive
enclave network log --summary              # Per-domain aggregate
enclave network log --verdict deny         # Only what was blocked
enclave network log --domain '*.github.com' --since 10m
enclave network log --json | jq            # The integration contract
enclave network log --show-capture <id>    # One exchange recorded by --network-log=full
```

The log is read from disk, so events of a session that has already exited are
still available. `--session <container>` reads one session's events and
`--all-running` merges every running gateway's log in timestamp order; only
`--all-running` needs Docker. `--session` uses it when reachable, so a session of
another project can be named, and otherwise reads this project's log. A name that
appears on no event is an error rather than an empty result. Concurrent sessions
of the same project and tool append to one file, so `--session` also filters on
the `session` field: events written before session stamping existed carry none
and are not shown.

`--since` takes a duration (`10m`), an RFC3339 timestamp, or `session`, which
resolves to the start of the most recent session in scope and therefore needs a
scope covering exactly one session. Because it anchors on a session boundary it
also limits the output to that session's events, so a concurrent session sharing
the file does not bleed in. Combined with `--session` it resolves to that
session's own start.

Output is the aligned human form. `--json` is the machine contract: on its own
it emits the raw JSONL event stream verbatim, and with `--summary` it emits the
aggregate as a single JSON object. Events that never carried a host name, such as
a TLS connection denied at the ClientHello, are grouped under `(no domain)` (an
empty `domain` in JSON), so the summary totals match what `--verdict deny`
prints. The last column shows the matched rule of a denied event, except that
policy denials (`allowlist` on `http` and `tcp` rows, `nxdomain` on `dns` rows)
read `blocked by policy`; the JSON keeps the raw rule. Colour follows `NO_COLOR`
and `ENCLAVE_COLOR`, and is off when stdout is not a terminal.

### What is recorded

| Type | Written by | Notes |
|------|-----------|-------|
| `http` | MITM proxy | One event per request, with method, path, status and byte counts. Query strings are never logged: only the URL path is recorded. Written for every plaintext HTTP request, and for HTTPS only where the proxy terminates TLS. Under `--network-log=full`, `capture_id` names the [capture](#full-capture) of the request |
| `tcp` | MITM proxy | One event per TLS connection, written at the ClientHello with the SNI host, the verdict and the matched rule. The requests carried inside the connection are not visible |
| `dns` | DNS audit translator | One event per denied or failed lookup, with `rule` naming the condition (`nxdomain` for a domain blackholed by policy, `upstream-servfail`, `upstream-refused` or `upstream-nxdomain` for an upstream failure) |
| `session` | Host, at gateway start | A boundary marker naming the session. Not an audit event: excluded from `--summary` and never matched by `--verdict`, `--domain` or `--type` |

Protected OAuth sessions disable dnsmasq's raw query log and its DNS audit
translator so attacker-controlled query names cannot record bootstrap material
or handles. They therefore have no `dns` events, including for denied lookups.
Broker audit events redact untrusted request details while preserving safe
policy verdicts; they are not OAuth wire traces.

A blocked lookup in an unprotected session usually produces two `dns` events, one for the A query and one
for the AAAA query, because the resolver asks for both. `NODATA` answers are not
recorded: dnsmasq returns `NODATA-IPv6` for every allowlisted host without an
IPv6 record, so recording it would report allowed domains as denied.

### Coverage and granularity

The log records policy decisions and connections, not traffic volume. Four
limits matter before a quiet log is read as a quiet session:

- **Successful DNS lookups are never recorded.** Only denied and failed lookups
  produce a `dns` event, and dnsmasq answers repeat lookups from its cache
  without going upstream at all.
- **In `coarse` mode an HTTPS connection is one `tcp` event, not one per
  request.** The proxy reads the ClientHello, records the SNI host, and tunnels
  the rest through without decrypting it. A client holding a long-lived HTTP/2
  connection — an agent talking to its model API, for example — produces a
  single event when the connection is opened and nothing for the hundreds of
  requests that follow. Short-lived connections (a `git fetch`, a CLI call, a
  poller reconnecting) produce one event each, so they dominate a coarse log
  even when they carry far less traffic.
- **`http` events for HTTPS require the proxy to terminate TLS.** In `coarse`
  mode that happens only for hosts covered by a declared secret's HTTP release
  rules, where the gateway has to rewrite a header anyway, and only when that
  secret was actually resolved for the session. Protected OAuth separately
  registers its capture and API hosts before login; those hosts also use MITM.
  Unprotected native OAuth alone does not register a release rule.
- **An upgraded connection is one `http` event.** A WebSocket, or any other
  protocol a client switches to (SPDY for `kubectl exec`, for example), is
  logged once with status `101`, when it closes or the proxy stops. The traffic
  it carries gets no events of its own, so requests multiplexed inside it do
  not show up in the log, even with `--network-log=requests`. `full` mode
  stores the messages of a WebSocket in its capture. Protected sessions scan a
  WebSocket that carries a released credential for that credential.

Run the session with `--network-log=requests` (or `full`) to force MITM for
every permitted HTTPS host and get an `http` event per request. In every mode,
SSH on port 22 bypasses the HTTP/TLS proxy and is never recorded.

### Rotation

At session start, a log larger than 32 MB is copied to `network.log.1`,
replacing any previous generation, and then truncated in place. The reader reads
`.1` first, so the boundary is invisible. Truncating
rather than renaming keeps the file a running gateway has bind-mounted, so a
session that is already going on keeps writing where readers can see it. A
session never rotates its own log, so a single long-running session can grow past
the cap, and worst-case disk use is roughly twice the cap per project and tool.
Concurrent session starts serialize on a `network.log.lock` file next to the log,
so two of them cannot discard the generation the other just wrote.

## Full Capture

`--network-log=full` (`"network_log": "full"`) keeps everything `requests` mode
does and also stores the request line, headers and body of every request the
MITM proxy relays, together with the response status, headers and body, and
the messages of WebSocket connections. It is off unless selected, and meant for
debugging a tool's API traffic or checking what an agent sent out.

```bash
enclave --network-log=full
enclave network log --type http --since session   # rows end in "capture <id>"
enclave network log --show-capture 3f9a0c1d2e4b5a69
```

**A capture is sensitive.** Request bodies are stored as the agent sent them,
including prompts, file contents, and any token the agent read and put into a
request. Query strings and cookies are stored too, and so are credentials an
upstream issues: a session or access token in a response body, and the header
that later sends it back, are stored as they crossed the proxy and stay valid
until they expire. Treat the capture directory like the credentials it
contains.

### Secrets in captures

The proxy snapshots each request before it releases secrets into the headers,
so the capture holds the placeholders the sandbox sent, never the released
values. A released value that shows up anywhere else, such as an upstream
echoing the credential in a response header or body, is replaced by its
placeholder before it is written. Bodies compressed with `gzip` or `deflate`
and WebSocket messages compressed with `permessage-deflate` are decoded first,
so this covers most API traffic. The replacement matches the value's literal
bytes, so it misses a value that arrives encoded in another way: in a body
compressed with `br` or `zstd`, in a WebSocket message that could not be
decoded, URL-encoded, escaped in JSON, or in base64. Credentials without an
HTTP release rule are ordinary request content and are stored as sent.

### Capture store

Captures live next to the network log, one directory per session:

```text
~/.local/state/enclave/projects/<project-hash>/<tool>/logs/capture/<session>/
  <id>.json             request line, headers, response head, body metadata
  <id>.request.body     request body as sent upstream (gzip/deflate decoded)
  <id>.response.body    response body as relayed to the client (gzip/deflate decoded)
  <id>.messages.jsonl   WebSocket messages, one JSON object per line
  <id>.open.json        record of a WebSocket that is still open
```

The `http` event of a captured request carries the ID in `capture_id`, so its
record is `capture/<session>/<capture_id>.json` with `<session>` from the
event's `session` field. Requests the gateway denies were not relayed and have
no capture. The record and the event are written when the exchange ends, so a
stream that is still open has neither yet. A WebSocket is visible earlier:
once its handshake is relayed, `<id>.open.json` holds the record so far and
`<id>.messages.jsonl` grows as messages cross, so `tail -f` follows the
conversation; list `*.open.json` to find the IDs of open WebSockets. When the
connection closes, `<id>.json` replaces the open record and the `http` event is
logged. When the proxy stops, for example because `enclave network apply`
restarts it, it closes its WebSockets first so that they are recorded; the
files of one cut off by a crash are removed as leftovers. The `capture`
directory is private to your user. The session directories below it are
world-writable, because the gateway's proxy runs as a different UID, and rely
on that parent for protection. The capture files belong to that UID: under
rootful Docker `ls -l` shows whichever host account has the same UID (for
example `messagebus`), and under rootless Podman a subordinate UID. You can
read and delete them, but not modify them.

The record (`schemaVersion: "1"`, `kind: "network-capture"`) looks like this:

```json
{
  "schemaVersion": "1",
  "kind": "network-capture",
  "id": "3f9a0c1d2e4b5a69",
  "session": "enclave-claude-e9671acd2448-1",
  "ts": "2026-10-21T10:11:12.345678Z",
  "durationMs": 1830,
  "request": {
    "method": "POST",
    "url": "https://api.anthropic.com/v1/messages?beta=true",
    "proto": "HTTP/1.1",
    "headers": {"Content-Type": ["application/json"], "X-Api-Key": ["<placeholder>"]},
    "body": {"file": "3f9a0c1d2e4b5a69.request.body", "bytes": 48213, "storedBytes": 48213}
  },
  "response": {
    "status": 200,
    "proto": "HTTP/1.1",
    "headers": {"Content-Type": ["text/event-stream"]},
    "body": {"file": "3f9a0c1d2e4b5a69.response.body", "bytes": 10422, "storedBytes": 10422}
  }
}
```

- Request headers are those the client sent, before secret release and before
  the proxy adds `X-Forwarded-For` and `X-Forwarded-Proto`. Response headers are
  those relayed to the client.
- `body` is absent when a message had none. `bytes` counts what crossed the
  proxy and `storedBytes` what the file holds. `truncated: true` marks a body
  that is not stored whole: cut at the size cap or the session budget,
  interrupted, not decodable to its end, or a request body the upstream
  answered before reading to the end, in which case what the client sent
  afterwards is not recorded. `file` is relative to the record and absent when
  nothing was stored.
- A body with `Content-Encoding: gzip`, `x-gzip` or `deflate` is stored
  decoded, and `decodedFrom` names the encoding; `bytes` still counts the
  encoded bytes relayed, and the headers stay as relayed. Other encodings are
  stored as relayed, and for `zstd` and `br` `--show-capture` prints the
  `zstd -dc` or `brotli -dc` command that reads the file.
- When the request could not be relayed, `error` says why and `response` may be
  absent.
- A WebSocket has a `websocket` object, described below. Other upgraded
  connections (status `101`) record the handshake only.
- Fields are only added, never renamed or removed.

#### WebSocket messages

The record of a connection upgraded to WebSocket describes its messages file:

```json
"websocket": {
  "file": "3f9a0c1d2e4b5a69.messages.jsonl",
  "compression": "permessage-deflate",
  "messages": 42,
  "storedMessages": 42,
  "bytes": 162380,
  "storedBytes": 498112
}
```

- `messages` counts the messages both sides sent and `storedMessages` those in
  `file`. `bytes` counts what crossed the proxy after the handshake, and
  `storedBytes` is the size of `file`. `truncated: true` marks a WebSocket not
  stored whole. `error` says why the frames of one side could no longer be
  followed; its later messages are not captured.
- `compression` is `permessage-deflate` when the handshake response accepted
  that extension and no other; its messages are stored decoded.
- In `<id>.open.json`, `open` is `true` and the counts are not filled in yet.

Each line of the messages file is one message:

```json
{"ts":"2026-10-21T10:11:13.204518Z","from":"client","type":"text","bytes":812,"storedBytes":2304,"decodedFrom":"permessage-deflate","text":"{\"type\":\"response.create\", ...}"}
```

- `from` is `client` or `server`; `type` is `text`, `binary`, `close`, `ping`
  or `pong`. A fragmented message is stored once, reassembled; control frames
  sent between its fragments come before it.
- `ts` is when the message's last byte was relayed. Each side's messages are in
  the order that side sent them. Between the two sides, lines follow the order
  they were stored in, which can lag the relay, so sort by `ts` for the order
  across both.
- The payload is in `text` for text messages and close reasons that are valid
  UTF-8, and in `base64` otherwise. A close also carries its status `code`.
- `bytes` counts the payload as relayed, compressed if it was, and
  `storedBytes` the payload stored. `decodedFrom` marks a payload stored
  decoded. `compressed: true` marks one stored as relayed because it could not
  be decoded: it was compressed by another extension, it was corrupt, or an
  earlier message too large to decode left the decoder without the history
  later messages refer to. `truncated: true` marks a payload cut at the size
  cap or by the end of the connection.

### Size limits and retention

- Each body and each WebSocket message is stored up to 8 MiB and marked
  `truncated` beyond that; the other side still receives all of it. For a
  decoded body or message the cap applies to the decoded bytes, and decoding
  stops there, unless later messages may refer back into a compressed one: it
  is then decoded on to at most 64 MiB, so that they still decode.
- The messages file of a WebSocket stops growing at 128 MiB. Further messages
  are counted but not stored, and the WebSocket is marked `truncated`, so a
  long-lived connection keeps its start.
- Streamed responses such as server-sent events, and WebSocket messages, are
  written to the capture as they are relayed. Each chunk reaches the other side
  before it is written to the capture, but the next chunk is read only
  afterwards, so a slow disk slows the stream down.
- A session directory keeps its most recent captures within 512 MiB, deleting
  the oldest to make room. Bodies and messages count against that budget as
  they are written, so when captures still in flight, open WebSockets included,
  fill it on their own, the rest of their bodies and messages is not stored and
  they are marked `truncated`.
- At session start the store of the project and tool is pruned to 1 GiB,
  oldest captures first, across all sessions. Directories are left in place, so
  a running session keeps writing. Files that a gateway stopped in the middle
  of a capture left without a record are removed once nothing has written to
  them for a day.
- `enclave cleanup` removes the capture store of the project and tool unless
  `--keep history` is given, and `enclave cleanup --all` removes every store.
  To drop captures without a cleanup, delete files in a session directory;
  deleting the directory of a running session stops its capture until the
  session starts again.

### What full capture cannot see

Full capture covers the same traffic as `requests` mode, the HTTP requests the
proxy terminates, and the WebSocket connections among them:

- Traffic inside other upgraded connections (SPDY for `kubectl exec`, h2c) is
  relayed but not captured; only the handshake is. WebSocket over HTTP/2
  (RFC 8441) is not captured either.
- WebSocket messages compressed by an extension other than
  `permessage-deflate`, or that cannot be decoded, are stored as relayed.
- SSH on port 22 bypasses the proxy and is never recorded.
- Clients that pin certificates or use their own trust store fail in this mode
  instead of being captured.
- With `--allow-all-network`, policy mode `unrestricted`, or the `qemu` backend
  there is no gateway, so nothing is captured.

### Reading captures

`enclave network log --show-capture <id>` prints the request and the response
as HTTP messages, each followed by the size and location of its body file. It
takes the scope flags of `network log` (`--tool`, `--session`, `--all-running`)
and cannot be combined with `--follow`, `--summary`, `--since`, `--verdict`,
`--domain` or `--type`. For a WebSocket it then lists the messages, `>` for
what the client sent and `<` for what the server sent, after a note with their
count and the messages file; one still open is shown from its open record with
the messages so far. Control characters in headers, bodies and messages are
replaced, so a capture cannot drive the terminal. The gateway can write into its session
directory, so only regular files are read from there: a symlink, which could
point at any file you can read, is refused. `--json` emits the record file on one
line; read the bodies and messages from their files. The files can also be read
directly, for example with `jq . <id>.json`, or
`jq -r 'select(.from == "client") | .text' <id>.messages.jsonl` for what an
agent sent over a WebSocket. HAR export is not supported.

## Managing the Network Policy

Use the `network` subcommand to inspect and modify network policy without editing files manually:

```bash
enclave network status                     # Show network policy status
enclave network print                      # Print effective dnsmasq config
enclave network diff                       # Show changes from built-in defaults
enclave network add-domain example.com --global     # Allow a domain
enclave network remove-domain example.com --global  # Remove a domain
enclave network set-mode unrestricted --global      # Or: restricted
enclave network apply                      # Apply policy to running gateways
```

`status`, `print`, and `diff` show the policy a run with the same options would enforce: tool allowlist and spec domains, enabled features' allowed and denied domains, credential release hosts, `allow_domains`, and `allow_all_network`.

- They accept the run flags that affect the policy (see the [CLI reference](cli-reference.md#network)), e.g. `enclave network print --features +github-cli --allow-domain api.example.com`.
- Configured options such as `devcontainer` or `backend: qemu` are honoured; `print` and `diff` take no `--backend`, so preview `run --backend qemu` with `--allow-all-network`.
- Sessions started with `enclave devcontainer run|shell` cannot be previewed: devcontainer mode has no flag, and it drops the default features.
- Hosts selected at session start through `serviceAuth.hostsFromCredential` are not shown.
- In unrestricted mode `print` emits only `# Mode: unrestricted`.
- If the options are invalid, `print` and `diff` fail (`print` also rejects an unknown tool), while `status` warns and summarizes the tool spec and policy files.
- `apply` and the running-gateway comparison in `status` use the tool spec and policy files only, not the options a session recorded.

Network mutations are currently global-only. `--project` scope is planned but not yet supported.

Mutating commands (`add-domain`, `remove-domain`, `set-mode`) apply the updated policy to the running gateways of the current project automatically, for every tool, so they do not accept `--tool`. Pass `--no-apply` to persist the change without applying it, or `--all-running` to target every running gateway on the host. Run `enclave network apply` (optionally with `--tool` or `--all-running`) to push the persisted policy to the current project/tool's gateways on demand. Changing a running session's destination mode requires a restart. Same-mode
reload is supported for both restricted gateways and protected unrestricted
gateways. Apply reads the active gateway's private metadata, not another
session's host bundle. Status likewise compares each gateway's installed bundle
with the policy it should enforce; a gateway too old to report it shows as
`UNKNOWN` until its session restarts. A session started with `--allow-all-network` retains that
explicit override across reloads, even when the saved policy is restricted;
restart without the flag to adopt the saved mode.


### JSON output (`schemaVersion: "1"`)

`enclave network print --json` emits the same allowlist as a versioned envelope instead of the dnsmasq config:

```json
{
  "schemaVersion": "1",
  "kind": "network-policy",
  "tool": "claude",
  "project": { "dir": "/home/you/src/app", "hash": "e9671acd2448" },
  "mode": "restricted",
  "domains": ["anthropic.com", "api.anthropic.com", "github.com"],
  "denied": ["telemetry.example.com"],
  "notes": ["Hosts selected at session start through serviceAuth.hostsFromCredential are not listed."],
  "error": ""
}
```

- `domains`: the allow set the gateway renders, canonical and sorted, without entries equal to or under a denied domain. `denied`: the blackholed domains, sorted.
- `mode` is `restricted` or `unrestricted`; in unrestricted mode `domains`, `denied`, and `notes` are empty.
- `project` is `null` when the project cannot be resolved. Arrays are never `null`. Fields are only added, never renamed or removed.

If the policy cannot be resolved (no or unknown tool, options a run would reject, a malformed spec, policy, or defaults file), stdout carries one envelope with `error` set, `mode: null`, and empty arrays (`tool` is `null` for startup failures), and the exit code is 1; unlike `config --json`, there is no result to report. Usage errors go to stderr only (exit 1, empty stdout), as do warnings.

## Adding Custom Domains

Add custom domains through global `~/.config/enclave/network.jsonc`,
`--allow-domain`, or the `allow_domains` config key (global or project).

### Global policy inheritance

A project policy at `~/.config/enclave/projects/<hash>/network.jsonc` can set
`"inherit_global_policy": false` to exclude domain entries from both
`domains.global` and `domains.tools` in the global policy. The project's own
entries still apply. This switch does not change mode, resolver, built-in tool
allowlist, or extension-domain precedence.

### Per-run domains

Use `--allow-domain <domain>` (repeatable or comma-separated) to add domains to the gateway allowlist for a single run only. The flag does **not** mutate `~/.config/enclave/network.jsonc` or any project file; it just augments the gateway's in-memory policy for the current container. It replaces `allow_domains` from config for the run; `--allow-domain +api.example.com` adds to them and `--allow-domain -api.example.com` drops one.

```bash
enclave --allow-domain api.deepseek.com --allow-domain api.example.com
enclave --allow-domain api.deepseek.com,api.example.com
```

A domain that a deny rule blocks stays blocked; the session start reports it separately from the allowlisted ones.

On the Docker backend, `--allow-domain` is inert when combined with `--allow-all-network`: destination allowlists are inactive (a protected session still runs its
credential-mediating gateway). The QEMU backend rejects `--allow-domain` because it cannot enforce restricted egress. Bare DNS names only — schemes, paths, ports, and wildcards are rejected.

The same key works in config: `"allow_domains": ["api.deepseek.com"]` in global `~/.config/enclave/config.json`, or in project `~/.config/enclave/projects/<hash>/config.json` for one project.

## Overriding the Main Allowlist

Replace the built-in allowlist entirely without rebuilding the image:

- Global: `~/.config/enclave/gateway-allowlists/<tool>.conf`
- Per-project: `~/.config/enclave/projects/<project-hash>/gateway-allowlists/<tool>.conf`

Project overrides take precedence over global. These files replace the built-in allowlist; use standard dnsmasq `server=` or `conf-file=` lines (referencing `/etc/dnsmasq.allowlists/...`).

The built-in allowlists live in `runtime-assets/gateway-allowlists/` in the repo and are baked into the container image at build time.

Without an override, the tool's own `gateway-allowlist.conf` applies. It is resolved from `~/.config/enclave/extensions/tools/<tool>/` first and from the built-in extension tree second, so a user-installed tool extension enforces the allowlist it ships. A tool that declares none falls back to `base.conf`, which allows more domains than a tool-specific allowlist.

## Port Direction: `-p` vs `--bridge-port`

These two flags handle opposite directions of port forwarding:

- **`-p <port>`** — Publishes a **container** port to the **host** (container → host). Use this when the agent starts a service inside the container (e.g. a dev server on port 3000) and you want to access it from your host browser.

  Accepts Docker's publish forms: `3000` (host `3000` → container `3000`), `8080:80` (host:container), and `127.0.0.1:8080:80` (explicit host-IP). A host port of `0` — e.g. `-p 0:3000` or `-p 127.0.0.1:0:3000` — asks the daemon to assign a free host port at runtime, which avoids collisions when many sessions publish the same container port. The assigned port is printed once the session starts and is discoverable with `enclave ps --json` (each session lists its `ports` bindings). Auto-assigned host ports are Docker-only; the experimental QEMU backend rejects a host port of `0`.

- **`--bridge-port <port>`** — Forwards a **host** port into the **container** (host → container). Use this when you have a service running on your host (e.g. an MCP server on port 9800) and the agent needs to reach it at `localhost:9800` from inside the container.

## Per-session Docker networks

Each Docker session gets its own labeled bridge network, in restricted and
unrestricted modes. Normally only one network namespace is attached: the
gateway for a restricted session, or the tool container for an unrestricted
session. The bridge and subnet exist while the session container exists; a
stopped background session keeps its network until `enclave stop`. Once the
session has exited, its gateway is stopped, releasing the published ports: by
`enclave attach` if attached, otherwise at the next session start or `enclave
cleanup --ephemeral`. Under podman the stopped gateway
container stays until the session is removed. Inspect networks through
structured output:

```bash
enclave ps --json
# ... "network": {"name": "...", "subnet": "172.30.0.0/28"}
```

Restricted gateways also drop unsolicited inbound IPv4 and IPv6 traffic, even
if a peer can route to the session. Only loopback, established/related replies,
and published container ports are admitted. OAuth callback forwarding remains
available through its published port; DNS and proxy listeners are not exposed
to peers unless explicitly published. Unrestricted sessions have no gateway
inbound filter.

Docker 29 and newer allocate a small dynamic IPv4 prefix for each session. On
older daemons, Docker's default allocation applies and the stock address pools
can limit the number of concurrent networks. If pool allocation is exhausted,
upgrade the daemon or configure Docker's `default-address-pools`.

Under `--backend podman` the same networks require netavark and are created with
`isolate=strict`, since netavark isolates bridge networks from each other only
on request before podman 6. Older netavark versions or Podman CLIs that reject
`strict` fall back to `isolate=true` with a warning: non-isolated networks can
still route to the session, but restricted gateways retain their inbound filter.
Podman's default pool hands out a `/24` per network
and is not subject to the Docker pool limit. Published ports still bind
`127.0.0.1` explicitly on both engines.

Teardown waits for asynchronous session-container removal, common with Podman.
Waits exceeding one second report the reason and retry limit. The limit counts
retries, not elapsed time; engine commands can extend the wait.

A network that outlives its session (for example after a `kill -9` of the
enclave process) is removed at the next session start, or by
`enclave cleanup --ephemeral`, once it is an hour old and has no endpoints.

Container-to-container access is not a compatibility contract. As an advanced,
unsupported integration, `docker network connect <network-name> <peer-container>`
can deliberately attach a peer to the network named in the `enclave ps --json`
output. Enclave does not manage or authorize that peer, and the network cannot
be removed until it disconnects. Restricted gateways still require a published
port for the peer to reach a session listener.

### Migrating from the default bridge

- Podman hosts using CNI must switch to netavark before starting sessions.
- On Linux Docker Engine, existing `docker0`-specific firewall rules for
  `--bridge-port` and the IDE bridge no longer match. Replace them with rules
  for each session's bridge and subnet; see [host service configuration](#linux-host-service-configuration).
- Session startup now requires a successful `docker info` or `podman info`
  query to select network options. Previously, failure of this query only
  suppressed configuration warnings.

## Bridging Host Ports

`--bridge-port` forwards host-side services into the container on `localhost`.
Restricted sessions configure DNAT in the gateway sidecar, with a userspace
proxy fallback when DNAT is unavailable. Unrestricted sessions run the
userspace proxy in the tool container. The automatic IDE bridge follows the
same placement and discovers VS Code extension ports from
`~/.claude/ide/*.lock` files.

```bash
enclave --bridge-port 9800                       # Single port
enclave --bridge-port 9800,9801                  # Comma-separated
enclave --bridge-port 9800 --bridge-port 9801    # Repeated flag
```

Or set them in config:

```json
{
  "bridge_ports": ["9800", "9801"]
}
```

Explicit bridge ports are merged with any auto-discovered IDE ports and deduplicated.

### Linux: host service configuration

On Linux with Docker Engine, `host-gateway` resolves to the gateway address of
Docker's default bridge (typically `172.17.0.1`), while traffic originates from
the session's dedicated bridge interface and subnet. Consequently:

1. The host service must bind to the default-bridge gateway address, not
   `127.0.0.1`.
2. The host firewall must allow the session subnet on the session bridge
   interface.

Docker Desktop routes `host.docker.internal` through its VM and can reach host
loopback services directly, so these Linux Engine steps do not apply there.

Start the session first, then select its structured network entry. The network
is ephemeral, so capture these values while the session is running:

```bash
SESSION_NAME=enclave-codex-abc123abc123-main
SESSION_NETWORK=$(enclave ps --json | jq -r --arg name "$SESSION_NAME" '.[] | select(.name == $name) | .network.name')
SESSION_SUBNET=$(enclave ps --json | jq -r --arg name "$SESSION_NAME" '.[] | select(.name == $name) | .network.subnet')
NETWORK_ID=$(docker network inspect "$SESSION_NETWORK" --format '{{.Id}}')
SESSION_INTERFACE="br-${NETWORK_ID:0:12}"
HOST_GATEWAY=$(docker network inspect bridge --format '{{(index .IPAM.Config 0).Gateway}}')
```

Bind the service to `$HOST_GATEWAY`; do not use `0.0.0.0`, which also exposes
it on external-facing interfaces:

```bash
my-mcp-server --host "$HOST_GATEWAY" --port 9800
```

If a host firewall blocks the traffic, allow only the session source and
destination. For UFW:

```bash
sudo ufw allow in on "$SESSION_INTERFACE" from "$SESSION_SUBNET" to "$HOST_GATEWAY" port 9800 proto tcp

# Remove the rule when the bridge is no longer needed.
sudo ufw delete allow in on "$SESSION_INTERFACE" from "$SESSION_SUBNET" to "$HOST_GATEWAY" port 9800 proto tcp
```

Both the interface and subnet change with each session, so recreate this UFW
rule after every start. A stable rule requires a broader policy: configure a
known Docker `default-address-pools` range, then use iptables or nftables to
match the `br-` interface prefix and that source range. Such a rule also
admits non-Enclave Docker bridges allocated from the range; use the per-session
rule when those containers are not equally trusted.

The equivalent rule for another firewall permits the session subnet on the
session bridge interface to reach only the default-bridge gateway and required
TCP port. Inside the session, the service remains available at
`localhost:9800` through the DNAT bridge.
