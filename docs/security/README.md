# Security boundaries

Enclave reduces an agent's access to the host and network; it is not a hardened
container escape boundary. The supported Docker backend uses a rootful daemon.
Use [host hardening](host-hardening.md) where compatible with the required
workflow. Rootless Docker is [not currently supported](rootless.md).

## Host filesystem

- The project is a host bind mount and is writable by default. Agent changes are
  real host changes.
- `--project-mount readonly` makes the project/worktree read-only and clamps
  writable additional mounts inside that subtree to read-only.
- `--worktree-metadata readonly|none` protects or omits linked-worktree
  gitdir/commondir mounts independently of the working tree. With read-only Git
  metadata, in-container Git writes such as `git add` fail.
- Additional host directories are explicit CLI/config inputs. Mounting sensitive
  paths expands the sandbox's host access.
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

The allowlist is destination policy, not content policy. It does not constrain
URL paths, methods, request bodies, responses, or an upstream service's own
proxy and relay features. An allowlisted API, package registry, or other service
can therefore return untrusted content or provide indirect access beyond what
its hostname suggests. Treat every allowlisted service as part of the trust
boundary.

The experimental QEMU backend has no restricted-egress implementation. It runs
with unrestricted networking and without gateway-side HTTP secret release.

## Secrets

Host environment variables are not passed unless declared by an enabled
extension or explicitly selected with `--pass-env`. Declared secrets configured
for HTTP release are represented by placeholders in the tool environment and
released by the gateway only for matching HTTPS hosts. This protects those
environment values from direct exfiltration, but credential files and secrets
without HTTP release can contain real values inside the tool config/auth store.

See [Authentication and secrets](../auth.md) and the
[restricted network request flow](../runtime/network-request-flow.md).
