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
  isolated bridges, in both directions. Inbound to a member is covered by the
  gateway's INPUT DROP policy except on published ports, which the existing
  rules accept on any interface; a bystander can therefore reach a session's
  published listeners under `isolate=true`. That is pre-existing fallback
  behavior, unchanged by A2A, and the reason the fallback is weaker, not
  equivalent. Inbound to the hub is closed by hub INPUT rules admitting only
  each member's gateway address on that bridge's interface. Outbound from a
  member is covered by the gateway's OUTPUT allowlist.
- Not yet measured here: bystander to a member's published port. P1-P
  records it as the expected difference.
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

Change request CR-1 against proposal draft 6, accepted on 2026-11-05 with the reviewer's correction that the fallback is weaker, not equivalent: replace "strict
isolation only" with "isolate option present (`true` or `strict`)", and add
hub INPUT rules restricted to member gateway addresses per interface.

## P1-D: Docker topology (2026-11-05)

Host: Ubuntu 24.04, kernel 6.17 (`CONFIG_INET_DIAG_DESTROY=y`), Docker
29.9.2 rootful.

Setup: three session bridges (`orch`, `w1`, `w2`) created the way enclave
creates them on Docker 29 (`--subnet 0.0.0.0/28 --ipv6=false -o
com.docker.network.bridge.host_binding_ipv4=127.0.0.1`). Each bridge runs
the real gateway image built from `Dockerfile.gateway` at `aee2f7e`
(A5's overlay included) with a hand-written bundle and a session-local
overlay, plus a tool container in the gateway's namespace (uid 1000). `w1`
publishes port 8000 on `127.0.0.1`. The hub is a stand-in: alpine with a
shell supervisor (root, `iptables-restore` from the snapshot, conntrack
flush of removed endpoints) and a Go stdlib proxy (uid 65532, snapshot
verification, status files, `/peers/<name>` forwarding with SSE
passthrough, control and liveness SSE with 1 s keepalives). Adapters are Go
stdlib servers with a control connection to `a2a.enclave:41820` (200 ms
retry, loss after 2 missed keepalives). The hub runs with `--cap-drop ALL
--cap-add NET_ADMIN,SETUID,SETGID,KILL,DAC_READ_SEARCH --read-only
--security-opt no-new-privileges --sysctl net.ipv4.ip_forward=0`, the
snapshot root mounted read-only and the 1777 status directory read-write.
Also present: a bystander on a default bridge and a rule-less probe on
`w1`'s bridge. Hub addresses are the last usable address of each `/28`.
Timings include the Docker CLI where marked "host".

### Design findings (read first)

1. **Conntrack flush cuts traffic but closes nothing.** After the
   supervisor drops the accept rule and flushes a removed endpoint's
   entries, the next packet is picked up as NEW and dropped. Both sockets
   stay open and stall: the orchestrator's stream through the hub, the
   hub's upstream socket and the worker adapter only learn of it through
   their own timeouts. Answering with `REJECT --reject-with tcp-reset` on
   INPUT resets only the worker side, and a REJECT on OUTPUT also swallows
   that RST. When the supervisor applies before the proxy reloads (the
   independent-reader design), the proxy's FIN for a revoked principal
   goes out after the flush and is dropped, so the orchestrator never sees
   the close. Fix measured: after installing the deny rules and before
   the conntrack flush, the supervisor aborts hub sockets to the removed
   endpoint with `ss -K dst <gw>` (SOCK_DESTROY, needs `NET_ADMIN`). The
   RST still matches the existing conntrack entry, and both ends close
   about 0.4 s after the swap (host), for worker removal and for
   orchestrator revocation alike. The supervisor needs no input from the
   proxy for this. Proposed as CR-P1D-1 below. The security property
   (traffic stops) holds without it.
2. **A compromised hub namespace reaches more than adapter ports.** With
   the supervisor's rules flushed, the hub reached `w1`'s published port
   8000 (the gateway accepts published ports on any interface and from any
   source), the host's sshd on the bridge gateway address, and the
   internet through Docker's NAT. Gateway DNS (TCP 53), proxy ports
   (8080/8443) and the orchestrator stayed closed. Proposal 6 bounds a
   compromised supervisor by "the gateways' own INPUT rules"; those rules
   admit published ports, and nothing bounds hub egress. The supervisor
   also reads every outbound token. Proposed as CR-P1D-2.
3. **Hub liveness cannot see silent loss.** The liveness stream is
   hub-to-client only. Process exit, `docker rm -f` of the tool container
   and removal of the whole session were all detected within 27 to 44 ms
   through FIN. A paused orchestrator was not detected in 8 s, and a
   silent path loss (both directions dropped in the gateway) was not
   detected in 30 s. Default TCP retransmission gives up only after about
   15 minutes. With `TCP_USER_TIMEOUT` = 5 s on the hub listener (inherited
   by accepted sockets), the same loss was detected at 6.0 s. The hub needs
   `TCP_USER_TIMEOUT` below the reconnect window on liveness and control
   connections, or a client-to-hub heartbeat.
4. **The hub cannot be created on `--network none`.** Docker refuses
   `network connect` for such a container ("cannot be connected to multiple
   networks with one of the networks in private (none) mode"). Create it on
   the first member's bridge with `--ip`. Afterwards it can be disconnected
   from every network, restarted with none, and reconnected.
5. **A stopped hub does not block network removal and then fails to
   start.** With the hub stopped, `docker network rm` of a member bridge
   succeeds, and `docker start` of the hub then fails with "failed to set
   up container networking: network <id> not found". `docker network
   disconnect <name> <hub>` on the deleted network succeeds while the hub
   is stopped, and the hub then starts. Reconcile has to disconnect
   stale network references before starting the hub. While the hub runs,
   `network rm` fails with "has active endpoints (name:\"a2a-p1d-hub\")",
   as teardown expects.
6. **Interface names are not stable across hub restart.** The address
   with `.30` was `eth0` before a restart and `eth2` after it; addresses
   were unchanged. The supervisor must map address to interface on every
   apply, as the stand-in does, and never cache names.
7. **The supervisor needs `CAP_DAC_READ_SEARCH`.** `secrets.json` is
   0400 and owned by 65532. Without the capability, root fails to hash it
   ("permission denied") and refuses the revision. With it, root reads
   the file and a third uid (65534) still cannot.
8. **Default deny before the first snapshot.** The first stand-in
   supervisor installed nothing until it loaded a valid snapshot, leaving
   ACCEPT policies in the meantime. The supervisor must load fixed DROP
   policies (loopback excepted) before it reads `current`.

### W0-B Q4: static `--ip` and session network creation

- Enclave today (`internal/backend/docker/network.go`): Docker 29.0 and
  later get `--subnet 0.0.0.0/28`, a dynamic small prefix taken from the
  default pools (inspect shows the concrete subnet, for example
  `172.18.0.16/28`). If the daemon rejects it, enclave retries with
  default IPAM. Docker before 29.0 and Podman always use default IPAM
  (Docker `/16`, Podman `/24`).
- **Docker 29.9.2 does not refuse `--ip` on a default-IPAM network.**
  `docker run --ip 172.19.0.77` and `docker network connect --ip` on a
  network created without `--subnet` both succeeded, and the address was
  assigned (`172.19.0.77/16`). The refusal Q4 assumed ("user specified IP
  address is supported only when connecting to networks with user
  configured subnets") was not reproduced here. Older daemons were not
  tested.
- `--ip <last usable>` works with `run` and `connect` on the dynamic `/28`,
  and survives `docker restart` and disconnect/connect.
- Recommendation for A7: compute the last usable address from the
  inspected IPv4 subnet, whatever its prefix. Reject `--peer-group` only
  when inspect reports no IPv4 subnet. Treat a failed `connect --ip` as a
  failed join with rollback, not as a precondition.

### Evidence list (proposal 23.P1, Docker)

| Item | Verdict | Evidence |
| --- | --- | --- |
| Hub reaches each gateway's adapter port and nothing else | verified | hub to `w1`/`w2` :41821 open; to `w1` :8000, :53, :8080, orchestrator :41821 blocked |
| Members cannot reach each other directly or via the hub | verified (network); routing policy: I2 | tool to other member's gateway and to the hub address on another bridge blocked; rule-less probe to another bridge blocked by Docker isolation, also with a route via the hub (forwarding off, FORWARD DROP); worker-initiated `/peers` refusal is proxy logic |
| Existing published ports unreachable to peers | verified, see finding 2 | other members, bystander and intact hub blocked from `w1`:8000; host `127.0.0.1:<port>` open; hub with flushed rules reaches it |
| Hub egress to internet and host blocked | verified | hub to 1.1.1.1:443, 8.8.8.8:53, host sshd on the bridge gateway and `docker0` blocked; rule-less probe on the same bridge reaches the host sshd and the internet |
| IPv6 and link-local dropped | verified | no IPv6 address (including link-local) on any hub or gateway interface: Docker sets `disable_ipv6=1` per interface on IPv4-only networks; ip6tables policies DROP in hub and gateway; `ping -6 ff02::1%eth1`: network unreachable |
| DNS record and NAT exemption work and survive a policy reload | verified (A5 gateway) | `a2a.enclave` resolves to the hub; nat OUTPUT RETURN for the hub precedes the REDIRECTs; request through the hub succeeds before and after 9 SIGHUP reloads; a hand-added rule is gone after one reload |
| Session-local overlay survives reload | verified (A5 gateway) | markers `applied (generation=2)`, `cleared (generation=3)` with DNS record and rules removed, `applied (generation=4)` with them back |
| Snapshot swap visible to supervisor and proxy, same revision | verified (engine); real readers: I2 | symlink swap in the read-only directory mount is visible in the container; both stand-in status files show the same revision, 10 of 10 commits |
| Mixed or corrupt snapshot refused, last revision retained, status reported | verified (stand-in readers); real readers: I2 | hash mismatch and secrets from N-1 both refused by both readers: `appliedRevision` N-1, `rejectedRevision` N, error text; requests keep routing on N-1 |
| Removal under active traffic kills existing connections (conntrack) | verified with amendment, see finding 1 | without flush the removed worker's stream kept flowing for more than 6 s; with flush it stops at once but stalls; with `ss -K` then flush both ends close at about 0.4 s (host) |
| Invalid update followed by engine disconnect cuts connectivity | verified | corrupt removal refused and the stream continues; `network disconnect` (78 ms) stops it at once (stall, no close); a new request gets a delivery error after the 3 s dial timeout |
| Hub restart keeps static addresses and reloads current | verified | 6 restarts (5 `restart`, 1 `kill`+`start`): addresses unchanged, interface names reordered (finding 6), rules reinstalled, both readers report the current revision and a new epoch |
| Token revocation by revision closes existing streams | not verifiable without Wave 2 code | stand-in proxy closes a revoked principal's stream; the close reaches the client only with proxy-first ordering or `ss -K` (finding 1). I2: the real proxy closes revoked principals' streams and upstream streams to removed workers, and the client sees EOF |
| Join, leave, failed join with rollback, concurrent stop, crash, stale-attachment recovery | not verifiable without Wave 2 code | engine primitives verified: `connect --ip` 33 ms, `disconnect` 78 ms, `network rm` refused while the hub is attached, stopped hub behavior (finding 5), uncommitted revision times out without a nudge. I2: A7 lifecycle against these behaviors |
| Unattended background orchestrator exit yields suspected departure in the window | not verifiable without Wave 2 code | FIN-based detection 27 to 44 ms; silent loss needs `TCP_USER_TIMEOUT` (finding 3). I2: suspected departure within the window for exit and for silent loss |
| Supervisor and proxy capability and mount restrictions, secrets readability | verified (stand-in hub container) | supervisor `CapEff 0x10e4` (DAC_READ_SEARCH, KILL, SETGID, SETUID, NET_ADMIN), `NoNewPrivs 1`; proxy uid 65532, `CapEff 0`, `NoNewPrivs 1`; no engine socket; proxy cannot write the snapshot root, replace or remove `supervisor.json`, write rootfs, run iptables, signal pid 1, read `/proc/1/environ`; secrets readable by proxy and root, not by 65534 (finding 7) |
| Hub INPUT blocks a non-isolated bystander under `isolate=true`; matrix under both isolate values | not applicable on Docker (P1-P) | Docker isolates user-defined bridges: the bystander cannot reach member gateways, hub addresses or the published port, even via its own bridge gateway. A rule-less container on a member bridge is blocked from hub:41820 by hub INPUT |

### Gateway overlay questions (A5, `aee2f7e`)

- Long-lived member-to-hub streams across gateway SIGHUP reloads:
  **survive.** 10 reloads of each of the `orch` and `w1` gateways within
  7 s: no stream closed, the adapter control connection was never lost,
  and the largest gap between 200 ms ticks was 402 ms.
- Established conntrack entries survive an overlay clear: **yes.** After
  `active:false` and reload, the worker's control connection and the
  orchestrator's stream kept running for 3 s. A hub revision with
  conntrack flush, followed by engine disconnect, cuts the traffic, but
  neither socket closes: the adapter noticed only through its keepalive
  timeout, and the orchestrator's stream stalled. With `ss -K` before the
  flush, both ends closed within about 0.4 s. Flush plus disconnect is
  enough for the security property, not for prompt notification.
- Overlay readability: **yes** on rootful Docker. The file was 0644 in a
  0755 directory, both owned by uid 1000, and the gateway logged `A2A
  overlay applied (generation=1)` at start. Rootless Podman maps gateway
  root to the host user under keep-id, so P1-P has to confirm it there.

### Measurements

| Quantity | Value |
| --- | --- |
| Snapshot commit, swap to both acks (host, includes `docker kill -s HUP`) | 204 to 263 ms over 10 unchanged revisions; 190 to 413 ms for revisions that add or remove a member |
| Supervisor apply (`iptables-restore`, conntrack, status) | 33 to 60 ms |
| Gateway SIGHUP reload, "Reload requested" to "Reload completed" (A5 image, in container) | 414 to 440 ms over 9 reloads; 496 to 538 ms from the host including the CLI |
| Hub restart: `docker restart` command | 359 to 425 ms |
| Hub restart: new proxy status file written | 456 to 507 ms after the command |
| Hub restart: both adapters' control connections back | 1282 to 1323 ms after the command; adapter-side outage 1207 to 1254 ms |
| Orchestrator liveness stream at hub stop | closed (FIN) 21 to 70 ms after the command |
| Removed endpoint, no conntrack flush | traffic continues (more than 6 s observed) |
| Removed endpoint, flush, DROP | last data 0.3 to 0.44 s after the swap, then a stall that never closed in the observed 6 s |
| Removed endpoint, `ss -K` then flush | closed at 0.40 to 0.43 s after the swap, both ends |
| Orchestrator revoked, supervisor first | stall, no close |
| Orchestrator revoked, proxy first | closed at 0.42 s |

Recommended bounds from these numbers: snapshot commit ack timeout 5 s
(about 12 times the worst observed commit; revisit with P1-P's rootless
numbers). The 60 s reconnect window placeholder comfortably covers a hub
restart (about 1.3 s). The liveness and control `TCP_USER_TIMEOUT` should
sit well below the window, for example 10 s.

### Change requests

- **CR-P1D-1** (proposal 6 and 15, interfaces 2): on a revision that
  removes an endpoint, the supervisor installs the deny rules, aborts
  hub sockets to that endpoint (`SOCK_DESTROY`, as `ss -K dst <addr>`),
  then flushes its conntrack entries. Its ack means all three. Reason:
  finding 1. Rootless Podman support for `SOCK_DESTROY` inside the hub
  namespace is for P1-P to check.
- **CR-P1D-2** (proposal 6 and 18, interfaces 4): the gateway overlay adds
  an INPUT drop for the hub address before the published-port accepts,
  so the hub reaches only the adapter port. The blast radius text also
  states hub egress and token exposure for a compromised supervisor.
  Reason: finding 2. An alternative is the published-port scoping already
  listed under Later.
- **CR-P1D-3** (proposal 13 and 17, interfaces 11): the hub sets
  `TCP_USER_TIMEOUT` on the liveness and control connections, as a new
  bound below the reconnect window. Reason: finding 3.

### Reproduction

Throwaway scripts lived in `/tmp/a2a-p1d` and are not kept. The
essential commands:

```sh
docker network create --driver bridge --ipv6=false --subnet 0.0.0.0/28 \
  -o com.docker.network.bridge.host_binding_ipv4=127.0.0.1 a2a-p1d-net-w1
docker run -d --name a2a-p1d-gw-w1 --network a2a-p1d-net-w1 --cap-add NET_ADMIN --cap-add NET_RAW \
  -e ENCLAVE_GATEWAY_CONFIG_DIR=/etc/enclave-gateway-config -e ENCLAVE_GATEWAY_PUBLISHED_PORTS=8000/tcp \
  -p 127.0.0.1::8000 -v <bundle>:/etc/enclave-gateway-config:ro -v <overlay dir>:/etc/enclave-a2a-overlay:ro \
  -v <ca dir>:/tls:ro -e ENCLAVE_GATEWAY_TLS_ROOT=/tls <gateway image>
docker run -d --name a2a-p1d-tool-w1 --network container:a2a-p1d-gw-w1 --user 1000:1000 ... <adapter>
docker run -d --name a2a-p1d-hub --network a2a-p1d-net-orch --ip <orch last usable> \
  --cap-drop ALL --cap-add NET_ADMIN --cap-add SETUID --cap-add SETGID --cap-add KILL --cap-add DAC_READ_SEARCH \
  --security-opt no-new-privileges --read-only --tmpfs /run --tmpfs /tmp --sysctl net.ipv4.ip_forward=0 \
  -v <snapshots>:/run/enclave-a2a/snapshots:ro -v <status>:/run/enclave-a2a/status <hub image>
docker network connect --ip <w1 last usable> a2a-p1d-net-w1 a2a-p1d-hub
# reachability: docker exec <src> nc -z -w 2 <dst> <port>
# commit: publish N, docker kill -s HUP a2a-p1d-hub, poll both status files
# removal: in the hub, ss -K dst <gw>; conntrack -D -s <gw>; conntrack -D -d <gw>
```

## P1-P: rootless Podman topology (2026-11-05)

Host: Ubuntu 24.04, kernel 6.17, podman 4.9.3 rootless, netavark 1.4.0, runc,
slirp4netns, `/etc/subuid` range of 65536. Session bridges are created the
way enclave creates them on Podman (`isolate=true`, IPv6 off, Podman's /24
IPAM), so the static hub address is `.254`.

Topology: three session bridges (orchestrator, worker w1, worker w2), each
with a gateway (`--userns keep-id --user 0:0`, NET_ADMIN and NET_RAW, a
published port 3000) and a tool container joined to the gateway's network
and user namespace. A fourth worker (w3) runs the real A5 gateway built from
`aee2f7e` (`Dockerfile.gateway`, transparent proxy enabled, host bundle and
overlay mounted `:ro,z`). The other three gateways run a shell stand-in with
the same INPUT, OUTPUT and nat shape. Hub stand-in: one container, default
rootless mapping, `--cap-drop all --cap-add
NET_ADMIN,NET_RAW,SETUID,SETGID,DAC_READ_SEARCH,KILL --read-only
--security-opt no-new-privileges --sysctl net.ipv4.ip_forward=0`, snapshot
root mounted read-only, status directory (1777) read-write. The root
supervisor (PID 1) installs INPUT/OUTPUT/FORWARD DROP for IPv4 and IPv6,
ESTABLISHED accept, per-member `-i <if> -s <gateway> --dport 41820` INPUT
and `-o <if> -d <gateway> --dport 41821` OUTPUT, flushes conntrack for
removed endpoints, and starts the proxy with `su-exec 65532:65532`. Proxy:
bearer check against snapshot hashes, name-based routing, streaming
passthrough. Adapter: HTTP on IPv4 41821 with ticking streams. A bystander
sits on a non-isolated bridge. Scripts and logs are in `/tmp/a2a-p1p/` on
the prototype host and are not committed; every container and network was
named `a2a-p1p-*` and removed afterwards.

### Results

No item failed. Three findings need code changes (marked **must**).

| P1 item | Verdict | Evidence |
| --- | --- | --- |
| Hub reaches each adapter port and nothing else | verified | proxy uid to w1/w2/w3 `:41821` open; to orchestrator and worker `:3000` blocked, also as hub root; to bystander, `1.1.1.1:443`, the bridge gateway and `host.containers.internal` blocked |
| Members cannot reach each other, directly or via the hub | verified | tool to other gateway `:41821` and `:3000` blocked in all directions; tool to hub address on another bridge blocked; orchestrator gateway with a route via the hub and its own OUTPUT opened cannot reach w1 (hub `ip_forward=0`, FORWARD DROP) |
| Published ports unreachable to peers | verified | member to member `:3000` blocked; hub proxy to `:3000` blocked by hub OUTPUT |
| Hub egress to internet and host blocked | verified | rows above |
| IPv6 and link-local dropped | verified | `disable_ipv6=0` and `fe80::` on every interface despite `--ipv6=false`; hub to w1 gateway link-local blocked with both DROP policies, still blocked with only the gateway's, open with both opened (control); adapter refuses `[fe80::]:41821` (IPv4-only listener) |
| DNS record and NAT exemption, survive reload | verified (real A5 gateway) | `host-record=a2a.enclave,10.89.4.254` in the gateway's dnsmasq config; tool resolves it; nat OUTPUT has `-d <hub> -j RETURN` before the proxy-uid RETURNs and the 80/443 REDIRECTs; unchanged after reloads |
| Session-local overlay survives reload | verified (real A5 gateway) | generations 1 to 8 each logged `applied` or `cleared` exactly once; rules present after each reload |
| Snapshot swap visible to both readers, same revision acked | verified (stand-in readers) | directory mount with a relative `current` symlink swapped by rename; both status files show the new revision 15 to 106 ms after publish plus SIGHUP |
| Mixed or corrupt snapshot refused, last revision kept, status reported | verified (stand-in readers) | `secrets.json` with revision N-1, and a manifest hash mismatch: both readers write `rejectedRevision=N` with the reason and keep serving N-1 |
| Removal under traffic kills connections (conntrack) | verified | without a flush the stream keeps flowing after the rule is gone (5, 7, 14 ticks); with `conntrack -D -s/-d <gateway>` it stops within 1 s; works in the rootless netns under both userns modes |
| Invalid update then engine disconnect cuts connectivity | verified | refused update keeps the stream (5 to 9 ticks); `podman network disconnect` (83 to 90 ms) stalls it within 1 s; hub to w1 and w1 to hub blocked; new request gets a 502 delivery error; reconnect with `--ip` and a new revision restores it |
| Hub restart keeps static addresses and reloads current | verified, **must** fix the gap below | addresses identical after `restart` and `stop`/`start`; new epoch in both status files 612 to 704 ms after the restart |
| Token revocation by revision closes streams | stand-in only; I2 with A3 | stand-in proxy shuts both sockets; client sees EOF at once |
| Join, leave, rollback, concurrent stop, crash, stale attachment | engine primitives verified; logic deferred to I2 | see engine behavior below |
| Unattended orchestrator exit gives suspected departure | not verifiable without Wave 2 | I2 with A11 and A3 |
| Supervisor and proxy restrictions, secrets readability | verified (stand-in process layout) | proxy: uid/gid 65532, CapEff/CapPrm 0, NoNewPrivs 1, `iptables` denied, snapshot mount read-only, cannot replace `supervisor.json`; root: read-only rootfs and snapshot, no CAP_SYS_ADMIN; no engine socket mounted |
| Hub INPUT blocks a bystander under `isolate=true` | verified | bystander to hub `:41820` on all three addresses blocked; with the INPUT rule replaced by accept-all it is open (control) |
| Full matrix under both isolate values | `true` verified; `strict` not available | podman 4.9.3 creates an `isolate=strict` network, but netavark 1.4.0 fails container start: `unable to parse "isolate": provided string was not true or false`. Creating the network is no proof of support; keep the netavark >= 1.7 gate |
| Bystander reaches a member's published port (CR-1 known difference) | verified, expected | bystander to w1 and w3 gateway `:3000` open; adapter `:41821` and ping blocked |

Coordinator questions on the A5 overlay (real gateway, `aee2f7e`):

1. `ip -4 route get <hub>` in the gateway (busybox `ip`) prints
   `10.89.4.254 dev eth0  src 10.89.4.4`, before and after the hub is
   attached; `enclave_route_interface_ipv4` returns `eth0`.
2. Overlay directory mounted `:ro,z` (`ro,relatime` in mountinfo; `z` is a
   no-op on this AppArmor host, SELinux enforcing is untested and left to
   I2). The host's rename is seen on the next SIGHUP. Inside, the file shows
   as uid 1000 (keep-id); gateway root reads it at mode 0664 and at 0600
   (root has CAP_DAC_OVERRIDE over mapped uids).
3. Bystander to w3 adapter `:41821` blocked; published `:3000` open.
4. A w3-to-hub stream and a hub-to-w3 stream both kept ticking across three
   SIGHUP reloads (5, 9, 17 ticks). After the overlay is cleared, existing
   streams keep flowing (ESTABLISHED accept) and new connections in both
   directions are blocked. Cutting existing flows at leave is therefore the
   hub's job (conntrack flush) or the engine disconnect's, not the gateway's.

### Findings that change code

- **must** Hub restart changes every interface MAC. Member gateways keep the
  stale neighbor entry and cannot reach the hub for 8 to 47 s per member
  (eleven measurements: 8.1, 8.1, 8.2, 9.4, 26.9, 31.1, 32.5, 32.7, 36.8,
  36.9, 47.2 s) and 54.4 s until all three members reached it; the same
  happens after a disconnect and reconnect (11.5 and 12.8 s). Fixes, both measured: a fixed MAC per attachment
  (`podman network connect --ip <addr> --mac-address <mac>`, persists
  across restart, reachable 129 to 159 ms after restart), or a gratuitous
  ARP from the supervisor on start and after each attach (`arping -U -c 1 -I
  <if> <addr>`, 123 to 159 ms; needs NET_RAW). A7 should set the MAC
  (derived from group and member, locally administered bit set); A4 should
  also send the gratuitous ARP. Docker needs the same check in P1-D.
- **must** Hub capability set when dropping all: NET_ADMIN, NET_RAW, SETUID,
  SETGID, plus DAC_READ_SEARCH (root cannot hash the 0400 `secrets.json`
  without it) and KILL (root cannot signal the proxy uid without it). The
  bounding set then is `0x30e4`; nothing else was needed.
- **must** A failed `podman network connect --ip` (address already
  allocated) leaves Podman inconsistent: `inspect` shows no networks while
  the container still has its interfaces, and the failed network stays
  recorded, so a later restart joins it once the address is free. A
  `network disconnect` of the failed network returns rc 125 but restores
  the state. A7: check the address against the network's containers before
  connecting, always disconnect on rollback, ignore that error, then verify
  with `inspect`.

Other engine behavior for A7 and A4:

- The hub cannot start on `--network none`: `podman network connect` then
  fails with `"" is not supported: invalid network mode`. Create the hub on
  the first member's bridge (`--network <net>:ip=<addr>`), connect the rest.
- Rootless containers start with `ip_forward=1`; `--sysctl
  net.ipv4.ip_forward=0` works rootless.
- `podman network connect` to a stopped hub succeeds and applies on start.
- `podman network rm` fails while the hub is attached ("has associated
  containers"), even when the hub is the only one: teardown must disconnect
  first and must not use `network rm -f`, which removes attached containers.
- Removing the hub's creation network (after disconnect) and restarting
  works; so does a hub with zero networks.
- Interface names follow attach order and change on reconnect; the
  supervisor must map interfaces from `hubAddress` on every apply.
- Status files survive a hub restart with the old epoch. A new revision's
  commit is unaffected, but anything that waits for a restarted hub must
  wait for a new epoch, not just `appliedRevision`.
- The supervisor signaled the proxy before Python installed its SIGHUP
  handler and killed it. A3 must install the handler first; A4 must not
  signal before the proxy's first status write (the stand-in waits for the
  proxy status file to carry the current epoch).
- A conntrack flush alone stalls a stream (dropped packets, no RST) and the
  client only notices at its timeout. The proxy closing streams of revoked
  principals is what gives the client a prompt EOF; the flush is the
  enforcement backstop.
- Source addresses seen by a gateway listener: bystander traffic arrives
  from the bridge gateway (`10.89.1.1`, masqueraded); host traffic through a
  published port arrives from the gateway's own address. Useful for the
  later hardening that scopes published-port INPUT rules.
- Inference, not measured: a compromised hub root that opens its own OUTPUT
  reaches members' published ports, because the gateway INPUT rule accepts
  them from any source (the bystander row shows the rule). This matches
  "bounded by the gateways' own INPUT rules" in the proposal.

### Secrets file mapping (for A2 and A4)

`/etc/subuid` `tobias:100000:65536`, host uid 1000. Measured maps:

| Mode | `uid_map` | container 65532 on the host |
| --- | --- | --- |
| default rootless (hub) | `0 1000 1; 1 100000 65536` | 165531 |
| keep-id (gateway, session) | `0 1 1000; 1000 0 1; 1001 1001 64536` | 165531 |

Both modes map container uid 65532 to intermediate uid 65532, so one host
command serves either:

```sh
chmod 0400 .staging-N/secrets.json      # first: after the chown the host user no longer owns it
podman unshare chown 65532:65532 .staging-N/secrets.json   # 34 to 45 ms
```

Result on the host: `-r-------- 165531:165531`. Inside the hub (either
mode): `-r-------- 65532:65532`; uid 65532 and root (with DAC_READ_SEARCH)
read it; uids 1000, 101 (gateway) and 65534 get EACCES; root without
DAC_READ_SEARCH or DAC_OVERRIDE gets EACCES. The host user cannot read it
(hence `host-secrets/outbound.json`) but can delete revision directories,
because it owns them. Requires a subordinate range of at least 65532
entries; with a smaller range the chown fails and A2 should report that.
The hub does not need keep-id (nothing joins its namespaces); the default
mapping makes root's `supervisor.json` host-owned (uid 1000), while
`proxy.json` appears as 165531 with mode 0644, readable by the host. In the
1777 status directory the proxy cannot replace `supervisor.json` (EPERM),
and the host, as directory owner, removes either file.

### Verdict

`isolate=true` on rootless Podman is supportable for v1 as the weaker
fallback CR-1 describes. The A2A message plane has the reachability the
design needs: members are isolated from each other in both directions and
through the hub, the hub reaches only adapter ports, a bystander on a
non-isolated network reaches neither the hub (closed only by the hub INPUT
rules, shown by the control) nor any adapter, IPv6 and link-local are
dropped by policy, and revocation works both by conntrack flush and by
engine disconnect. The remaining difference is the one CR-1 records: a
bystander reaches a session's published ports, which A2A does not change.
Support depends on the three "must" items above, the hub restart neighbor
gap above all, since without a fixed MAC or a gratuitous ARP a hub restart
silently cuts every member for up to a minute. `strict` could not be
measured on this host because netavark 1.4.0 rejects it at container start.

Deferred to I2: real hub readers (A3, A4) for snapshot refusal, status and
stream revocation; lifecycle logic (join rollback, leave, concurrent stop,
crash, stale attachment, reconcile); suspected departure; the full matrix
under `isolate=strict` on netavark >= 1.7; the overlay mount under SELinux
enforcing.
