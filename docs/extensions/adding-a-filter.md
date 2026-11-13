# Adding a filter

Steps to write a request filter (`kind: filter`): data that tells the gateway
which requests to an API it may forward with the user's credential. Field
semantics are in the [filter spec reference](filter-spec.md), the guarantees
the engine gives in the [security concept](../security/request-filters.md).

Core ships no filters. Publish a filter in an extension repository
([Installing extensions](installing.md)). A typical candidate is a filter for
Google Workspace through the `gws` CLI.

## 1. Map the API

- List every host the client calls, including discovery, upload, and helper
  hosts, and the OAuth token host. Each becomes a governed host, together
  with every name below it. Hosts that the agent must not call get no
  routes.
- Check whether the provider accepts other tokens on hosts next to the
  governed ones, such as other APIs under `googleapis.com`. If so, confine
  the parent domain ([Confine](filter-spec.md#confine)).
- Find how the API takes a credential: the header for the injection, and the
  alternatives to block, such as API key headers, `access_token` query
  parameters, and cookies.
- Collect the operations the client uses with method, path, and the
  parameters that change what they do. Google APIs publish discovery
  documents at
  `https://www.googleapis.com/discovery/v1/apis/<api>/<version>/rest`.
- Note defaults that widen an operation, such as a `format` parameter that
  returns full content when absent, or a body field that turns an update into
  a delete.

## 2. Create the directory and spec

```
example/
├── spec.yaml
├── README.md
├── SECURITY.md
└── presets/
    └── readonly.yaml
```

```yaml
schemaVersion: "1"
kind: filter
name: example
displayName: Example API
description: Example API used by the example CLI
defaultPreset: readonly

hosts: [api.example.com, oauth2.example.com]

credential:
  oauth2:
    authUrl: https://accounts.example.com/o/oauth2/auth
    tokenUrl: https://oauth2.example.com/token
    authParams: { access_type: offline }
    scopePrefix: "https://www.example.com/auth/"
    clientHelp: Create a Desktop OAuth client, see the filter README.
  inject: { header: authorization, format: "Bearer %s" }
  env: [EXAMPLE_TOKEN]
  denyHeaders: [x-api-key, cookie]
  denyParams: [access_token, key]
```

Use `static` instead of `oauth2` for a fixed API key
([Static](filter-spec.md#static)). `env` names the variables the client reads
its token from, which receive the placeholder.

## 3. Define capabilities

Group operations by risk, one capability per grade, each with the narrowest
OAuth scopes that cover it. Add constraints where a request names a recipient,
resource, or value the user should be able to limit.

```yaml
resolvers:
  parents:
    url: "https://api.example.com/v1/files/{id}?fields=parents"
    extract: "$.parents[*]"
    recursive: true
    scopes: [files.metadata.readonly]

counters:
  files.read: { description: Files whose content was read, unit: files }

capabilities:
  discovery:
    description: API discovery documents
    public: true
  files.read:
    description: Read file content
    scopes: [files.readonly]
    constraints:
      resources: { kind: resource, var: fileId, resolver: parents, default: "*" }
  files.edit:
    description: Change existing files
    scopes: [files]
    constraints:
      resources: { kind: resource, var: fileId, resolver: parents }
```

A constraint without `default` forces every preset that enables the
capability to set it.

## 4. Write routes

```yaml
routes:
  - op: files.permissions
    action: deny
    reason: sharing changes are never allowed
    method: [GET, POST, PATCH, DELETE]
    host: api.example.com
    path: /v1/files/{fileId}/permissions/**

  - op: files.download
    capability: files.read
    method: GET
    host: api.example.com
    path: /v1/files/{fileId}
    query: { alt: [media] }
    count:
      - { counter: files.read, key: fileId }

  - op: files.trash
    capability: files.edit
    method: PATCH
    host: api.example.com
    path: /v1/files/{fileId}
    json: { "$.trashed": [true] }

  - op: files.update
    capability: files.edit
    method: PATCH
    host: api.example.com
    path: /v1/files/{fileId}
    json: { "$.trashed": [false, ~] }
```

- The first matching route wins. Put deny routes and narrow routes before
  broad ones.
- Pin parameters that widen an operation with `query` conditions, and body
  fields with `json` conditions. Unlisted parameters pass with any value.
- Bind every constraint variable the route can carry. A path capture binds
  itself. Read bodies with [binds](filter-spec.md#binds), for example the
  recipients of a raw message.
- A request that fails a route's conditions falls through to the next route.
  Never follow a conditioned route with a broader one for the same method and
  path.
- On batch endpoints, list the request kinds an allow route accepts with
  [`jsonAllowedKeys`](filter-spec.md#allowed-keys), so kinds the API adds
  later are denied.
- Check mail recipients with `{rfc822: destinations}`, so replies and
  receipts cannot go to addresses the constraint does not allow.
- Set `multipartParts` on upload routes, and `allowFormBody` only where the API
  needs form bodies.
- Set `jsonKeys: protobuf` for APIs that accept snake_case JSON keys, as
  Google's do ([JSON keys](filter-spec.md#json-keys)).
- Never route an endpoint that echoes the credential, such as token
  introspection.

## 5. Add presets and a deny format

```yaml
# presets/readonly.yaml
description: Read files. No writes.
allow:
  files.read: { resources: "*" }
quotas:
  files.read: 200
```

Ship a read-only default preset and a few graded ones. Add a
[deny template](filter-spec.md#deny-responses) in the API's error format, so
the client shows the denial message to the agent.

## 6. Document

- `README.md`: the client setup (OAuth client type, required APIs), every
  preset, and user preset examples for specific resources.
- `SECURITY.md`: what each capability and preset allows, and the risks that
  remain within them.

## 7. Validate and test

```bash
enclave validate-extensions
enclave filters add ./my-filters --name example
enclave filters show example --preset readonly
enclave filters login example --preset readonly --client-secret client.json
enclave --filters example:readonly
enclave network log --verdict deny
```

- Validation errors name the field. Warnings flag ignored files and routes
  that do not bind a constraint variable.
- Run the real client against every preset, and check the denials in the
  network log ([Read denials](../filters.md#read-denials-in-the-network-log)).
- `update` shows the capability diff and warns about changes users must
  review
  ([Install-time review](../security/request-filters.md#install-time-review)).

## Checklist

| Item | Check |
|------|-------|
| Hosts | Every host the client calls is governed. The token host has no routes. Parents that serve other APIs are confined |
| Foreign credentials | `denyHeaders` and `denyParams` cover every alternative credential form of the API |
| Routes | Deny routes come first. Parameters and body fields that widen an operation are pinned. Batch routes allow listed request kinds |
| Constraints | Every constraint variable is bound on every route of its capability, or the route needs `"*"` |
| Scopes | Each capability has the narrowest scopes. `scopeImplies` reflects the provider's scope hierarchy |
| Presets | The default preset is read-only. Quotas cap bulk reads and writes |
| Docs | `README.md` and `SECURITY.md` describe setup, presets, and residual risks |
