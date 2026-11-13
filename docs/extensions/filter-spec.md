# Filter spec reference

Every field of a request filter's `spec.yaml` and preset files
(`kind: filter`). The step-by-step guide is
[Adding a filter](adding-a-filter.md), usage is in
[Request filters](../filters.md), and the guarantees behind the rules are in
the [security concept](../security/request-filters.md).

## Format

- A filter has one `spec.yaml`, or alternatively `spec.json`, of at most
  1 MiB. Decoding is strict: an unknown field, or a known one in other case
  such as `Allow`, is a load error, and `schemaVersion` must be `"1"`. The
  same holds for presets.
- `name` must match the directory name.
- Validation runs at `add`, `update`, `enclave validate-extensions`, and every
  session start, which resolves the selected preset. Every shipped preset is
  resolved at load. The gateway validates the compiled policy again.
- The authoritative shape is `specDocument` in
  [`internal/filter/spec/document.go`](../../internal/filter/spec/document.go).

```
<name>/
├── spec.yaml
├── README.md               # required by convention, as for every extension
├── SECURITY.md             # optional, what each capability and preset allows
├── presets/<preset>.yaml   # shipped presets
└── commands/host/...       # optional host commands, same rules as every kind
```

Anything else, such as `install.sh`, `files/`, `skills/`, or `go/`, is
reported as ignored.

| Name | Rule |
|------|------|
| Filter, preset, capability, counter | `[a-z0-9][a-z0-9._-]*`, at most 128 characters |
| Operation (`op`), resolver, constraint | `[A-Za-z0-9][A-Za-z0-9._-]*`, at most 128 characters |
| Variable, path capture | `[A-Za-z_][A-Za-z0-9_]*` |
| Env var | `[A-Z_][A-Z0-9_]*`, not starting with `ENCLAVE_` |

## Top-level fields

```yaml
schemaVersion: "1"
kind: filter
name: example
displayName: Example API
description: Example API used by the example CLI
defaultPreset: readonly      # required when presets/ ships presets
jsonKeys: protobuf           # optional
hosts: [api.example.com]
confine: [example.com]       # optional
credential: {...}
resolvers: {...}             # optional
counters: {...}              # optional
capabilities: {...}
routes: [...]
deny: {...}                  # optional
```

