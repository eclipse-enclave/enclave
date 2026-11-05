# A2A prototype findings

## Pre-P1: podman isolate=true reachability (2026-11-05)

Host: Ubuntu 24.04, podman 4.9.3 rootless, netavark 1.4.0 (no `isolate=strict`).
Setup: two bridges with `isolate=true` (session-like), one container each; a
"hub" container connected to both; one bystander container on a non-isolated
bridge. Alpine 3.20, ICMP ping with 1 s timeout.

| Path | Result |
| --- | --- |
| member to member (both directions) | blocked |
| hub to each member over its own bridge | reachable |
| member to hub address on its own bridge | reachable |
| member to hub address on the other bridge | blocked |
| non-isolated bystander to member | reachable |
| non-isolated bystander to hub address | reachable |
| member to non-isolated bystander | reachable |
| member to member routed through the hub | blocked (hub had ip_forward=1; no route) |

Conclusions:

- The properties A2A relies on (members cannot reach each other, a
  multi-homed hub reaches each member only over that member's bridge) hold
  under `isolate=true`.
- What `strict` adds is blocking non-isolated podman networks from reaching
  isolated bridges, in both directions. Inbound to a member is already covered
  by the gateway's INPUT DROP policy, the documented fallback posture. Inbound
  to the hub is not covered by anything in draft 6 and must be closed by hub
  INPUT rules admitting only each member's gateway address on that bridge's
  interface. Outbound from a member is covered by the gateway's OUTPUT
  allowlist.
- Forwarding through the hub was blocked by routing alone; the hub still
  disables forwarding as specified.

Reproduction:

```sh
podman network create --opt isolate=true a2a-n1
podman network create --opt isolate=true a2a-n2
podman network create a2a-n3
podman run -d --name a2a-s1 --network a2a-n1 docker.io/library/alpine:3.20 sleep 300
podman run -d --name a2a-s2 --network a2a-n2 docker.io/library/alpine:3.20 sleep 300
podman run -d --name a2a-hub --network a2a-n1 docker.io/library/alpine:3.20 sleep 300
podman network connect a2a-n2 a2a-hub
podman run -d --name a2a-bystander --network a2a-n3 docker.io/library/alpine:3.20 sleep 300
# then: podman exec <src> ping -c1 -W1 <dst ip> for each row of the table
```

Change request CR-1 against proposal draft 6, accepted by the maintainer on 2026-11-05: replace "strict
isolation only" with "isolate option present (`true` or `strict`)", and add
hub INPUT rules restricted to member gateway addresses per interface.
