# github-cli

GitHub CLI (`gh`) for interacting with GitHub repositories, issues, and pull
requests from the command line. Enabled by default.

**Priority**: 50

## Installation

The ordinary client is installed from GitHub's official apt repository. A
separate **2.87.3** binary at `/opt/enclave/gh-2.87.3/bin/gh` is downloaded from
the official release and checked against a pinned SHA-256 for Linux amd64/arm64.
Protected sessions select and version-check that binary. Installation requires
root. An old image missing the pin fails protected startup, even with
`--no-rebuild`.

## Auth

Ordinary sessions use the shared Enclave feature auth store at
`~/.local/state/enclave/features/github-cli/auth/` (with platform/XDG path
resolution):

- `hosts.yml` — authentication tokens
- `config.yml` — CLI preferences

Declared `GH_TOKEN`/`GITHUB_TOKEN` and Enterprise env-secret sources are also
available in ordinary mode. See [authentication](../../../docs/auth.md).

### Experimental protected auth

When enabled with Claude's `--protect-auth` flow, the built-in feature keeps its
real native credentials gateway-only. The workload gets a writable
`~/.config/gh` bind mount containing session-bound token handles. The gateway
substitutes credentials only in the adapter's approved GitHub.com API and HTTPS
Git authentication fields. Native view files are never reconciled into the
trusted feature store.

Use GitHub.com device login from inside the protected session. PAT paste,
Enterprise hosts, keyring-only credentials, expiry/refresh-bearing token
responses and unsupported native/token schemas are not supported. Existing
compatible native feature credentials are consumed
by the gateway without exposing them in the projected view.

The shared feature identity supports concurrent protected sessions across projects
and Docker/Podman, independently of Claude's auth name. Each has separate handles
and a native view; gateway grant updates use a common identity lock. New sessions
receive fresh handles; proxy reload preserves current ones. Saved protection
persists when the flag is omitted. After stopping every session using the identity, `enclave auth reset --feature github-cli` clears its grant and native
source files, including `config.yml` preferences, without removing protection;
an explicit `enclave auth downgrade --feature github-cli`
requires the identity to be idle and empty.

Engine and live GitHub verification are **NOT RUN**. Follow the
[verification handoff](../../../docs/runtime/oauth-broker-verification.md).
This protects captured/saved tokens, not device-login bootstrap material from
an already-malicious client or every operation a token authorizes.
