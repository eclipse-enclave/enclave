# Credentials and providers

How tool and feature specs declare secrets (`credentials.sources`), how the
gateway injects them as HTTP headers (`network.serviceDomains`,
`network.serviceAuth`), and how Enclave detects a login (`providers`). Tools
may declare all of it, features all but `providers`. The service id is the same
key in each map. For how users supply credentials, see
[Authentication and secrets](../auth.md).

From `extensions/tools/claude/spec.yaml`:

```yaml
credentials:
  sources:
    anthropic-api-key: { env: [ANTHROPIC_API_KEY] }
    claude-code-oauth-token: { env: [CLAUDE_CODE_OAUTH_TOKEN], apiKey: false }

network:
  serviceDomains:
    api.anthropic.com: anthropic-api-key
    "*.anthropic.com": anthropic-api-key
  serviceAuth:
    anthropic-api-key: { headerName: x-api-key }
    claude-code-oauth-token: { headerName: authorization, valueFormat: "Bearer %s", hosts: [api.anthropic.com] }

providers:
  - name: anthropic
    credentials: [anthropic-api-key, claude-code-oauth-token]
    authFiles: [config.json, .credentials.json]
    securestorageDirEnv: CLAUDE_SECURESTORAGE_CONFIG_DIR
    authSession:
      mode: any
      checks:
        - { file: config.json, type: file_exists }
        - { file: .credentials.json, type: file_exists }
```

## Credential sources

