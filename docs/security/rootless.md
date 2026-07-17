# Rootless Docker compatibility

**Status: unsupported.** Enclave currently requires a rootful Docker daemon.

The restricted-network gateway shares a network namespace with the tool
container and relies on:

- iptables filtering and NAT redirects;
- ipset allowlists populated by dnsmasq;
- network sysctls;
- `NET_ADMIN` and `NET_RAW` capabilities.

Rootless Docker cannot provide the required host-kernel netfilter behavior. A
session that bypassed these controls would also bypass domain enforcement,
request auditing, and gateway-side HTTP secret release, so Enclave does not
automatically degrade to unrestricted networking.

The tool container itself runs as a non-root user and host-directory stores are
compatible with unprivileged ownership. The blocker is mandatory traffic
steering through the gateway, not tool execution or persistence.

Use rootful Docker for the supported backend. Docker user namespace remapping
can reduce host UID exposure while retaining the gateway; see
[Host hardening](host-hardening.md). The experimental QEMU backend is another
option for foreground tool-only sessions, but it runs with unrestricted network
access and does not provide gateway-side secret release.

Rootless support would require a different enforced egress design in which the
tool has no direct route around a userspace proxy. Cooperative `HTTP_PROXY`
configuration alone is insufficient because arbitrary agent processes can
ignore it.
