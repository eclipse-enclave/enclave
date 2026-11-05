# Security boundaries

What Enclave protects against, where its boundaries are, and the risks that
remain. Enclave reduces an agent's access to the host and the network; it is
not a hardened container-escape boundary.

| Engine | Status |
|--------|--------|
| Rootful Docker | Default; apply [host hardening](host-hardening.md) where the workflow allows |
| Rootless Podman | Supported with `--backend podman` ([rootless engines](rootless.md)) |
| Rootless Docker | Not supported ([why](rootless.md)) |
| QEMU | Experimental, no restricted egress ([below](#network-boundary)) |

## Host filesystem

- Enclave refuses to run as root: the agent would run as UID 0, which is host
  root on bind mounts under rootful Docker, and its files would become
  root-owned. Only `--allow-root` or `ENCLAVE_ALLOW_ROOT=1` overrides this, not
  config. [userns-remap](host-hardening.md#enable-userns-remap) limits what
  container UID 0 maps to.
- The project is a writable host bind mount by default, so agent changes are
  real host changes. `--project-mount readonly` makes it read-only and clamps
  writable additional mounts inside it.
- `--worktree-metadata readonly|none` protects or omits linked-worktree gitdir
  and commondir mounts; read-only metadata makes Git writes such as `git add`
  fail.
- Additional directories are mounted only from explicit CLI or config inputs.
  Home and its parents, hidden home entries, per-user application data,
  Enclave's own roots, and the SSH agent socket are refused, read-only or not.
  Per-path exceptions warn on every start, but cannot unlock home, Enclave's
  roots (which would let the agent widen its own policy or add host commands),
  or the agent socket; only `--allow-sensitive-mounts` or
  `ENCLAVE_ALLOW_SENSITIVE_MOUNTS=1` can, never config. Devcontainer and
  `.git`-pointer mounts stay blocked. Rules:
  [Sensitive mounts](../mounts.md#sensitive-mounts).
- Per-project Enclave config lives outside the worktree, keyed by project
  hash, so neither the repository nor a session can write it. It may set any
  option except the [global-only](../configuration.md#global-only) ones, so it
  cannot change binary updates or re-enable a command the user disabled.
- Existing Git config files are read-only, but the rest of the Git metadata is
  writable: an agent can still install hooks or redirect Git to an unprotected
  config, affecting later Git commands on the host. Use
  `--project-mount readonly` when metadata must not change; see
  [Git](../git.md).

## Project-controlled execution

Treat an untrusted repository as executable input and review its devcontainer
and environment files first:

- The project `.env` is loaded into the container environment.
- Devcontainer mode reads the project's `devcontainer.json`; filtered mounts,
  run arguments, and lifecycle commands still shape the container.
- `commands.initFiles` and `files/workspace` of enabled extensions may write
  into the project under their documented overwrite rules.

## Network boundary

Restricted sessions use a gateway sidecar (dnsmasq, iptables and ipset, an
HTTP/TLS proxy) that enforces the domain allowlist by DNS and Host/SNI; see the
[network request flow](../runtime/network-request-flow.md). Limits:

- An allowed IP is reachable on any port; ports other than 80 and 443 skip the
  proxy and the log. Broad CDN allowlists widen the tunneling surface.
- The gateway is privileged (`NET_ADMIN`, `NET_RAW`) and shares the tool's
  network namespace, so a gateway vulnerability can weaken policy.
- The allowlist is destination policy, not content policy: paths, methods,
  bodies, responses, and a service's own proxy or relay features are not
  constrained. Treat every allowlisted service as part of the trust boundary.
- The network log records decisions, not all traffic: in `coarse` mode a reused
  TLS connection is one event, successful DNS lookups are not logged, and
  request detail exists only for plaintext HTTP and MITM'd hosts
  (`--network-log=requests` closes the HTTPS gap). A missing event proves
  nothing; see [Coverage and granularity](../networking.md#coverage-and-granularity).
- The experimental QEMU backend runs unrestricted, without gateway-side
  credential release.

Restricted gateways drop inbound IPv4 and IPv6 by default, admitting only
loopback, established or related replies, and published ports (including
OAuth callbacks). Unrestricted sessions have no such filter.

Each Docker and Podman session has its own bridge network (the gateway attaches
in restricted mode, the tool in unrestricted mode), so other networks and
sessions cannot route to its listeners:

| Engine | Bridge isolation |
|--------|------------------|
| Docker | Always isolated |
| Podman with netavark | `isolate=strict` |
| Older netavark, or a Podman CLI rejecting `strict` | `isolate=true` with a warning; non-isolated Podman networks can still route in |
| netavark's native `firewalld` driver | [None](https://github.com/containers/netavark/blob/main/docs/netavark-firewalld.7.md) |

The gateway's inbound filter still protects restricted sessions in the weaker
cases. Published ports keep the reach of their host binding: Docker Desktop's
`host.docker.internal` can let another container reach a port published on
host loopback. Enclave adds no authentication to services published with `-p`,
so privileged services need their own.

> [!WARNING]
> Isolation and loopback publishing rely on engine-managed firewall rules
> (Docker's on the host, netavark's in Podman's rootless namespace). A Docker
> daemon with `"iptables": false` or `"ip6tables": false` and no equivalent
> rules is unsupported;
> [disabling Docker's firewall integration can expose bridge-container ports](https://docs.docker.com/engine/network/packet-filtering-firewalls/#prevent-docker-from-manipulating-firewall-rules).
> Enclave shows the relevant `docker info` warnings; see
> [host hardening](host-hardening.md).

## Secrets

- Host environment variables pass only when an extension declares them or
  `--pass-env` selects them.
- Credentials with an HTTP release rule appear as placeholders; the gateway
  releases the real value only in the configured header to matching HTTPS
  hosts ([API keys](../auth.md#api-keys)). Credential files and secrets without
  a release rule can hold real values in the config or auth store.
- A configured SSH key is available to every session of every project and
  tool, and SSH on port 22 bypasses the proxy, so pushes never appear in
  `network.log`. See [SSH keys](../auth.md#ssh-keys).
