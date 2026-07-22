# Networking

## How It Works

By default, network access is restricted via a gateway sidecar (dnsmasq + transparent proxy). DNS only resolves allowlisted domains and the proxy enforces Host/SNI against the same allowlist. This prevents agents from making arbitrary outbound requests.

Coarse pass/deny audit events are logged to `~/.local/state/enclave/projects/<project-hash>/<tool>/logs/network.log`.

For request-level logging, enable:

```bash
enclave --network-log=requests
```

This forces allowlisted HTTPS traffic through the gateway MITM proxy so the gateway can emit HTTP-style request audit events for both HTTP and HTTPS. Some clients that pin certificates or use custom trust stores may fail in this mode.

To disable all restrictions:

```bash
enclave --allow-all-network
```

The experimental `qemu` backend currently has no restricted-egress implementation, so it always runs with all outbound network allowed. Selecting it implies `--allow-all-network` automatically and prints a notice; passing `--allow-domain` (which would require restricted egress) is rejected.

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

## Port Direction: `-p` vs `--bridge-port`

These two flags handle opposite directions of port forwarding:

- **`-p <port>`** — Publishes a **container** port to the **host** (container → host). Use this when the agent starts a service inside the container (e.g. a dev server on port 3000) and you want to access it from your host browser.

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