| Field | Meaning |
|-------|---------|
| `env` | One or more env-var aliases for the same credential; an alias belongs to one source only. See [Env aliases](../auth.md#env-aliases) for how aliases resolve against each other. |
| `apiKey` | Enclave-native. Omitted or `true`: an API key. `false`: an OAuth or session token. |
| `file.path` | Optional host file to read the secret from; supports `~` |
| `file.parser` | Empty for the trimmed raw file content, or `json:<dot.path>` (for example `json:auth.token`) to extract a scalar from a JSON document |
| `priority` | `env-first` (default) reads the env aliases before the file; `file-first` the reverse |
| `placeholder` | Custom placeholder shape; see [Client-side credential format checks](#client-side-credential-format-checks) |

- The `json:` dot-path is split on `.`; it is not an RFC 6901 JSON Pointer. A
  malformed parser is a load error; a missing file or key silently falls back
  to the env aliases.
- `--no-api-key` suppresses only credentials whose `apiKey` is unset or `true`.

## Gateway header injection

| Field | Meaning |
|-------|---------|
| `serviceDomains.<host>` | Service id whose credential this host receives |
| `serviceAuth.<id>.headerName` | Header the credential goes into |
| `serviceAuth.<id>.valueFormat` | Go `fmt`-style format with `%s` (not `{secret}`). Empty: the raw value with no wrapping. Non-empty: must contain `%s`, for example `"Bearer %s"`. |
| `serviceAuth.<id>.hosts` | Enclave-native. Hosts declared on the service itself |
| `serviceAuth.<id>.requiredValuePrefix` | Limits gateway protection to one credential type |
| `serviceAuth.<id>.hostsFromCredential` | Release to a self-hosted instance named by another credential |

### Pairing rules

The spec is rejected at load unless every service id in `serviceDomains` and
`serviceAuth`, and every `hostsFromCredential` reference, names a
`credentials.sources` id, and the pairing is complete both ways:

- Every service id in `serviceDomains` has a `serviceAuth` entry (otherwise
  the hosts would never reach the allowlist and the credential would be
  injected as a raw env value).
- Every `serviceAuth` entry has a host, from `serviceDomains` or its `hosts`.

Hosts that only need to be reachable belong in `network.allowedDomains`.

### Several services on one host

`serviceDomains` maps a host to one service id; `serviceAuth.<id>.hosts`
lets several services share a host, as for the three `gitlab-cli` tokens:

```yaml
network:
  serviceAuth:
    gitlab-token:        { headerName: private-token, hosts: [gitlab.com, "*.gitlab.com"] }
    gitlab-oauth-token:  { headerName: authorization, valueFormat: "Bearer %s", hosts: [gitlab.com, "*.gitlab.com"] }
    gitlab-job-token:    { headerName: job-token, hosts: [gitlab.com, "*.gitlab.com"] }
```

### Required value prefix

While gateway protection is active, startup fails unless the real value starts
with this literal (at most 256 bytes of non-whitespace printable ASCII). It
does not apply to unprotected env injection. Codex uses `"at-"` to accept
personal access tokens and reject Agent Identity JWTs, whose client-side
signature check cannot accept a placeholder.

### Self-hosted instances (`hostsFromCredential`)

`hostsFromCredential` names another `credentials.sources` id whose resolved
value is a host, for services that can point at a self-hosted instance.

| Credential state | Release target | Allow set |
|------------------|----------------|-----------|
| Unset | The declared `hosts` and `serviceDomains` entries | Unchanged |
| Set to a usable host | Only that host, matched exactly | The host is added, with the usual subdomain reach; declared hosts stay reachable |
| Set to an unusable value | Ignored, with a warning | Unchanged |

- The service still needs `hosts` or `serviceDomains` for the pairing rules.
- Replacing the declared hosts keeps an instance token away from the public
  host. Unlike declared hosts, the selected host does not cover subdomains:
  `gitlab.example.com`, not `runner.gitlab.example.com`.
- Values may be a host, `host:port`, or URL (scheme, port, and path are
  stripped); wildcards are rejected.
- The value is not persisted to the env store, so it lasts only for the run.

## Placeholders

With the gateway enabled, credentials with a `serviceAuth` entry appear in the
container as random placeholders (for the aliases in
[`environment.proxyManaged`](spec-reference.md#environment-environment)). The
MITM proxy substitutes the real value only in `headerName` of HTTPS requests to
the declared hosts and denies inspected requests that break the release rule.
Credentials without `serviceAuth` are plain env vars. A placeholder generation
error aborts startup instead of falling back to the real value. User-facing
behavior is in [Authentication](../auth.md#api-keys).

Only add `serviceAuth` for credentials the client reads from the environment
without persisting them. A client that saves the placeholder to a file cannot
reuse it next session; file-backed and refreshable logins need separate
handling ([#133](https://github.com/eclipse-enclave/enclave/issues/133)). Auth
reconciliation warns about and skips contaminated files but does not intercept
client writes.

### Client-side credential format checks

Some clients reject the default placeholder shape,
`ENCLAVE_SECRET_<48 hex characters>`, before sending a request. Declare a
custom shape with `credentials.sources.<id>.placeholder`:

```yaml
credentials:
  sources:
    example-token:
      env: [EXAMPLE_API_TOKEN]
      placeholder:
        prefix: "vendor_"
        suffix: "_end" # optional
        random: { encoding: hex, length: 48 }
```

`prefix` and `suffix` are optional literals, at most 256 bytes each, printable
ASCII without whitespace. `random.encoding` and `random.length` are required:

| Encoding | Random-body characters | Length range |
|----------|------------------------|--------------|
| `hex` | `0-9a-f` | 32–1024 |
| `base64url` | `A-Za-z0-9-_` (no padding) | 22–1024 |
| `alphanumeric` | `A-Za-z0-9` | 22–1024 |

- Length counts the random body only, sampled uniformly; the minimums give at
  least 128 bits of entropy. Static placeholders are not supported: values are
  fresh per session, shared across the credential's proxy-managed aliases, and
  never derived from the secret.
- The gateway replaces the whole value, with the usual HTTPS and release-host
  rules. A matching `serviceAuth` entry is required; it only applies while
  secret release is active, and it does not turn raw env injection into
  proxying.
- It covers syntactic client checks, not cryptographic validation, signing, or
  client-side transformations.
- Include `ENCLAVE_SECRET_` in the prefix or suffix when the client allows it,
  so the contamination guard also recognizes placeholders from earlier
  sessions; otherwise only the current session's values are checked.

## Providers

`providers[]` is Enclave-native and tool-only. It describes an auth provider,
as opposed to a raw credential.

| Field | Meaning |
|-------|---------|
| `name` | Provider name |
| `credentials` | List of `credentials.sources` keys; an undeclared key is an error |
| `authFiles` | Auth files, relative to `sandbox.configDir`; copied by `enclave auth import` |
| `authSession` | Session detection: `mode` and `checks` |
| `oauthPorts` | OAuth loopback callback ports; see [OAuth callback ports](#oauth-callback-ports-oauthports) |
| `securestorageDirEnv` | Env var the tool reads to locate its credential-storage directory |

### Session detection (`authSession`)

Detection is per provider. By default a session exists if any of the
provider's auth files exists. For multi-provider auth files, set `mode` (`any`,
the default, or `all`) and `checks`. Each check has `file`, `type`, and, for
the JSON Pointer types, `pointer`:

| Check type | Passes when |
|------------|-------------|
| `file_exists` | The file exists |
| `json_pointer` | An RFC 6901 JSON Pointer resolves in a JSON file; `null` counts as present |
| `json_pointer_non_null` | The JSON Pointer resolves to a non-null value |

### OAuth callback ports (`oauthPorts`)

Each entry maps a loopback callback port, for example
`oauthPorts: [{ port: "1455" }]`. When the exact `-p <port>:<port>` mapping
exists, Enclave forwards it to the container's loopback interface.

| Field | Default | Meaning |
|-------|---------|---------|
| `port` | required | Callback port, as a string |
| `autoHintWhenNoSession` | `true` | Map the port automatically while the provider has no session |
| `requireMappingWhenNoCredentials` | `true` | With no session and no env credential, fail startup unless the port is mapped exactly and free on the host |

User-facing behavior per tool is in
[Authentication](../auth.md#oauth-callback-ports).

### Native auth declarations (internal)

`providers[].authSecrets` and `tokenEndpoint` are reserved for the internal protected-auth
implementation. They do not enable a protected session or change ordinary auth
behavior. Claude, Codex, Pi, OpenCode and Copilot enable them with `--protect-auth`; see
[protected native auth](../auth.md#protected-native-auth).

| Field | Meaning |
|-------|---------|
| `authSecrets[].file` | One declaration owner per relative path across the profile; must appear in its owner’s `authFiles` unless `checkOnly` |
| `authSecrets[].checkOnly` | Inspect credential pointers in the config store; refuse raw values, leave metadata untouched, never project or reconcile the file |
| `authSecrets[].codec` | Native format; projection currently implements `claude-credentials`, `claude-config`, `codex-auth`, `provider-map` and `copilot-config` |
| `authSecrets[].fields[].pointer` | RFC 6901 object-field pointer; at most one `*` segment; arrays are not supported |
| `fields[].kind` | `access`, `refresh`, `id`, or `apiKey` |
| `fields[].release` | Existing `network.serviceAuth` id; required for access/API keys, forbidden for refresh/ID tokens |
| `fields[].placeholder` | Opaque prefix/suffix/random shape, or `shape: jwt` for access/ID fields; API keys reuse their env placeholder |
| `tokenEndpoint` | Exact normalized `host`, absolute `path`, `json`/`form`/`either` body, allowed grants, response fields, and explicit deny host/path pairs |

Every file placeholder contains `ENCLAVE_SECRET_`. Base64url shapes include the
marker within the declared random-body length and retain at least 128 random
bits. Hex/alphanumeric shapes place the marker before their random body.
JWT placeholders retain readable header/payload claims and use a bound signature;
refreshes update the claims without invalidating older cached JWT placeholders.
Provider-map slots use their concrete provider key as the binding namespace.
Unknown provider credentials refuse rather than being copied. API-key fields use the configured env
credential's placeholder and refuse a raw native key when no such credential is
configured. A native access field may reference an HTTP release source without
an env alias; unrelated empty-env sources still refuse.

For a shared provider-map file, declare all its fields once. Each token endpoint
uses the profile's single supported exchange file; the endpoint provider need not
own that declaration. Duplicate owners or ambiguous native files refuse. The
provider's concrete map key still namespaces bindings, independent of the file's
owner. Copilot's equal access/refresh slots and legacy/current account keys share
one opaque placeholder. Other credentials keep independent bindings. Copilot’s
leading JSONC headers are read from canonical files and stripped from views;
regular native paths containing comment bytes remain a strict refusal.

The gateway reads only declared, bounded regular files and publishes private
per-session views. `bindings.json` stores placeholder identities, not grants;
native files remain canonical. Unknown keys are stripped from projected views
but stay in the canonical files. Native-path classification is strict: unknown
keys in an original regular file are refused, since keeping it would retain
those bytes. Raw or foreign native config files are refused, never overwritten. Native-path operations are rechecked by the entrypoint;
ordinary reconciliation is not used for a protected view.

### Shared credential storage (`securestorageDirEnv`)

In the `shared` auth scope, Enclave points this env var at the shared auth
store mount, so the tool writes its credentials there. Claude uses
`CLAUDE_SECURESTORAGE_CONFIG_DIR` so concurrent sessions coordinate token
refresh ([Authentication](../auth.md#claude-and-concurrent-sessions)).

## Auth hooks (Go)

Tools with a `go/` directory can register auth hooks with
`auth.RegisterHooks()`. They run in a fixed order during runtime auth
preparation:

| Hook | Runs |
|------|------|
| `OnAuthReady` | After auth stores are prepared. Use `StorageHasSession` to gate behavior; it is true when any provider session is detected through `providers[].authSession` or auth file presence. |
| `AfterEnvInjected` | After API keys are injected as env vars |
| `FinalizeAuth` | Last chance to adjust config stores before the container starts |

After adding Go code, run `go generate ./cmd/enclave`; see
[Go code](README.md#go-code-tools-only).

## Checklist for authors

- `credentials.sources` and `providers[].credentials` cover every env-based
  auth input.
- OAuth and provider tokens are marked `apiKey: false` when `--no-api-key`
  should not suppress them.
- `enclave auth import --tool <tool>` copies the required auth files
  (`providers[].authFiles`).
