# Networking

How Enclave restricts outbound network access, how to allow more domains,
forward ports in both directions, and read the network audit log.

## How it works

Two settings shape a session's network:

- The network mode is the egress policy: `restricted` (the default) allows
  only allowlisted domains, `unrestricted` allows everything.
- The gateway is the sidecar container (dnsmasq and a transparent proxy) whose
  network namespace the session shares. It writes the network log in both
  modes and enforces the policy in restricted mode.

In restricted mode, DNS resolves only allowlisted domains, and the proxy checks
the HTTP `Host` header or TLS SNI against the same allowlist. Pass and deny
decisions are logged to
`~/.local/state/enclave/projects/<project-hash>/<tool>/logs/network.log`
([reading the log](#reading-the-network-log)). See
[Network request flow](runtime/network-request-flow.md) for the request path
and [Security boundaries](security/README.md#network-boundary) for the limits.

| Mode | Selected by | Effect |
|------|-------------|--------|
| Restricted | Default | Gateway enforces the allowlist |
| Unrestricted | `--allow-all-network`, `allow_all_network` config key, or `enclave network set-mode unrestricted --global` | All outbound traffic allowed; the gateway only logs ([Unrestricted mode](#unrestricted-mode)) |

## Unrestricted mode

```bash
enclave --allow-all-network
```

An unrestricted session still starts a gateway, which only observes. It
installs no inbound or outbound filters, leaves IPv6 and Docker's DNS resolver
untouched, and connects each redirected connection to the address the client
dialled, so `/etc/hosts` entries, split DNS, and IP-pinned endpoints behave as
without a gateway. It adds no headers to the requests or responses it parses,
sets no response-header timeout, and passes responses to the client with their
original encoding. Declared secrets are injected with their real values; the
gateway releases none.

TCP ports 80 and 443 go through the proxy and are logged as in restricted
mode, with the rule `unrestricted`:

- Plaintext HTTP gets an `http` event per request; HTTPS gets a `tcp` event per
  connection, or an `http` event per request with `--network-log=requests`.
- TLS without SNI and non-HTTP traffic on these ports, meaning anything that
  does not open with an HTTP/1 request line, are passed through and logged as
  a `tcp` event without a domain; so is a first line longer than 8 KiB.
- Requests for an explicit proxy on port 80, such as the `CONNECT` sent for
  `HTTPS_PROXY=http://proxy:80` or an absolute-form `GET http://host/`, reach
  that proxy unchanged and are logged as a `tcp` event for the requested host.
- A connection made straight to the gateway's proxy ports has no destination:
  an HTTP request to 15080, or to an intercepted host on 15443, gets `400` with
  the rule `invalid-host`; other TLS on 15443 is passed through to its SNI host
  on port 443; anything else is closed and logged with the rule
  `upstream-error`.
- Everything else passes unlogged: other TCP ports (SSH included), UDP
  (including QUIC/HTTP3 and DNS), and IPv6 web traffic when the gateway cannot
  set up IPv6 NAT. There are no `dns` events, since the gateway does not
  resolve names. A quiet unrestricted log is not a quiet session.

Costs:

- The gateway image is built on first use, and every session start waits for
  the gateway.
- If the gateway cannot start, the session does not start; the error suggests
  `--disable-gateway`. With `--no-rebuild` and no gateway image, the session
  fails the same way.
- If the proxy inside the gateway dies, the gateway removes its redirects and
  traffic flows unlogged. If the gateway container stops, the session loses
  network access, because it shares the gateway's network namespace.
- The gateway's proxy listens on loopback ports 15080 and 15443 in the
  namespace the session shares, so a server inside the session cannot use those
  ports, and the session refuses to start if `-p` publishes one of them as a
  container port. Use another container port, or `--disable-gateway`.
  Restricted sessions have the same limit. The proxy listens only on loopback,
  so publishing another container port never reaches it.

To run without a gateway, and without a network log:

```bash
enclave --allow-all-network --disable-gateway   # Config key: "disable_gateway": true
```

This is the unrestricted session of earlier releases: the tool container is
attached to the session network directly. `--disable-gateway` requires
unrestricted mode and never implies it; in restricted mode the run fails.

The experimental `qemu` backend has no restricted egress and no gateway, so it
always runs unrestricted: selecting it implies `--allow-all-network` and
`--disable-gateway` with a notice, and `--allow-domain` is rejected.

## Adding custom domains

| Scope | How |
|-------|-----|
| One run | `--allow-domain <domain>` (repeatable or comma-separated) |
| All projects | `allow_domains` in `~/.config/enclave/config.json`, or `enclave network add-domain <domain> --global` (writes `~/.config/enclave/network.jsonc`) |
| One project | `allow_domains` in `~/.config/enclave/projects/<hash>/config.json` |

```bash
enclave --allow-domain api.deepseek.com,api.example.com
```

- `--allow-domain` changes only the running gateway's policy, not policy or
  project files. It replaces `allow_domains` for the run; `+api.example.com`
  adds to the configured list and `-api.example.com` drops one.
- Only bare DNS names are accepted, without schemes, paths, ports, or
  wildcards.
- A domain blocked by a deny rule stays blocked; session start reports it
  separately.
- It has no effect with `--allow-all-network`: unrestricted mode has no
  allowlist to extend, whether or not a gateway runs.

A project policy at `~/.config/enclave/projects/<hash>/network.jsonc` can set
`"inherit_global_policy": false` to ignore the domain entries of
`domains.global` and `domains.tools` in the global policy. The project's own
entries still apply; mode, resolver, built-in tool allowlist, and
extension-domain precedence are unchanged.

## Overriding the main allowlist

These files replace the built-in allowlist without rebuilding the image; the
project file takes precedence:

- Global: `~/.config/enclave/gateway-allowlists/<tool>.conf`
- Project: `~/.config/enclave/projects/<project-hash>/gateway-allowlists/<tool>.conf`

Use dnsmasq `server=` or `conf-file=` lines that reference
`/etc/dnsmasq.allowlists/...`. Without an override, the tool's own
`gateway-allowlist.conf` applies, read from
`~/.config/enclave/extensions/tools/<tool>/` before the built-in extension tree
(so a user-installed tool enforces the allowlist it ships). A tool without one
falls back to the broader `base.conf`. The built-in allowlists live in
`runtime-assets/gateway-allowlists/` and are baked into the image.

## Managing the network policy

```bash
enclave network status                              # Policy status and sync state of running gateways
enclave network print                               # Effective dnsmasq config
enclave network diff                                # Changes from built-in defaults
enclave network add-domain example.com --global     # Allow a domain
enclave network remove-domain example.com --global  # Remove a domain
enclave network set-mode unrestricted --global      # Or: restricted
enclave network apply                               # Apply policy to running gateways
```

`status`, `print`, and `diff` show the policy a run with the same options would
enforce: tool allowlist and spec domains, features' allowed and denied domains,
credential release hosts, `allow_domains`, and `allow_all_network`. They accept
the run flags that shape it ([CLI reference](cli-reference.md#network)), for
example `enclave network print --features +github-cli --allow-domain api.example.com`.
`print --json` emits a versioned envelope; see
[`network print --json`](integration.md#network-print---json).

- Configured options such as `devcontainer` or `backend: qemu` are honored.
  `print` and `diff` take no `--backend`; preview `run --backend qemu` with
  `--allow-all-network`.
- Not shown: sessions started with `enclave devcontainer run|shell` (no flag
  selects devcontainer mode, and it drops the default features), and hosts
  chosen at start through `serviceAuth.hostsFromCredential`.
- In unrestricted mode, `print` emits only `# Mode: unrestricted`.
- Invalid options make `print` and `diff` fail (`print` also rejects an unknown
  tool); `status` warns and summarizes the tool spec and policy files.
- `apply` and the running-gateway comparison in `status` use the tool spec and
  policy files only, not the options a session recorded.

Changes are global only; `--project` scope is planned. `add-domain`,
`remove-domain`, and `set-mode` apply the result to running gateways of the
current project, for every tool (so they take no `--tool`). `--no-apply` only
persists; `--all-running` targets every running gateway on the host.
`enclave network apply` (optionally `--tool` or `--all-running`) pushes the
persisted policy on demand.

A running gateway keeps the network mode its session started with, so
`set-mode` reaches a running session only after a restart, in both directions.
`apply` reports gateways of unrestricted sessions as `SKIPPED` and never writes
a policy into them; `status` shows them as `UNRESTRICTED` instead of in sync or
stale.

## Port direction: `-p` vs `--bridge-port`

| Flag | Direction | Example |
|------|-----------|---------|
| `-p <port>` | Container to host | Open the agent's dev server on port 3000 from the host browser |
| `--bridge-port <port>` | Host to container | Let the agent reach a host MCP server at `localhost:9800` |

`-p` takes Docker's publish forms: `3000`, `8080:80` (host:container),
`127.0.0.1:8080:80` (host IP), and a host port of `0` (`0:3000`,
`127.0.0.1:0:3000`) to let the engine pick a free port, which avoids
collisions between sessions. The assigned port is printed at start and listed
under `ports` in `enclave ps --json`. Auto-assigned ports are Docker-only; the
`qemu` backend rejects port `0`. Without a host IP, ports bind `127.0.0.1`.
Container ports 15080 and 15443 are taken by the gateway proxy in every
session with a gateway, and publishing them is an error; see
[Unrestricted mode](#unrestricted-mode).

## Bridging host ports

```bash
enclave --bridge-port 9800,9801    # Or repeat the flag; config key: "bridge_ports": ["9800"]
```

Sessions with a gateway, restricted or unrestricted, use DNAT in the gateway,
with a userspace proxy as fallback; sessions started with `--disable-gateway`
run the proxy in the tool container. The
automatic IDE bridge works the same way, discovers VS Code extension ports from
`~/.claude/ide/*.lock`, and is merged and deduplicated with explicit ports.

### Linux: host service configuration

With Docker Engine on Linux, `host-gateway` resolves to the default bridge's
gateway (typically `172.17.0.1`), while traffic comes from the session's own
bridge and subnet. The host service must bind to that gateway address, not
`127.0.0.1`, and the firewall must allow the session subnet on the session
bridge. Docker Desktop reaches host loopback through its VM, so this does not
apply there.

Capture the values while the session runs:

```bash
SESSION_NAME=enclave-codex-abc123abc123-main
SESSION_NETWORK=$(enclave ps --json | jq -r --arg name "$SESSION_NAME" '.[] | select(.name == $name) | .network.name')
SESSION_SUBNET=$(enclave ps --json | jq -r --arg name "$SESSION_NAME" '.[] | select(.name == $name) | .network.subnet')
NETWORK_ID=$(docker network inspect "$SESSION_NETWORK" --format '{{.Id}}')
SESSION_INTERFACE="br-${NETWORK_ID:0:12}"
HOST_GATEWAY=$(docker network inspect bridge --format '{{(index .IPAM.Config 0).Gateway}}')
```

Bind the service to `$HOST_GATEWAY`, not `0.0.0.0`, which also exposes it
externally, and allow only that source and destination (UFW shown):

```bash
my-mcp-server --host "$HOST_GATEWAY" --port 9800

sudo ufw allow in on "$SESSION_INTERFACE" from "$SESSION_SUBNET" to "$HOST_GATEWAY" port 9800 proto tcp

# Remove the rule when the bridge is no longer needed.
sudo ufw delete allow in on "$SESSION_INTERFACE" from "$SESSION_SUBNET" to "$HOST_GATEWAY" port 9800 proto tcp
```

The interface and subnet change every session, so recreate the rule after each
start. A stable rule needs a known Docker `default-address-pools` range and an
iptables or nftables match on the `br-` prefix and that range; it also admits
other Docker bridges from the range, so prefer the per-session rule when those
containers are less trusted. Other firewalls need the equivalent: the session
subnet on the session bridge, to the default-bridge gateway and port only. In
the session, the service stays at `localhost:9800`.

## Reading the network log

```bash
enclave network log                        # Current project and tool
enclave network log --follow               # Stream new events
enclave network log --summary              # Per-domain aggregate
enclave network log --verdict deny         # Only what was blocked
enclave network log --domain '*.github.com' --since 10m
enclave network log --json | jq            # The integration contract
```

The log is read from disk, so exited sessions remain visible. All flags are in
the [CLI reference](cli-reference.md#network).

- `--session <container>` reads one session. It uses Docker when reachable, so
  another project's session can be named; otherwise it reads this project's
  log. A name on no event is an error. Concurrent sessions of a project and tool
  share one file, so it filters on the `session` field; events from before
  session stamping have none and are not shown.
- `--all-running` merges every running gateway's log by timestamp and needs
  Docker.
- `--since` takes a duration (`10m`), an RFC3339 time, or `session`: the start
  of the most recent session in scope, which must cover exactly one session.
  It then shows only that session's events, even if a concurrent session
  shares the file; with `--session` it is that session's start.

Output is aligned text. `--json` alone emits the raw JSONL events; with
`--summary` it emits one JSON object. Events without a host name (such as a TLS
connection denied at the ClientHello) are grouped as `(no domain)`, an empty
`domain` in JSON, so summary totals match `--verdict deny`. Policy denials
(`allowlist` on `http`/`tcp`, `nxdomain` on `dns`) show `blocked by policy` in
the rule column; JSON keeps the raw rule. Color follows `NO_COLOR` and
`ENCLAVE_COLOR` and is off when stdout is not a terminal.

### What is recorded

| Type | Written by | Notes |
|------|-----------|-------|
| `http` | MITM proxy | One per request: method, path (never the query string), status, byte counts. Every plaintext HTTP request; HTTPS only where the proxy terminates TLS |
| `tcp` | MITM proxy | One per TLS connection at the ClientHello: SNI host, verdict, matched rule. Requests inside are not visible |
| `dns` | DNS audit translator | One per denied or failed lookup. `rule`: `nxdomain` (blocked by policy), or `upstream-servfail`, `upstream-refused`, `upstream-nxdomain` |
| `session` | Host, at gateway start | Boundary marker. Not an audit event: excluded from `--summary`, never matched by `--verdict`, `--domain`, or `--type` |

A blocked lookup usually logs two `dns` events (A and AAAA). `NODATA` answers
are not recorded, because dnsmasq returns `NODATA-IPv6` for every allowlisted
host without an IPv6 record.

### Coverage and granularity

The log records decisions and connections, not traffic volume, so a quiet log
does not mean a quiet session:

- Successful DNS lookups are never recorded, and dnsmasq answers repeats from
  its cache.
- In `coarse` mode (default), an HTTPS connection is one `tcp` event; the rest
  is tunneled undecrypted. A long-lived HTTP/2 connection, such as an agent's
  model API connection, logs once for hundreds of requests, while short-lived
  connections (`git fetch`, CLI calls, reconnecting pollers) log each time and
  dominate the log.
- `http` events for HTTPS need TLS termination. In `coarse` mode that happens
  only for hosts of a declared credential's HTTP release rule, and only when
  the credential was resolved. A tool logged in with OAuth instead of an API key
  therefore gets no request-level events for its API host.
- An upgraded connection (WebSocket, SPDY for `kubectl exec`) is one `http`
  event with status `101`, logged when it closes; its traffic is relayed
  uninspected, even with `--network-log=requests`.
- Only ports 80 and 443 go through the proxy. Other ports, such as SSH on 22,
  reach allowlisted IPs directly and are never recorded.
- Unrestricted sessions have further gaps; see
  [Unrestricted mode](#unrestricted-mode).

### Request-level logging

`enclave --network-log=requests` routes all allowlisted HTTPS through the
gateway's MITM proxy, giving one `http` event per request. Clients that pin
certificates or use custom trust stores may fail. The container trusts the
gateway CA through `SSL_CERT_FILE` and `REQUESTS_CA_BUNDLE`
(`/tmp/enclave-ca/ca-certificates.crt`: system bundle plus gateway CA, written
at startup) and `NODE_EXTRA_CA_CERTS`. They are set on the container, so
`docker exec` processes such as an attached IDE backend trust it too. Only the
CA persists on the host; leaf certificates stay in each gateway container.

### Rotation

At session start, a log over 32 MB is copied to `network.log.1` (replacing the
old copy) and truncated in place; the reader reads `.1` first. Truncating keeps
the file a running gateway has bind-mounted. A session never rotates its own
log, so one long session can exceed the cap, and worst-case use is about twice
the cap per project and tool. Concurrent starts serialize on
`network.log.lock`, so neither discards the other's copy.

## Per-session Docker networks

Each session gets its own labeled bridge network, in both modes. The gateway
is attached and the tool shares its namespace; sessions started with
`--disable-gateway` attach the tool container. Inspect it with:

```bash
enclave ps --json
# ... "network": {"name": "...", "subnet": "172.30.0.0/28"}
```

- Restricted gateways drop unsolicited inbound IPv4 and IPv6 traffic. Only
  loopback, established or related replies, and published ports (including
  OAuth callbacks) get in; DNS and proxy listeners stay unexposed. Unrestricted
  sessions have no inbound filter, with or without a gateway.
- Docker 29 and newer allocate a small dynamic IPv4 prefix per session. Older
  daemons use the stock address pools, which can limit concurrent networks; if
  they run out, upgrade or set `default-address-pools`.
- Podman specifics are in
  [Podman per-session networks](backends.md#per-session-networks).

Lifecycle:

- The network lives as long as the session container; a stopped background
  session keeps it until `enclave stop`.
- After the session exits, its gateway is stopped and its published ports are
  released: by `enclave attach` if attached, otherwise at the next session
  start or by `enclave cleanup --ephemeral`.
- Teardown waits for asynchronous container removal (common with Podman).
  Waits over one second report the reason and retry limit; the limit counts
  retries, so engine commands can extend the wait.
- A network left behind (for example after `kill -9` of enclave) is removed at
  the next session start or by `enclave cleanup --ephemeral` once it is an hour
  old and has no endpoints.

Container-to-container access is not a compatibility contract. As an
unsupported integration, `docker network connect <network-name> <peer>` can
attach a peer to the network from `enclave ps --json`. Enclave does not manage
or authorize it, the network cannot be removed until it disconnects, and a
restricted gateway still admits it only on published ports.

Upgrading from releases that used the default bridge: see
[Upgrade notes](install.md#upgrade-notes).
