# DNS Allowlist Layout

This folder contains per-agent DNS allowlist presets and shared fragments.

## Presets
Per-agent allowlist presets live at the top level, e.g. `pi.conf`, `claude.conf`.

## Fragments
Reusable fragments live in `fragments/` and are included with:

```
conf-file=/etc/dnsmasq.allowlists/fragments/<name>.conf
```
