# A5b report: gateway hub-source rule (CR-P1D-2)

Branch `a2a-a5b`, based on `6fdb667`.

## Summary

- With an active overlay, the member gateway's INPUT chain now drops every
  packet sourced from the hub address except TCP to the adapter port.
  Replies to connections the member opened still pass through the existing
  ESTABLISHED,RELATED accept.
- `setup_input_firewall` calls the new `setup_a2a_input_rules` for
  `iptables` only, between the ESTABLISHED,RELATED accept and the
  published-port accepts. Before this change, the adapter accept was
  appended after the published ports, and the published ports accepted the
  hub like any other source.
- `adapterPort: 0` (orchestrator): no route lookup and no accept, only the
  drop.
- The drop has no `-i`, so a hub-sourced packet is dropped on any interface.
  This also covers loopback-forwarded ports, because nat PREROUTING DNAT
  runs before filter INPUT and the source is unchanged.
- ip6tables gets nothing, as before.
- No overlay, or `active: false`: no new rules. `interfaces.md` section 4
  now describes the rule in its `active: true` bullet.

## Rule set as rendered

Live, worker overlay, hub `172.29.78.14`, published port `3000/tcp`:

```text
-P INPUT DROP
-A INPUT -i lo -j ACCEPT
-A INPUT -m state --state RELATED,ESTABLISHED -j ACCEPT
-A INPUT -s 172.29.78.14/32 -i eth0 -p tcp -m tcp --dport 41821 -j ACCEPT
-A INPUT -s 172.29.78.14/32 -j DROP
-A INPUT -p tcp -m tcp --dport 3000 -j ACCEPT
```

The orchestrator overlay renders the same chain without the `--dport 41821`
line.

## Tests (`internal/gateway/a2a_overlay_test.go`)

These tests use the packet-level replay of the recorded iptables calls, with
`ENCLAVE_GATEWAY_PUBLISHED_PORTS=3000/tcp,5353/udp`:

- **Worker:**
  - The exact INPUT rules are the adapter accept and then the hub drop.
  - Packets from the hub to published `3000/tcp` and `5353/udp` are DROP.
  - Packets from the hub to the adapter port are ACCEPT.
  - An ESTABLISHED reply from the hub is ACCEPT.
  - A bystander reaching `3000/tcp` and `5353/udp` is ACCEPT.
  - All earlier cases still hold.
- **Orchestrator:** the only hub INPUT rule is the drop.
  - Packets from the hub to the adapter port and to the published port are
    DROP.
  - An ESTABLISHED reply from the hub is ACCEPT.
  - A bystander reaching the published port is ACCEPT.
- **No overlay:** an absent overlay and a cleared overlay both produce the
  same call sequence as a run without overlay handling, published ports
  included.
- **Comparison with `6fdb667`:** a throwaway dump test, not committed, ran
  against both this branch's script and the `6fdb667` script. For the absent
  and cleared overlays, the full 33-call sequences were byte-identical.

## Live verification (Docker)

Setup:

- Network `a2a-a5b-net` (`172.29.78.0/28`).
- Gateway container from `enclave-gateway-claude:latest` at `.2`, running
  this branch's functions and `net.sh` with the overlay directory mounted,
  and socat listening on 3000 and 41821.
- Hub container at `.14`, listening on 41820.
- Bystander container at `.9`.

| Overlay | hub to :3000 | hub to :41821 | bystander to :3000 | bystander to :41821 | gateway to hub:41820 |
| --- | --- | --- | --- | --- | --- |
| worker (gen 7) | dropped | accepted | accepted | dropped | accepted |
| orchestrator (gen 8) | dropped | dropped | accepted | n/a | accepted |
| cleared (gen 9) | accepted (as today) | n/a | n/a | n/a | n/a |

Each overlay logged its marker (`applied` for gen 7 and gen 8, `cleared`
for gen 9). All `a2a-a5b-*` containers and the network were removed
afterwards.

## Gates

- `make build`, `make test`: ok.
- `make lint-changed`: ok (go vet, golangci-lint, gosec, shellcheck).
- `make check-license-headers`: ok.

## Change requests

None.
