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

## AppArmor on rootless Podman

On rootless Podman setups without an engine-applied AppArmor profile, container
processes can inherit an unconfined label, including a named unconfined-mode
profile such as `podman (unconfined)`. Executing a binary can then attach a host
profile matching its path inside the container. `no-new-privileges` does not
prevent that transition. This can affect tool-container binaries such as
`/usr/bin/man`; it was observed on Ubuntu 24.04 with Podman 4.9.3.

The gateway installs dnsmasq at `/usr/local/libexec/enclave/dnsmasq` and uses
that absolute path for validation and startup. This avoids the host-service
attachment `/usr/{bin,sbin}/dnsmasq` without changing host policy. The private
binary retains the capabilities needed for DNS and ipset updates. It does not
provide the container-wide confinement of Docker's usual `docker-default`
profile, and custom host profiles can still match the private path.

Do not rely on adding the gateway's staging and log paths to the host dnsmasq
profile: other restrictions still apply. On the tested Ubuntu host, file-path
overrides allowed startup but the profile denied signals from the named
unconfined parent, causing policy reloads to hang. The old policy kept allowing
traffic to names removed by the attempted update until the session was stopped.

Gateway startup failures include the trailing container output in the CLI
error, including dnsmasq validation errors. For suspected AppArmor denials,
check the kernel journal or the audit log if auditd is running, and use
`sudo aa-status` to check profile modes. AppArmor merely being enabled does not
establish the cause of a startup failure.

Rootful Docker remains the default backend. Docker user namespace remapping can
reduce host UID exposure while retaining the gateway; see
[Host hardening](host-hardening.md). The experimental QEMU backend is another
option for foreground tool-only sessions, but it runs with unrestricted network
access and does not provide gateway-side secret release.
