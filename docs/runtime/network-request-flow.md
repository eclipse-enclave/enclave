# Network Request Flow

This document describes how enclave handles outbound network requests in restricted mode, including DNS policy enforcement, proxying, and secret placeholder rewriting.

## Diagram

```mermaid
sequenceDiagram
    autonumber
    box Host
        participant Host as Host runtime<br/>(internal/runtime)
    end
    box Agent Container
        participant Tool as Tool process<br/>(agent CLI)
    end
    box Gateway Container
        participant Entry as gateway-entrypoint.sh
        participant DNS as dnsmasq
        participant FW as iptables + ipset
        participant Proxy as enclave-gateway-proxy
        participant Audit as enclave-gateway-proxy<br/>-dns-audit
        participant Log as network.log<br/>(JSONL)
    end
    box External
        participant API as Upstream API
    end

    Note over Host,API: Session startup
    Host->>Entry: Start gateway with config bundle<br/>(dnsmasq.conf, domains.txt, meta.json)
    Host->>Entry: Mount optional secret mapping JSON<br/>and TLS root (ca.crt/ca.key/hosts)
    Entry->>DNS: Start with rendered allowlist config
    Entry->>FW: OUTPUT DROP + local DNS allow<br/>NAT redirect 80/443 to 8080/8443
    Entry->>Proxy: Start transparent proxy
    Entry->>Audit: Start DNS audit translator on the dnsmasq log
    Host->>Log: Rotate above the size cap<br/>and append session start marker

    Note over Host,API: HTTPS request
    Tool->>DNS: Resolve api.example.com
    alt Domain in allowlist
        DNS->>FW: Add resolved IP to enclave_allowed (ipset)
        DNS-->>Tool: Return A/AAAA answer
        Tool->>FW: Connect api.example.com:443
        alt Destination IP not in enclave_allowed
            FW-->>Tool: Drop connection (fail-closed)
            FW->>Log: TCP deny (allowlist)
        else Destination allowed
            FW->>Proxy: Redirect tcp/443 to :8443
            Proxy->>Proxy: Parse SNI from ClientHello
            alt Host requires MITM (secret rule host match)
                Proxy->>Proxy: Terminate TLS using local CA leaf cert
                Proxy->>Proxy: rewriteHeaders()<br/>ENCLAVE_SECRET_* to real secret
                alt Placeholder used for unauthorized secret host
                    Proxy-->>Tool: HTTP 403 Forbidden
                    Proxy->>Log: HTTP deny (secret-injection)
                else Rewrite allowed
                    Proxy->>API: Forward HTTPS request
                    API-->>Proxy: Upstream response
                    Proxy-->>Tool: Return response
                    Proxy->>Log: HTTP pass (allowlist)
                end
            else Host allowlisted but no secret rule match
                Proxy->>API: TLS passthrough tunnel
                API-->>Proxy: TLS response
                Proxy-->>Tool: TLS response
                Proxy->>Log: TCP pass (allowlist)
            end
        end
    else Domain not in allowlist
        DNS-->>Tool: NXDOMAIN from the blackhole
        DNS->>Audit: dnsmasq log line
        Audit->>Log: DNS deny (nxdomain)
    end

    Note over Tool,Proxy: When secret injection is active, the container sees placeholders only.<br/>Real secrets are released only in-flight for matching host rules.
```

## Flow Summary

1. The host runtime writes a gateway config bundle (`dnsmasq.conf`, `domains.txt`, `meta.json`) and starts the sidecar.
2. `gateway-entrypoint.sh` applies a fail-closed firewall (`OUTPUT DROP`), starts `dnsmasq`, and redirects outbound `tcp/80` and `tcp/443` to `enclave-gateway-proxy`.
3. DNS resolution goes through `dnsmasq`. Allowlisted domains resolve and populate the `enclave_allowed` `ipset`; non-allowlisted domains fail resolution.
4. TCP connections are gated by firewall rules and redirected to the proxy. The proxy checks host allowlist rules again using HTTP `Host` / TLS `SNI`.
5. For hosts that match declared secret `release.http` rules, or for all allowlisted HTTPS when `network_log=requests`, the proxy uses TLS MITM before forwarding upstream.
6. If a placeholder appears on plaintext HTTP or on a host outside that secret rule's `hosts`, the proxy blocks the request with HTTP 403.
7. `network_log=coarse` writes pass/deny events to `~/.local/state/enclave/projects/<hash>/<tool>/logs/network.log`: one `tcp` event per TLS connection at the ClientHello, and an `http` event per request for plaintext HTTP and for the MITM'd hosts of step 5. `network_log=requests` adds request-level HTTP/HTTPS audit events for every allowlisted host. The log is JSONL throughout: the proxy appends HTTP and TCP events, a separate `enclave-gateway-proxy -dns-audit` process translates dnsmasq's own log into `dns` events (so DNS denials are recorded even when the proxy is disabled), and the host appends a session marker at gateway start. Read it with `enclave network log`.

## Why This Works

- Real API key values for mapped secrets are never exposed inside the tool container environment.
- Egress checks happen at multiple layers (DNS allowlist, firewall/IP set, proxy host checks).
- Gateway reload/startup failures fail closed rather than silently allowing unrestricted egress.
- Host-side network logs preserve an auditable record of pass/deny decisions, tagged with the session that produced them and rotated at session start rather than truncated.
- Logged HTTP paths never include the query string, so credentials passed as query parameters cannot leak into the log.

## Scope and Limits

- In unrestricted mode (`--allow-all-network` or policy mode `unrestricted`), the gateway is bypassed and HTTP secret release is disabled.
- `--network-log=requests` enables request-level MITM logging in restricted mode.
- The log records decisions, not traffic volume. In `coarse` mode a reused TLS connection produces a single `tcp` event no matter how many requests it carries, and successful DNS lookups are never recorded, so an absence of events is not evidence of an absence of traffic. See [Coverage and granularity](../networking.md#coverage-and-granularity).
- Placeholder protection applies only to declared secrets with `release.http` in the selected tool profile or enabled feature manifests.
- Real secrets are released only on HTTPS requests; plaintext HTTP requests carrying a placeholder are denied.
- Current secret injection is header-based; file credential mediation is separate work.

## Related Implementation

- [`internal/runtime/network_manager.go`](../../internal/runtime/network_manager.go)
- [`internal/runtime/auth_manager.go`](../../internal/runtime/auth_manager.go)
- [`internal/gateway/bundle/bundle.go`](../../internal/gateway/bundle/bundle.go)
- [`internal/gateway/mitm/proxy.go`](../../internal/gateway/mitm/proxy.go)
- [`gateway-entrypoint.sh`](../../gateway-entrypoint.sh)
