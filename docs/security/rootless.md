# Rootless container engines

**Status:** rootless Docker is unsupported; rootless podman is supported through
`--backend podman`; rootful Docker is the default backend.

The restricted-network gateway shares a network namespace with the tool
container and relies on:

- iptables filtering and NAT redirects;
- ipset allowlists populated by dnsmasq;
- network sysctls;
- `NET_ADMIN` and `NET_RAW` capabilities.

Rootless Docker cannot provide the required host-kernel netfilter behavior. A
session that bypassed these controls would also bypass domain enforcement,
request auditing, and gateway-side HTTP secret release, so Enclave does not
automatically degrade to unrestricted networking. Supporting rootless Docker
would require a different enforced egress design in which the tool has no direct
route around a userspace proxy; cooperative `HTTP_PROXY` configuration alone is
insufficient because arbitrary agent processes can ignore it.

Rootless podman is different: with `--backend podman` the gateway keeps
`NET_ADMIN`/`NET_RAW` inside its own user and network namespaces, so netfilter
steering and DNS enforcement work there, and the tool container joins that
namespace exactly as under Docker (see
[Podman backend](../cli-reference.md#podman-backend)).

The tool container itself runs as a non-root user and host-directory stores are
compatible with unprivileged ownership. The blocker for rootless Docker is
mandatory traffic steering through the gateway, not tool execution or
persistence.

Rootful Docker remains the default backend. Docker user namespace remapping can
reduce host UID exposure while retaining the gateway; see
[Host hardening](host-hardening.md). The experimental QEMU backend is another
option for foreground tool-only sessions, but it runs with unrestricted network
access and does not provide gateway-side secret release.