| Field | Required | Meaning |
|-------|----------|---------|
| `schemaVersion`, `kind`, `name` | Yes | `"1"`, `filter`, the directory name |
| `displayName`, `description` | No | Shown by `list`, `show`, and the capability summary |
| `defaultPreset` | With presets | Preset used by a bare `--filters <name>`. Must name a shipped preset |
| `jsonKeys` | No | `protobuf`, see [JSON keys](#json-keys) |
| `hosts` | Yes | Governed hosts, see [Hosts](#hosts) |
| `confine` | No | Domains whose other names are blocked, see [Confine](#confine) |
| `credential` | Yes | See [Credential](#credential) |
| `resolvers`, `counters` | No | See [Resolvers](#resolvers), [Counters](#counters) |
| `capabilities` | Yes | At least one, see [Capabilities](#capabilities) |
| `routes` | Yes | At least one, see [Routes](#routes) |
| `deny` | No | Deny response format, see [Deny responses](#deny-responses) |

## Hosts

`hosts` lists the host patterns the filter governs: exact names such as
`api.example.com`, or `*.example.com`. A filter governs every name equal to or
below a pattern, the same names its allowlist entry admits: `api.example.com`
governs itself and every subdomain, `*.example.com` governs `example.com` and
every subdomain.

- Every request to a governed name is TLS-terminated and evaluated. Requests
  no route allows are denied, so list hosts the client must not use with your
  credential too, such as the OAuth token host, and give them no routes.
- Route hosts match exactly, so a subdomain without a route of its own gets
  the `no route` denial.
- Governed hosts join the session allowlist. Deny rules of the tool or a
  feature still block them.
- Two filters selected together must not govern a common name, and no
  governed name may be a release host of a selected tool's or feature's
  credential.

## Confine

`confine` lists domains above governed hosts. While the filter is active,
every name equal to or below a confined domain that no selected filter
governs is blocked. Use it where the provider accepts a token the agent
brings on hosts the filter does not govern, such as other Google APIs.

```yaml
hosts: [gmail.googleapis.com, www.googleapis.com]
confine: [googleapis.com]
```

- Each entry is a plain domain with at least two labels and a governed host
  below it, and is not governed itself.
- It also blocks allowlist entries of the tool, features, policy files, and
  `--allow-domain` below it. Session start names those.
- A deny rule of the tool or a feature still blocks governed hosts below a
  confined domain.

## Credential

```yaml
credential:
  oauth2: {...}            # exactly one of oauth2 and static
  inject:
    header: authorization
    format: "Bearer %s"
  env: [EXAMPLE_TOKEN]
  placeholder:             # optional
    prefix: "ENCLAVE_SECRET_"
    random: { encoding: hex, length: 48 }
  denyHeaders: [x-api-key, cookie]
  denyParams: [access_token, key]
```

| Field | Meaning |
|-------|---------|
| `oauth2` | OAuth client configuration; users log in with `enclave filters login` |
| `static` | A fixed secret read from the host at session start |
| `inject.header` | Header that carries the credential, case-insensitive |
| `inject.format` | `%s` is the real value, exactly once and no other `%`, printable ASCII. Empty means the raw value |
| `env` | Container env vars that receive the placeholder, at least one. The client sends it in `inject.header`, as the raw value or in `inject.format` |
| `placeholder` | Placeholder shape, same schema as [custom placeholder shapes](credentials.md#client-side-credential-format-checks). Default `ENCLAVE_SECRET_<48 hex>` |
| `denyHeaders` | Headers denied on governed hosts, such as alternative API key headers and `cookie`. Not the injection header |
| `denyParams` | Query parameters denied on governed hosts, matched in every folded spelling, such as `access_token` and `key` |

`Authorization` is always treated as a credential header: if it is not the
injection header, any request carrying it is denied.

### OAuth2

```yaml
oauth2:
  authUrl: https://accounts.example.com/o/oauth2/auth
  tokenUrl: https://oauth2.example.com/token
  revokeUrl: https://oauth2.example.com/revoke
  authParams: { access_type: offline, prompt: consent }
  scopePrefix: "https://www.example.com/auth/"
  scopeImplies:
    files: [files.readonly]
  clientHelp: Create a Desktop OAuth client, see the filter README.
```

| Field | Meaning |
|-------|---------|
| `authUrl`, `tokenUrl` | Required HTTPS URLs without user info or fragment. The gateway sends the refresh token and client secret to `tokenUrl` |
| `revokeUrl` | Optional HTTPS URL, same rules. `filters logout` revokes the refresh token there |
| `authParams` | Extra authorization URL parameters, printable without spaces. The flow parameters (`client_id`, `redirect_uri`, `response_type`, `response_mode`, `scope`, `state`, `code_challenge`, `code_challenge_method`) are reserved. Request offline access here if the provider needs it for a refresh token |
| `scopePrefix` | HTTPS URL prepended to short scope names. Names containing `://` are kept as they are |
| `scopeImplies` | Scope to the scopes it covers, for grant selection and for minimizing login scopes. No self-implication, no cycles |
| `clientHelp` | Appended to the error when a login has no OAuth client yet |

### Static

```yaml
static:
  env: [EXAMPLE_API_KEY]
  file: { path: ~/.config/example/token, parser: "json:token" }
  priority: env-first
```

`env` names env aliases, `file` a host file with the
[parser](credentials.md#credential-sources) of credential sources, at least
one of them. `priority` is `env-first` (default) or `file-first`. The value
resolves like a declared credential: each alias from the host environment,
Enclave's secrets files, and the env store
([Env aliases](../auth.md#env-aliases)), then the file. A missing value fails
the session start, naming both sources.

## Capabilities

A capability is a named operation group with the OAuth scopes it needs. Presets
enable capabilities. Routes map operations to them.

```yaml
capabilities:
  discovery:
    description: API discovery documents
    public: true
  mail.read:
    description: Read message bodies
    scopes: [mail.readonly]
  mail.send:
    description: Send mail
    scopes: [mail.send]
    constraints:
      recipients: { kind: email, var: recipients }
  files.edit:
    description: Edit existing files
    scopes: [files]
    constraints:
      resources: { kind: resource, var: fileId, resolver: parents }
```

| Field | Meaning |
|-------|---------|
| `description` | Shown by `show`, the summary, and the container's policy summary |
| `public` | Always enabled, never injects a credential (the injection header is dropped), no scopes or constraints, no counted routes. Presets cannot name it |
| `scopes` | OAuth scopes, short or full. Only with `oauth2` |
| `constraints.<name>` | `kind`, `var` (the bound variable it checks), `resolver` (`resource` only), and `default` (a constraint value) |

### Constraints

| `kind` | Preset value | Passes when |
|--------|--------------|-------------|
| `email` | `"*"`, or a list of `user@domain`, `@domain`, `@*.domain` | `"*"`, or the bound list is non-empty and every address matches a pattern, ASCII case-insensitively. Non-ASCII domains never match |
| `resource` | `"*"`, or `{ids: [...], within: [...]}` | `"*"`, or the bound list is non-empty and every value is in `ids`, in `within`, or has a resolver ancestor in `within`. `within` needs `resolver` |
| `value` | `"*"`, or a list of strings | `"*"`, or the bound list is non-empty and every value is in the list |

- A preset that enables the capability without setting the constraint gets
  `default`. Without a default the preset is invalid.
- With `"*"` the constraint is not checked and its variable is not bound.
- A route that does not bind the constraint's variable passes only with
  `"*"`. Otherwise it denies with
  `operation <op> cannot be checked against <constraint>`. Validation, and
  so `filters add` and `update`, warns about such routes unless the route
  lists the constraint in `wildcardOnly`.
- A constraint cannot check a variable bound from the response.

## Resolvers

A resolver looks up the parents of a resource, so `within` can allow
everything inside a folder.

```yaml
resolvers:
  parents:
    url: "https://api.example.com/v1/files/{id}?fields=parents"
    extract: "$.parents[*]"
    recursive: true
    maxDepth: 32
    scopes: [files.metadata.readonly]
```

| Field | Meaning |
|-------|---------|
| `url` | HTTPS on a governed host, without port, user info, or fragment. `{id}` exactly once, in the path, path-escaped |
| `extract` | [JSON path](#json-paths) to the parent IDs |
| `recursive` | Walk ancestors. Without it `maxDepth` is 1 |
| `maxDepth` | Levels to walk, default 32, at most 64. Above 1 needs `recursive` |
| `scopes` | Scopes the lookup needs. Required by a preset only when it uses `within` with this resolver |

The gateway sends a `GET` with the real credential and
`Accept: application/json`, bypassing the route table, and never shows the
result to the container. Lookups time out after 10 seconds, read at most
1 MiB, follow no redirects, and are cached for 30 seconds. IDs must match
`[A-Za-z0-9._~-]+`. A failed first lookup fails the constraint, a failure
further up ends that branch of the walk, and a walk stops after 128 lookups
per value.

## Counters

```yaml
counters:
  mail.read: { description: Messages whose content was read, unit: messages }
```

Routes add to counters with `count`, presets set quotas. A counter without a
quota in the active preset is not tracked. See
[Counting](#counting-count).

## Routes

Routes are evaluated in order, and the first match wins. A request no route
matches is denied.

```yaml
routes:
  - op: files.permissions
    action: deny
    reason: sharing changes are never allowed
    method: [GET, POST, PATCH, DELETE]
    host: api.example.com
    path: /v1/files/{fileId}/permissions/**

  - op: messages.get
    capability: mail.read
    method: GET
    host: api.example.com
    path: /v1/messages/{id}
    query:
      format: [full, raw, ~]
    count:
      - { counter: mail.read, key: id }
```

| Field | Meaning |
|-------|---------|
| `op` | Unique operation name, shown in deny messages, the log, and the summary |
| `capability` | Required unless `action: deny` |
| `action` | `allow` (default) or `deny`. A deny route takes no `capability`, `bind`, or `count` |
| `reason` | Deny routes only, at most 256 printable characters, appended to the deny message |
| `method` | String or list of `GET`, `HEAD`, `POST`, `PUT`, `PATCH`, `DELETE`, `OPTIONS` |
| `host` | String or list of host patterns equal to or below a governed host. An exact name matches only itself, `*.example.com` only strict subdomains |
| `path` | [Path template](#path-templates) |
| `query` | [Query conditions](#query-conditions) |
| `queryMatch` | [Match conditions](#match-conditions) |
| `json` | [JSON conditions](#json-conditions) |
| `jsonPresent`, `jsonMatch` | [Match conditions](#match-conditions) |
| `jsonAllowedKeys` | [Allowed keys](#allowed-keys) |
| `bind` | [Binds](#binds), variable name to step list |
| `wildcardOnly` | Constraint names of the capability that the route does not bind on purpose, such as `parents` on a create route without a parent. The route passes them only with `"*"`, without a warning. Naming a constraint the route binds is an error |
| `count` | [Counting](#counting-count) |
| `allowFormBody` | Allow `application/x-www-form-urlencoded` and `multipart/form-data` bodies, denied by default |
| `multipartParts` | Exact part count of a multipart body, 1 to 64. Setting it makes the route parse the body |
| `maxBodyBytes` | Inspection limit for the request body, default 10 MiB, at most 64 MiB |
| `maxResponseBytes` | Inspection limit for counted responses, default 32 MiB, at most 64 MiB |

A condition that does not hold makes the route not match, and matching
continues with the next route. A body that a route's JSON conditions cannot
read denies the request instead, so it never reaches a later route.

### Path templates

| Syntax | Matches |
|--------|---------|
| `/literal` | Exactly, case-sensitive |
| `{name}` | A whole segment of `[A-Za-z0-9_-]+`, bound as variable `name` |
| `{name:regex}` | A segment matching the anchored regex |
| `{name}:suffix` | A capture followed by a literal in the same segment, such as `{documentId}:batchUpdate`. With the default class the literal must not start with a letter, digit, `_`, or `-` |
| `/**` | One or more remaining segments, last only, not captured |
| Trailing `/` | Only requests with a trailing slash |

Segments are compared after decoding each once. Request paths with encoded
`/`, `\`, or NUL, dot segments, or empty segments are denied before matching.

### Query conditions

```yaml
query:
  format: [full, raw, ~]   # one of these values, or absent
  alt: [media]             # exactly media
  pageToken: "*"           # any value, or absent
```

- `~` allows the parameter to be absent, `"*"` allows any value or absence.
  Numbers and booleans compare as strings in their JSON spelling.
- Every occurrence of a repeated parameter must match.
- Parameters the route does not list are allowed with any value.
- A mismatch makes the route not match.
- A request parameter maps to the condition only by its exact name or, with
  `jsonKeys: protobuf`, its exact snake_case alias (`page_token` for
  `pageToken`). Any other spelling that folds to a parameter the route
  constrains or binds (case, `_`) denies the request as ambiguous once
  method, host, and path match.
- Parameter names use `[A-Za-z0-9._~-]`, in the spec and in requests. A
  request with any other name is denied on every governed host.

### JSON conditions

```yaml
json:
  "$.trashed": [false, ~]
```

- Keys are [JSON paths](#json-paths), values JSON scalars that compare as JSON
  values (`1` equals `1.0`). `~` allows the path to select nothing, meaning
  missing or `null`.
- Evaluated on the request body, or on the first part of a `multipart/*`
  body. An empty body or first part has every path absent.
- Every selected value must be allowed.
- The body or first part must declare `application/json` or an
  `application/*+json` type, without a charset other than UTF-8, and hold a
  JSON object. Otherwise the request is denied, so a type the API parses
  differently, such as Google's JSPB (`application/json+protobuf`), cannot
  hide a key.
- A body that does not parse, has duplicate keys, or does not have the shape
  the path expects denies the request. So does a key that folds like a name
  the path reads, see [JSON paths](#json-paths).

### Match conditions

Match conditions select requests by content, for deny routes that shadow a
broader allow route and for routing one operation to different
capabilities.

```yaml
- op: slides.batchUpdate.crossFile
  action: deny
  reason: requests that read other files cannot be checked
  method: POST
  host: slides.example.com
  path: /v1/presentations/{fileId}:batchUpdate
  jsonPresent: ["$..createSheetsChart", "$..replaceAllShapesWithSheetsChart"]
- op: drive.files.list.fullText
  action: deny
  method: GET
  host: www.example.com
  path: /drive/v3/files
  queryMatch: { q: "fullText" }
```

| Field | Matches when |
|-------|--------------|
| `jsonPresent: [<path>, ...]` | Any path selects a value: a scalar, object, array, or `null` |
| `jsonMatch: {<path>: <regex>}` | For every entry, some scalar the path selects matches the regex. Numbers and booleans match in their JSON spelling, objects and arrays are skipped |
| `queryMatch: {<param>: <regex>}` | For every entry, some value of the parameter matches the regex. An absent parameter does not match |

- Expressions are [RE2](https://github.com/google/re2/wiki/Syntax), at most
  1024 characters, and match anywhere in the value unless anchored with `^`
  and `$`. `(?i)` makes them case-insensitive. Invalid expressions fail the
  load.
- JSON paths and the body follow [JSON conditions](#json-conditions):
  `jsonKeys: protobuf` renames keys first, and a body that does not parse
  denies the request.
- `queryMatch` reads values percent-decoded, by the name rules of
  [Query conditions](#query-conditions).
- All conditions of a route must hold. Use recursive descent `..name` to
  catch a request type at any depth, such as inside a batch.

### Allowed keys

`jsonAllowedKeys` maps a JSON path to the only keys the objects it selects
may have. Use it on batch endpoints to allow listed request kinds instead of
denying dangerous ones, so a kind the API adds later matches no allow route.

```yaml
- op: docs.batchUpdate
  capability: files.edit
  method: POST
  host: docs.example.com
  path: /v1/documents/{fileId}:batchUpdate
  jsonAllowedKeys:
    "$": [requests, writeControl]
    "$.requests[*]": [insertText, deleteContentRange, updateTextStyle]
- op: docs.batchUpdate.unsupported
  action: deny
  reason: only text edits are allowed
  method: POST
  host: docs.example.com
  path: /v1/documents/{fileId}:batchUpdate
```

- An object with an unlisted key makes the route not match. `[]` allows only
  empty objects. A path that selects nothing passes.
- A path that selects a value other than an object or `null` denies the
  request.
- Keys use `[A-Za-z0-9_-]`. Only the selected objects are checked, not the
  objects below them.
- The body follows [JSON conditions](#json-conditions).

### JSON paths

The supported subset: `$`, then `.name`, `..name`, `[n]`, and `[*]` steps,
at most 512 characters. Names match keys exactly. `..name` is recursive
descent: it selects the members called `name` of the current value and of
every object below it. Absent members, indexes out of range, and `null`
select nothing, except in `jsonPresent`, where `null` counts. A name step on
a non-object or an index step on a non-array is an error.

A key that folds like `name` but is spelled differently, such as `Trashed`
for `trashed`, is an error in every object a name step reads or a recursive
descent passes. Folding ignores case, `_`, whitespace, and control
characters, as for [query parameters](#query-conditions), since a server
may read the key as `name`. In requests the error denies with reason
`ambiguous`.

### Binds

A bind is a pipeline: a source step, then transforms. Every step yields a list
of strings.

```yaml
bind:
  recipients: [ {body: {}}, {json: "$.raw"}, {base64url: {}}, {rfc822: recipients} ]
  parents: [ {body: {}}, {multipart: {index: 0}}, {json: "$.parents[*]"} ]
  messageIds: [ {response: {}}, {json: "$.messages[*].id"} ]
```

| Step | Kind | Yields |
|------|------|--------|
| `{path: <capture>}` | Source | The path capture |
| `{query: <name>}` | Source | All values of the parameter, by the same name rules as query conditions |
| `{header: <name>}` | Source | All values of the request header |
| `{body: {}}` | Source | The raw request body, buffered up to `maxBodyBytes` |
| `{response: {}}` | Source | The raw 2xx response body, up to `maxResponseBytes`. Makes the variable a response variable |
| `{multipart: "<type prefix>"}` | Transform | The first part, which must have a `Content-Type` starting with the prefix. Directly after `body` or `response` |
| `{multipart: {index: <n>}}` | Transform | Part `n`, 0 to 63. Directly after `body` or `response` |
| `{json: "<path>"}` | Transform | Strings as they are, numbers and booleans in JSON spelling, nothing for `null` or absent. An object or array is an error. Directly after `body`, `response`, or `multipart`, the input must declare a JSON type as in [JSON conditions](#json-conditions) |
| `{base64url: {}}` | Transform | Decoded URL-safe or standard base64, padding optional |
| `{rfc822: recipients}` | Transform | Lowercased addresses of all `To`, `Cc`, `Bcc`, `Resent-To`, `Resent-Cc`, and `Resent-Bcc` fields of the message headers |
| `{rfc822: destinations}` | Transform | `recipients` plus the fields that direct replies, receipts, and bounces: `Reply-To`, `Mail-Reply-To`, `Mail-Followup-To`, `Disposition-Notification-To`, `Return-Receipt-To`, and `Errors-To` |
| `{split: "<sep>"}` | Transform | Each value split, trimmed, empty parts dropped |

- Path captures are bound implicitly under their name. A bind of the same name
  overrides the capture.
- A variable is bound only when a non-`"*"` constraint or a tracked counter
  needs it. Any step error denies the request. A bind that yields nothing binds
  an empty list, which fails every non-`"*"` constraint.
- Multipart parsing rejects ambiguous bodies, and `rfc822` rejects malformed
  header blocks. The exact rules are in
  [Parsing hardening](../security/request-filters.md#parsing-hardening).

### Counting (`count`)

```yaml
count:
  - { counter: mail.read, key: id }     # distinct values of variable id
  - { counter: mail.sent }              # one per allowed request
```

- With `key`, a path capture or a bind of this route, the counter holds the
  set of distinct values. Without, every allowed request adds one. A counter is
  counted with a key on every route or on none.
- Request variables are reserved before forwarding. Response variables are
  counted before the response is relayed, which is withheld if it would exceed
  the quota.
- 2xx and 3xx responses commit, since a redirect target may serve the content
  without the filter. A route that counts from the response withholds a 3xx.
- Public and deny routes cannot count.

### Deny routes

`action: deny` denies matching requests whatever the preset, with
`<op> is never allowed: <reason>`. Put them before allow routes they must
shadow, for example permission changes under a path an allow route covers
with `**`. The policy summary lists them as `deniedOperations`.

## JSON keys

`jsonKeys: protobuf` makes the engine read JSON the way protobuf JSON parsers
do, for APIs such as Google's:

- Object keys in snake_case are renamed to lowerCamelCase before conditions,
  binds, and resolvers read them, and the names in your JSON paths are renamed
  the same way. Two keys that name the same field reject the body.
- Query conditions and binds on a lowerCamelCase parameter also read its
  snake_case alias.

## Deny responses

Without `deny`, a denial is `text/plain` with the message. With it:

```yaml
deny:
  contentType: "application/json; charset=UTF-8"
  body: '{"error":{"code":{{code}},"message":{{message}},"status":{{status}}}}'
```

| Placeholder | Becomes |
|-------------|---------|
| `{{message}}` | The deny message as a JSON string literal, quotes included |
| `{{status}}` | `PERMISSION_DENIED`, `RESOURCE_EXHAUSTED` for quotas, or `UNAVAILABLE` for token refresh failures, as a JSON string literal |
| `{{code}}` | The HTTP status: 403, or 502 for token refresh failures |

A JSON content type must render valid JSON. Match the API's error format, so
the client surfaces the message to the agent. Messages start with
`filter <name>: `.

## Presets

Presets live in `presets/<name>.yaml` of the filter, and users add their own
in `~/.config/enclave/filters/<filter>/presets/<name>.yaml`
([Write a user preset](../filters.md#write-a-user-preset)).

```yaml
description: Read mail, and send it only to example.com.
extends: readonly
allow:
  mail.read: {}
  mail.send: { recipients: ["@example.com"] }
  files.edit: { resources: { ids: ["1AbC"], within: ["1Folder"] } }
deny: [files.read]
quotas:
  mail.read: 200
  mail.sent: 20
```

| Field | Meaning |
|-------|---------|
| `description` | Shown by `enclave filters show` |
| `extends` | A preset of the same filter to start from |
| `allow` | Capability to `true`, `{}`, or a map of constraint values. `false` is an error |
| `deny` | Capabilities to drop from the inherited set. Not public, not also in `allow` of the same file |
| `quotas` | Counter to a per-session limit, at least 0. 0 blocks every counted request |

- Unknown capability, constraint, or counter names are errors, and public
  capabilities cannot appear. Values must fit the constraint kind.
- Lookup: the user directory first, then the filter's `presets/`. A user
  preset shadows a shipped one of the same name, also as the base of shipped
  presets that extend that name. A user preset that extends its own name
  extends the shipped one. Cycles are errors, and a chain holds at most 8
  presets.
- Merge, from the root of the chain to the selected preset: `deny` removes,
  each `allow` entry replaces the capability's entry as a whole, and `quotas`
  override per counter. Constraints left unset then take their `default`.
- Required scopes: the scopes of the enabled capabilities, plus the scopes of
  resolvers that a `within` value uses. `filters login --preset` requests
  them, minimized through `scopeImplies`.
- File names that are not `<valid name>.yaml` are skipped. In a filter's
  `presets/` they are reported as ignored.

## Limits

| Item | Limit |
|------|-------|
| `spec.yaml`, preset file | 1 MiB |
| Names | 128 characters |
| Path template | 2048 characters |
| JSON path | 512 characters |
| JSON nesting | 256 levels |
| Constraint value | 1024 bytes |
| Deny route `reason` | 256 characters |
| Request body inspection | 10 MiB default, 64 MiB maximum |
| Response inspection | 32 MiB default, 64 MiB maximum |
| Multipart parts | 64 |
| Resolver depth | 32 default, 64 maximum, 128 lookups per value |
| Resolver ID | 512 characters |
| Preset chain | 8 presets |
| Placeholder | 22 characters minimum |
