# Rootless container engines

Rootful Docker is the default backend, rootless Podman is supported through
`--backend podman`, and rootless Docker is not supported.

## Why rootless Docker is not supported

The restricted-network gateway shares a network namespace with the tool and
needs iptables filtering and NAT redirects, ipset allowlists populated by
dnsmasq, network sysctls, and `NET_ADMIN` and `NET_RAW`. Rootless Docker cannot
provide this host-kernel netfilter behavior. Without it, a session would also
lose domain enforcement, request auditing, and gateway-side credential release,
so Enclave does not fall back to unrestricted networking.

Support would need an egress design in which the tool has no route around a
userspace proxy; cooperative `HTTP_PROXY` settings are not enough, because any
agent process can ignore them. The tool itself runs as a non-root user and the
host-directory stores work with unprivileged ownership, so traffic steering is
the only blocker.

With rootless Podman, the gateway keeps `NET_ADMIN` and `NET_RAW` inside its own
user and network namespaces, so steering and DNS enforcement work, and the tool
joins that namespace as under Docker. See [Podman](../backends.md#podman).

Alternatives:

- Docker user namespace remapping reduces host UID exposure and keeps the
  gateway; see [Host hardening](host-hardening.md).
- The experimental QEMU backend runs foreground tool-only sessions, with
  unrestricted network access and no gateway-side credential release.

## AppArmor on rootless Podman

Without an engine-applied AppArmor profile, container processes can inherit an
unconfined label, including a named unconfined-mode profile such as
`podman (unconfined)`. Executing a binary can then attach a host profile that
matches its path in the container, and `no-new-privileges` does not prevent
it. This can hit tool-container binaries such as `/usr/bin/man`; it was seen on
Ubuntu 24.04 with Podman 4.9.3.

The gateway therefore runs dnsmasq from `/usr/local/libexec/enclave/dnsmasq`
(also for validation), so a host profile for `/usr/{bin,sbin}/dnsmasq` does not
attach and host policy stays unchanged. The private binary keeps the
capabilities for DNS and ipset updates, but lacks the container-wide
confinement of Docker's `docker-default` profile, and custom host profiles can
still match its path.

Do not rely on adding the gateway's staging and log paths to the host dnsmasq
profile. On the tested Ubuntu host this allowed startup, but the profile denied
signals from the named unconfined parent, policy reloads hung, and the old
policy kept allowing names the update removed until the session stopped.

To troubleshoot, read the gateway startup error: it includes the container's
trailing output and dnsmasq validation errors. For suspected denials, check the
kernel journal (or the audit log with auditd) and `sudo aa-status`; AppArmor
being enabled does not by itself explain a failure.
