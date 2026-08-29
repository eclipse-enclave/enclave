# Networking

## How It Works

By default, network access is restricted via a gateway sidecar (dnsmasq + transparent proxy). DNS only resolves allowlisted domains and the proxy enforces Host/SNI against the same allowlist. This prevents agents from making arbitrary outbound requests.

Pass/deny audit events are logged to `~/.local/state/enclave/projects/<project-hash>/<tool>/logs/network.log`. Read them with [`enclave network log`](#reading-the-network-log). The default `coarse` mode records one event per TLS connection rather than one per request, so read [Coverage and granularity](#coverage-and-granularity) before drawing conclusions from what the log does or does not contain.

For request-level logging, enable:

```bash
enclave --network-log=requests
```

This forces allowlisted HTTPS traffic through the gateway MITM proxy so the gateway can emit HTTP-style request audit events for both HTTP and HTTPS, instead of one event per TLS connection. Some clients that pin certificates or use custom trust stores may fail in this mode.

Only the gateway CA persists on the host; leaf certificates stay inside each
gateway container. Upgrades may leave an unused legacy cache at
`~/.local/state/enclave/tls/hosts`. On Linux, its old container-owned permissions
can prevent normal removal; delete it with
`sudo rm -rf ~/.local/state/enclave/tls/hosts`, or use
`podman unshare rm -rf ~/.local/state/enclave/tls/hosts` when rootless podman
last owned it.

To disable all restrictions:

```bash
enclave --allow-all-network
```

The experimental `qemu` backend currently has no restricted-egress implementation, so it always runs with all outbound network allowed. Selecting it implies `--allow-all-network` automatically and prints a notice; passing `--allow-domain` (which would require restricted egress) is rejected.

## Reading the Network Log

```bash
enclave network log                        # Events of the current project and tool
enclave network log --follow               # Stream new events as they arrive
enclave network log --summary              # Per-domain aggregate
enclave network log --verdict deny         # Only what was blocked
enclave network log --domain '*.github.com' --since 10m
enclave network log --json | jq            # The integration contract
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
prints. Colour follows `NO_COLOR` and `ENCLAVE_COLOR`, and is off when stdout is
not a terminal.

### What is recorded

| Type | Written by | Notes |
|------|-----------|-------|
| `http` | MITM proxy | One event per request, with method, path, status and byte counts. Query strings are never logged: only the URL path is recorded. Written for every plaintext HTTP request, and for HTTPS only where the proxy terminates TLS |
| `tcp` | MITM proxy | One event per TLS connection, written at the ClientHello with the SNI host, the verdict and the matched rule. The requests carried inside the connection are not visible |
| `dns` | DNS audit translator | One event per denied or failed lookup, with `rule` naming the condition (`nxdomain` for a domain blackholed by policy, `upstream-servfail`, `upstream-refused` or `upstream-nxdomain` for an upstream failure) |
| `session` | Host, at gateway start | A boundary marker naming the session. Not an audit event: excluded from `--summary` and never matched by `--verdict`, `--domain` or `--type` |

A blocked lookup usually produces two `dns` events, one for the A query and one
for the AAAA query, because the resolver asks for both. `NODATA` answers are not
recorded: dnsmasq returns `NODATA-IPv6` for every allowlisted host without an
IPv6 record, so recording it would report allowed domains as denied.

### Coverage and granularity

The log records policy decisions and connections, not traffic volume. Three
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
  secret was actually resolved for the session. A tool authenticated with an
  OAuth token rather than an API key therefore has no release rule for its API
  host and produces no request-level events for it.

Run the session with `--network-log=requests` to force MITM for every
allowlisted HTTPS host and get an `http` event per request. In either mode, SSH
on port 22 bypasses the HTTP/TLS proxy and is never recorded.

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

Network mutations are currently global-only. `--project` scope is planned but not yet supported.

Mutating commands (`add-domain`, `remove-domain`, `set-mode`) apply the updated policy to running gateways automatically. Pass `--no-apply` to persist the change without applying it, or `--all-running` to target every running gateway on the host instead of just the current project/tool. Run `enclave network apply` (optionally with `--all-running`) to push the persisted policy to running gateways on demand. Persisted unrestricted mode still requires a session restart.

## Adding Custom Domains

Add custom domains through global `~/.config/enclave/network.jsonc`,
`--allow-domain`, or the global `allow_domains` config key.

### Per-run domains

Use `--allow-domain <domain>` (repeatable) to add domains to the gateway allowlist for a single run only. The flag does **not** mutate `~/.config/enclave/network.jsonc` or any project file — it just augments the gateway's in-memory policy for the current container.

```bash
enclave --allow-domain api.deepseek.com --allow-domain api.example.com
```

On the Docker backend, `--allow-domain` is inert when combined with `--allow-all-network`: the gateway is not running, so there is no allowlist to extend. The QEMU backend rejects `--allow-domain` because it cannot enforce restricted egress. Bare DNS names only — schemes, paths, ports, and wildcards are rejected.

The same key works in **global** config: `"allow_domains": ["api.deepseek.com"]` in `~/.config/enclave/config.json`. In **project** config (`~/.config/enclave/projects/<hash>/config.json`) it is ignored with a warning — project configs cannot widen the network allowlist. Use `--allow-domain` or global config instead.

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
session. The bridge and subnet exist only while the session runs. Inspect them
through structured output:

```bash
enclave ps --json
# ... "network": {"name": "...", "subnet": "172.30.0.0/28"}
```

Docker 29 and newer allocate a small dynamic IPv4 prefix for each session. On
older daemons, Docker's default allocation applies and the stock address pools
can limit the number of concurrent networks. If pool allocation is exhausted,
upgrade the daemon or configure Docker's `default-address-pools`.

Container-to-container access is not a compatibility contract. As an advanced,
unsupported integration, `docker network connect <network-name> <peer-container>`
can deliberately attach a peer to the network named in the `enclave ps --json`
output. Enclave does not manage or authorize that peer, and the network cannot
be removed until it disconnects.

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
