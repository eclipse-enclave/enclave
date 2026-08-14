# Networking

## How It Works

By default, network access is restricted via a gateway sidecar (dnsmasq + transparent proxy). DNS only resolves allowlisted domains and the proxy enforces Host/SNI against the same allowlist. This prevents agents from making arbitrary outbound requests.

Pass/deny audit events are logged to `~/.local/state/enclave/projects/<project-hash>/<tool>/logs/network.log`. Read them with [`enclave network log`](#reading-the-network-log). The default `coarse` mode records one event per TLS connection rather than one per request, so read [Coverage and granularity](#coverage-and-granularity) before drawing conclusions from what the log does or does not contain.

For request-level logging, enable:

```bash
enclave --network-log=requests
```

This forces allowlisted HTTPS traffic through the gateway MITM proxy so the gateway can emit HTTP-style request audit events for both HTTP and HTTPS, instead of one event per TLS connection. Some clients that pin certificates or use custom trust stores may fail in this mode.

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
still available. `--session <container>` reads one running session's events and
`--all-running` merges every running gateway's log in timestamp order; both need
Docker, the default scope does not. Concurrent sessions of the same project and
tool append to one file, so `--session` also filters on the `session` field:
events written before session stamping existed carry none and are not shown.

`--since` takes a duration (`10m`), an RFC3339 timestamp, or `session`, which
resolves to the start of the most recent session in scope and therefore needs a
scope covering exactly one session. Because it anchors on a session boundary it
also limits the output to that session's events, so a concurrent session sharing
the file does not bleed in. Combined with `--session` it resolves to that
session's own start.

`--json` and `--summary` are mutually exclusive: `--json` is the event stream
contract and a summary is not an event stream. Use `--summary --plain` for a
machine-readable aggregate.

Output adapts to the destination: a terminal gets the aligned, coloured form and
a pipe or redirect gets tab-separated columns in a fixed order (timestamp,
verdict, type, method, domain, path, status, req_bytes, resp_bytes, rule,
session, with `-` for absent values). `--plain` forces the machine form in a
terminal, and `NO_COLOR` or `ENCLAVE_COLOR=never` does the same.

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

At session start, a log larger than `network_log_max_size` (default `32MB`) is
copied to `network.log.1`, replacing any previous generation, and then truncated
in place. The reader reads `.1` first, so the boundary is invisible. Truncating
rather than renaming keeps the file a running gateway has bind-mounted, so a
session that is already going on keeps writing where readers can see it. A
session never rotates its own log, so a single long-running session can grow past
the cap, and worst-case disk use is roughly twice the cap per project and tool.
Concurrent session starts serialize on a `network.log.lock` file next to the log,
so two of them cannot discard the generation the other just wrote. Set
`network_log_max_size` to `0` or `off` in global config to disable rotation.

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

## Bridging Host Ports

`--bridge-port` uses DNAT forwarding through the gateway sidecar to make host-side services accessible inside the container on `localhost`. This is the same mechanism used by the automatic IDE bridge, which discovers VS Code extension ports from `~/.claude/ide/*.lock` files.

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

On Linux with Docker Engine, bridged traffic reaches the host via the Docker bridge network (e.g. `docker0`), not the loopback interface. This has two implications:

1. **The host service must bind to the Docker bridge IP**, not `127.0.0.1`.
2. **The host firewall must allow traffic** from the Docker bridge network.

This is not an issue on macOS or Windows where Docker Desktop routes `host.docker.internal` through its VM, transparently reaching host loopback services.

#### Step 1: Find the Docker bridge IP

```bash
docker network inspect bridge --format '{{(index .IPAM.Config 0).Gateway}}'
```

This is typically `172.17.0.1`. Binding to this address keeps the service off external-facing interfaces while making it reachable from containers. Do **not** bind to `0.0.0.0` — that exposes the service on all network interfaces, including external ones.

#### Step 2: Bind the host service

Configure the host service to listen on the Docker bridge IP. For example, for an MCP server:

```bash
# Instead of binding to 127.0.0.1:
my-mcp-server --host 127.0.0.1 --port 9800   # ✗ unreachable from container

# Bind to the Docker bridge IP:
my-mcp-server --host 172.17.0.1 --port 9800   # ✓ reachable from container
```

#### Step 3: Allow traffic through the firewall

If the host runs a firewall (e.g. UFW), it will block traffic from containers by default. Container traffic arrives on the `docker0` interface, not `lo` (loopback), so the standard loopback-allow rule does not apply.

**UFW** — open a specific port:

```bash
SUBNET=$(docker network inspect bridge --format '{{(index .IPAM.Config 0).Subnet}}')
GW=$(docker network inspect bridge --format '{{(index .IPAM.Config 0).Gateway}}')

# Allow container traffic to a specific port
sudo ufw allow in on docker0 from "$SUBNET" to "$GW" port 9800 proto tcp

# Remove the rule when no longer needed
sudo ufw delete allow in on docker0 from "$SUBNET" to "$GW" port 9800 proto tcp
```

Rules take effect immediately — no restart required. To list current rules:

```bash
sudo ufw status numbered
```

**Other firewalls** — the equivalent rule allows TCP traffic on the `docker0` interface from the Docker bridge subnet (typically `172.17.0.0/16`) to the gateway IP (typically `172.17.0.1`) on the target port.

#### Putting it all together

```bash
# 1. Determine the Docker bridge IP
BRIDGE_IP=$(docker network inspect bridge --format '{{(index .IPAM.Config 0).Gateway}}')

# 2. Start the host service on the bridge IP
my-mcp-server --host "$BRIDGE_IP" --port 9800 &

# 3. Open the firewall (UFW example)
SUBNET=$(docker network inspect bridge --format '{{(index .IPAM.Config 0).Subnet}}')
sudo ufw allow in on docker0 from "$SUBNET" to "$BRIDGE_IP" port 9800 proto tcp

# 4. Start enclave with the bridge port
enclave --bridge-port 9800

# Inside the container, the service is reachable at localhost:9800
```
