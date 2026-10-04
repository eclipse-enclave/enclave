# Security boundaries

Enclave reduces an agent's access to the host and network; it is not a hardened
container escape boundary. The supported Docker backend uses a rootful daemon.
Use [host hardening](host-hardening.md) where compatible with the required
workflow. Rootless Docker is [not supported](rootless.md); rootless podman is, through `--backend podman`.

## Host filesystem

- Enclave refuses to run as root: the agent would then run as UID 0, which is
  host root on bind-mounted directories under rootful Docker, and files it
  writes would become root-owned. `--allow-root` or `ENCLAVE_ALLOW_ROOT=1`
  overrides this; no config file can. [userns-remap](host-hardening.md) limits
  what container UID 0 maps to on the host.
- The project is a host bind mount and is writable by default. Agent changes are
  real host changes.
- `--project-mount readonly` makes the project/worktree read-only and clamps
  writable additional mounts inside that subtree to read-only.
- `--worktree-metadata readonly|none` protects or omits linked-worktree
  gitdir/commondir mounts independently of the working tree. With read-only Git
  metadata, in-container Git writes such as `git add` fail.
- Additional host directories are explicit CLI/config inputs. Enclave refuses
  to mount a project or additional directory that is the home directory or one
  of its parents, or that overlaps a hidden home entry, a per-user application
  data directory, enclave's own roots, or the SSH agent socket, read-only or
  not. `--allow-sensitive-mounts` or `ENCLAVE_ALLOW_SENSITIVE_MOUNTS=1`
  overrides this; no config file can, and devcontainer or `.git`-pointer mounts
  stay blocked. See [Sensitive mounts](../cli-reference.md#sensitive-mounts).
- Per-project Enclave config is keyed by project hash under the host config root,
  outside the worktree. Project-scoped config cannot enable guarded options such
  as unrestricted networking or writable project mounts.

## Project-controlled execution

- The project `.env` file is loaded into the container environment.
- Devcontainer mode reads project-controlled `devcontainer.json`; filtered
  mounts, run arguments, and lifecycle commands still influence the container.
- `commands.initFiles` and `files/workspace` from enabled extensions may write
  into the mounted project according to their documented overwrite rules.

Treat an untrusted repository as executable input. Review its devcontainer and
environment files before enabling those paths.

## Network boundary

Restricted Docker sessions use a gateway sidecar with dnsmasq, iptables/ipset,
and an HTTP/TLS proxy. DNS and Host/SNI checks enforce the domain allowlist, but
an allowed IP is reachable on arbitrary ports and broad CDN allowlists increase
tunneling surface. The privileged gateway has `NET_ADMIN`/`NET_RAW` and shares
the tool's network namespace; a gateway vulnerability can weaken policy.

Restricted gateways use default-drop inbound IPv4 and IPv6 policies. They admit
only loopback, established/related replies, and published container ports,
including published OAuth callbacks. Unpublished session, DNS, and proxy
listeners remain blocked even if another container has a route into the session.
Unrestricted sessions do not have this gateway filter.

Every Docker and podman session is enclosed in its own user-defined bridge
network. In restricted mode the gateway is the network's attached endpoint and
the tool shares its network namespace; in unrestricted mode the tool is
attached directly. This prevents unrelated bridge networks and other Enclave
sessions from routing directly to listeners in the session. Docker isolates
user-defined bridges unconditionally; under podman netavark is required and the
network is created with `isolate=strict`. Older netavark versions or Podman CLIs
that reject `strict` fall back to `isolate=true` with a warning, which leaves
containers on non-isolated podman networks a route into the session. The native
netavark `firewalld` driver also [does not support bridge isolation](https://github.com/containers/netavark/blob/main/docs/netavark-firewalld.7.md).
The gateway inbound filter still applies to restricted sessions in these cases.
Published ports
still have the reachability requested by their host binding. In particular,
Docker Desktop's `host.docker.internal` route can let another container reach a
port published on host loopback, so privileged services need their own
per-session authentication. Enclave does not add authentication to arbitrary
services published with `-p`.

Bridge separation and loopback publishing depend on engine-managed firewall
rules: Docker's on the host, netavark's inside the rootless network namespace
under podman. A Docker daemon configured with `"iptables": false` or
`"ip6tables": false` without equivalent replacement rules is outside the
supported security posture; Docker documents that [disabling its firewall integration can expose
bridge-container ports](https://docs.docker.com/engine/network/packet-filtering-firewalls/#prevent-docker-from-manipulating-firewall-rules).
Enclave surfaces relevant `docker info` warnings when the daemon reports them.
See [host hardening](host-hardening.md).

The allowlist is destination policy, not content policy. It does not constrain
URL paths, methods, request bodies, responses, or an upstream service's own
proxy and relay features. An allowlisted API, package registry, or other service
can therefore return untrusted content or provide indirect access beyond what
its hostname suggests. Treat every allowlisted service as part of the trust
boundary.

The network log is an audit trail of decisions, not a record of everything that
crossed the boundary. In the default `coarse` mode a reused TLS connection
yields one event however many requests it carries, successful DNS lookups are
never recorded, and request-level detail exists only for plaintext HTTP and
MITM'd hosts. `--network-log=requests` closes the HTTPS gap for allowlisted
hosts, but no mode records SSH. Treat a missing event as no evidence either
way; see [Coverage and granularity](../networking.md#coverage-and-granularity).

The experimental QEMU backend has no restricted-egress implementation. It runs
with unrestricted networking and without gateway-side HTTP secret release.

## Secrets

Host environment variables are not passed unless declared by an enabled
extension or explicitly selected with `--pass-env`. Declared secrets configured
for HTTP release are represented by placeholders in the tool environment and
released by the gateway only for matching HTTPS hosts. This protects those
environment values from direct exfiltration, but credential files and secrets
without HTTP release can contain real values inside the tool config/auth store.

Once configured, the SSH key is available to every Docker session, regardless
of project or tool. SSH on port 22 bypasses the HTTP/TLS proxy, so a push does
not appear in `network.log`, even with `--network-log=requests`. See
[SSH keys](../auth.md#ssh-keys) for its access and how to scope or revoke it.

See the [restricted network request flow](../runtime/network-request-flow.md).
