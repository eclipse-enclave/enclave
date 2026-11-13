# Request filters

A request filter lets the agent use a third-party API, such as Google
Workspace through the `gws` CLI, while the gateway holds the credential. The
agent only sees a placeholder. The gateway forwards only the operations the
selected preset allows, checks request content such as mail recipients or
Drive folders, and enforces per-session quotas.

Filters are extensions of `kind: filter`. Enclave ships none. Sessions with a
filter need the Docker or Podman backend, restricted network mode, and the
gateway. Guarantees and residual risks are in the
[security concept](security/request-filters.md).

## Install a filter

The examples on this page use `gws`, a hypothetical filter for Google
Workspace through the `gws` CLI.

```bash
enclave filters add <source> --name gws
enclave filters show gws
```

- `add` and `update` show the capability summary before they write anything.
  Review it and the filter's `README.md` and `SECURITY.md`
  ([Install-time review](security/request-filters.md#install-time-review)).
- `show` lists hosts, credential, capabilities with scopes and constraints,
  counters, and presets. `--preset <p>` adds the merged preset, its required
  scopes, and the grant a session would use.
- `list`, `update`, and `remove` work as for every extension kind
  ([Installing extensions](extensions/installing.md)).
- The filter only governs the API. The client comes separately, usually as a
  feature.

## Log in

OAuth filters need a grant on the host:

```bash
enclave filters login gws --client-secret ~/Downloads/client_secret.json
enclave filters login gws --preset send-eclipsesource
enclave filters grants gws
```

- The first login needs your own OAuth client. The filter's README says how
  to create one. `--client-secret` stores it for later logins. It accepts
  Google's `installed` and `web` JSON shapes and a flat
  `{"client_id", "client_secret"}` document.
- `login` requests the scopes the preset needs (the default preset without
  `--preset`), dropping scopes that another requested scope covers.
  `--scopes a,b` requests a custom set instead.
- It prints the authorization URL and opens a browser (`--no-browser` only
  prints it). The browser redirects to a listener on `127.0.0.1` with a
  random port, which waits 5 minutes.
- The grant is stored under the preset name, or `custom-<hash>` for
  `--scopes`. `--grant <id>` names it, and a login with an existing ID
  replaces that grant. If you deselect scopes on the consent screen, `login`
  warns about the missing ones.
- `--import <file>` stores an existing `authorized_user` document with
  `client_id`, `client_secret`, and `refresh_token`, such as the output of
  `gws auth export --unmasked`, after one test refresh. Scopes come from the
  token response, or from `--scopes` when it names none. `logout` never
  revokes an imported grant ([Manage grants](#manage-grants)).

Static filters have no login. `enclave filters show` names the env aliases
and the file their credential resolves from
([Static](extensions/filter-spec.md#static)).

## Start a session with a preset

```bash
enclave --filters gws                       # The filter's default preset
enclave --filters gws:send-eclipsesource    # A named preset
```

Or select filters in the global or project config:

```json
{ "filters": ["gws:readonly"] }
```

At start, Enclave prints one line per filter:

```
filter gws: preset readonly, grant readonly
```

- The session uses the grant with the fewest scopes that covers the preset,
  then the newest. Without one, the start fails with the exact
  `enclave filters login` command to run.
- The governed hosts join the session allowlist
  ([Hosts](extensions/filter-spec.md#hosts)). A deny rule of the tool or a
  feature still blocks them, and session start warns about each host the
  preset uses that a deny rule covers.
- A filter with [`confine`](extensions/filter-spec.md#confine) blocks every
  other name below its confined domains. Session start names the allowlisted
  domains it blocks.
  `enclave network print --filters gws` lists them
  ([Request filter hosts](networking.md#request-filter-hosts)).
- The policy is fixed at start. Preset edits and new grants apply to the next
  session.

What the agent sees:

| Item | Content |
|------|---------|
| The filter's `credential.env` variables, for gws `GOOGLE_WORKSPACE_CLI_TOKEN` | The placeholder, new every session |
| `ENCLAVE_ACTIVE_FILTERS` | The active filters as `name:preset`, comma-separated |
| `ENCLAVE_FILTER_SUMMARY_DIR` | `/opt/enclave/filters`, read-only, one [policy summary](#policy-summary) per filter as `<name>.json` |

## Write a user preset

User presets live on the host, never in the project:

```bash
mkdir -p ~/.config/enclave/filters/gws/presets
$EDITOR ~/.config/enclave/filters/gws/presets/project-docs.yaml
```

```yaml
description: readonly, plus editing the files of one project folder.
extends: readonly
allow:
  files.edit: { resources: { within: ["<folder ID>"] } }
quotas:
  files.written: 50
```

```bash
enclave filters show gws --preset project-docs   # Merged result, scopes, grant
enclave filters login gws --preset project-docs  # If no grant covers it
enclave --filters gws:project-docs
```

- A user preset shadows a shipped preset of the same name, also for shipped
  presets that extend it. Lookup and merge rules are in
  [Presets](extensions/filter-spec.md#presets).
- Capability, constraint, and counter names are the filter's own, listed by
  `enclave filters show`.

## Read denials in the network log

```bash
enclave network log --verdict deny
enclave network log --json | jq 'select(.filter != null)'
```

Denials and passes on governed hosts are `http` events with rule `filter` and
the fields `filter`, `op`, `capability`, and `reason`
([What is recorded](networking.md#what-is-recorded)). The text view shows
`<filter>:<op>` and the reason in the rule column.

The client receives the denial in the filter's error format, or as
`text/plain`. The message starts with `filter <name>: ` and names the
operation, capability, preset, and offending value, for example
`filter gws: gmail.messages.send: recipients "x@example.org" is not allowed
by preset send-eclipsesource`.

| Status | `{{status}}` | Cause |
|--------|--------------|-------|
| 403 | `PERMISSION_DENIED` | Any policy denial |
| 403 | `RESOURCE_EXHAUSTED` | Quota exhausted. Never 429, which clients retry |
| 502 | `UNAVAILABLE` | The access token could not be refreshed. The message asks for `enclave filters login` when the grant or client was rejected, and otherwise says the token endpoint is unreachable or failed |

## Manage grants

```bash
enclave filters grants gws                    # ID, source, creation time, scopes
enclave filters logout gws --grant readonly
enclave filters logout gws --all
```

- `logout` without flags deletes the only grant and asks for `--grant` or
  `--all` when there are several. It revokes each grant at the filter's
  revoke endpoint, best effort.
- It deletes without revoking an imported grant, whose refresh token your
  own client still uses, and a grant whose OAuth client another remaining
  or imported grant uses, since providers such as Google revoke the whole
  authorization of a client. `--no-revoke` deletes without revoking at all.
- A filter whose spec does not load fails `logout` unless you pass
  `--no-revoke`. A removed filter's grants are deleted without revoking.
- `enclave filters remove gws` keeps the OAuth client, grants, and user
  presets. With grants left, its confirmation names the `logout` command to
  run first, since `logout` cannot revoke without the spec.
- Grant files that do not load are skipped with a warning at session start
  and in `filters show`.
- `enclave cleanup`, even with `--all`, never deletes grants.

## Reference

### Selection

The `filters` key and `--filters` take a
[selection](configuration.md#selections) keyed by filter name:

| Entry | Effect |
|-------|--------|
| `gws` | gws with its default preset |
| `gws:readonly` | gws with the preset `readonly` |
| `+gws:send-any` | Adds gws, or replaces the preset a lower layer selected |
| `-gws` | Removes gws, whatever its preset and position. Removing a filter that is not installed only warns |
| `none`, or `[]` in config | No filters |

- No filter is selected by default.
- Selecting the same filter twice, `none` with other entries, or an empty
  value such as `--filters ,` is an error.
- Filters apply to `run`, `shell`, `continue`, and `resume`. `enclave ps
  --json` records them as `name:preset`
  ([Session provenance](integration.md#session-provenance)).

### Startup checks

The session fails before any image build when:

| Check | Fix |
|-------|-----|
| The backend cannot enforce filters (`qemu`), or `--disable-gateway`, `--allow-all-network`, or an unrestricted policy mode is set. One error names every option to remove, before other option checks | Use Docker or Podman in restricted mode |
| The tool can run as root: `--admin`, `--allow-root` or `ENCLAVE_ALLOW_ROOT`, `--runtime-uid-remap`, or a devcontainer `remoteUser` of root. Root could take the proxy's UID and bypass the firewall. Named in the same error | Run without them |
| A filter is not installed, or has no such preset. The error lists the installed filters or the available presets | `enclave filters list`, `enclave filters show <name>` |
| A filter is selected twice, or `none` comes with filters | Fix the selection |
| No grant covers the preset | Run the `enclave filters login` command the error names |
| A static credential is missing | Set the env var on the host or in Enclave's secrets, or create the file the error names |
| Two filters govern a common name, one host equal to or below another | Select one of them |
| A name a filter governs is a release host of the tool's or a feature's credential | Disable the feature, or drop its credential |
| A `credential.env` variable is already set by the tool, a feature, another filter, or `pass_env` | Drop the conflicting `pass_env` entry or feature |

A project `.env` entry for a filter's env var is dropped with a warning.

### Policy summary

`$ENCLAVE_FILTER_SUMMARY_DIR/<name>.json` describes the active policy for the
agent and its skills. It holds neither the placeholder nor any credential.

```json
{
  "schemaVersion": "1",
  "kind": "filter-summary",
  "filter": "gws",
  "preset": "send-eclipsesource",
  "hosts": ["www.googleapis.com", "gmail.googleapis.com"],
  "confine": ["googleapis.com"],
  "capabilities": [
    {
      "name": "mail.send",
      "description": "Send new messages to allowed recipients",
      "enabled": true,
      "constraints": {
        "recipients": { "kind": "email", "value": { "values": ["@eclipsesource.com"] } }
      },
      "operations": ["gmail.messages.send"]
    }
  ],
  "counters": [
    { "name": "mail.sent", "description": "Messages sent", "unit": "messages", "quota": 20 }
  ],
  "deniedOperations": ["drive.permissions"]
}
```

| Field | Content |
|-------|---------|
| `confine` | The [confined domains](extensions/filter-spec.md#confine), absent when none |
| `capabilities` | Every capability, sorted, with `enabled`, `public` when set, and the routes mapped to it as `operations` |
| `constraints.<name>.value` | `{"any": true}` for `"*"`, `values` for `email` and `value`, `ids` and `within` for `resource` |
| `counters` | Every counter. `quota` is absent when the preset sets none |
| `deniedOperations` | Operations of deny routes |

### Host files

| Path | Content |
|------|---------|
| `~/.config/enclave/extensions/filters/<filter>/` | The installed filter |
| `~/.config/enclave/filters/<filter>/presets/<preset>.yaml` | User presets |
| `~/.local/state/enclave/filters/<filter>/client.json` | OAuth client ID and secret, mode 0600 |
| `~/.local/state/enclave/filters/<filter>/grants/<id>.json` | Grants with their refresh tokens, mode 0600 |
| `~/.local/state/enclave/projects/<hash>/<tool>/filter-summaries/<key>/` | Policy summaries, rewritten at each start, mounted read-only |

`<key>` is the config store key ([Store types](runtime/stores.md#store-types)).
Nothing under `~/.local/state/enclave/filters/` is ever mounted into a
container. macOS paths: [Host directories](configuration.md#host-directories).

### Deny reasons

| `reason` | Cause |
|----------|-------|
| `plain-http` | Plain HTTP to a governed host |
| `ambiguous` | A request the API could read differently: encoded separators or dot segments in the path, a character outside RFC 3986 such as a raw `#`, a query parameter name outside `[A-Za-z0-9._~-]` or spelled like a checked one, a JSON key spelled like a checked one, method override headers, `Upgrade`, trailers, a body without a valid `Content-Type`, repeated `Content-Type` or `Content-Encoding`, or a TLS server name that differs from `Host` |
| `foreign-credential` | An `Authorization` header without the placeholder, a `denyHeaders` header, or a `denyParams` parameter |
| `placeholder` | The placeholder in another header, a header name, or the URL, or sent in them to a host the filter does not govern. Bodies are not scanned |
| `no-route` | No route matches |
| `deny-route` | A route that always denies matches |
| `form-body` | A form-encoded body on a route that does not allow one |
| `capability` | The preset does not enable the route's capability |
| `missing-credential` | The request lacks the placeholder in the injection header |
| `body` | The body is unreadable, compressed, over the inspection limit, not a JSON object with a JSON `Content-Type` where a route's conditions read it, or a multipart body the filter rejects |
| `bind` | A value could not be read from the request |
| `constraint` | A constraint failed, such as a recipient or a file outside the allowed folder |
| `resolver:<cause>` | A resolver lookup a constraint needs failed: `http-<status>`, `unreachable`, `bad-response`, `invalid-id` (the ID has other characters than `[A-Za-z0-9._~-]`), or `token:<cause>` |
| `quota` | A quota is exhausted |
| `token:<cause>` | The access token refresh failed. The cause is the OAuth error code, such as `invalid_grant`, `unauthorized_client`, or `invalid_client`, after which only `enclave filters login` helps, or `unreachable`, `http-<status>`, `bad-response`, or `canceled` |
| `response` | A counted response was compressed, unreadable, not JSON, a redirect, or over the inspection limit |
| `not-governed` | The `Host` header names a host the filter does not govern |

A placeholder of another tool's or feature's credential on a governed host is
denied with rule `secret-injection`.

## Limitations

- Docker and Podman only, in restricted mode with a gateway, and without
  root in the tool container. `enclave exec --admin` into a filtered session
  fails.
  `enclave network set-mode unrestricted` fails while a session in its scope
  runs filters ([Request filter hosts](networking.md#request-filter-hosts)).
- Quotas live in the gateway's memory: they reset when the session or gateway
  restarts, and parallel sessions each get their own.
- One grant, so one account, per filter and session.
- A refresh token the provider rotates is used in memory only and never
  written back to the grant.
- Responses are not filtered: list operations return what the API returns.
- Two sessions of the same tool in one project share one gateway config
  directory. Starting another session rewrites the allowlist files of a
  running filtered session there, which then may no longer list the governed
  hosts, feed the governed ipset, or blackhole the confined domains. The
  running gateway keeps what it loaded, since it reads the files only at
  start and on a reload. `enclave network apply` writes each gateway's files
  right before it reloads that gateway, so its own hosts and confined domains
  come back.
