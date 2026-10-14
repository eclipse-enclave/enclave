# OAuth broker verification and handoff

`--protect-auth` provides an **experimental opt-in** Claude subscription flow
for named shared, per-project and per-session identities on Docker/Podman, including gateway custody
for the built-in `github-cli` feature. Runtime activation is
implemented; real Docker/Podman mounts and the Claude and GitHub client
compatibility gates have **NOT RUN** in the implementation environment. Do not treat this as a
validated production login mode. Unenrolled identities retain ordinary login
behavior.

## Implemented flow

The gateway captures the bounded token response, commits the grant outside the
workload and returns native-format session handles. Runtime admission, private
gateway mounts, separate workload config view, reset/downgrade, cleanup and
same-mode proxy reload are wired. Protected sessions retain mediation under
restricted policy, `--allow-all-network`, and unrestricted policy files.

The selected grant remains in `tools/claude/auth/<identity>/`. Host-only
`locks/store-<store-path-hash>.identity.json` records protection/reservations.
`oauth-sessions/<incarnation>/view/` is mounted at `~/.enclave-auth`, named by
`CLAUDE_SECURESTORAGE_CONFIG_DIR`; its sibling `runtime/` and the exact host
identity lock are gateway-only. Config and history persist in a protected-only
`<store-key>.protected` config store at `~/.claude`.
Concurrent protected sessions across Docker/Podman share grants and the identity
lock, with independent reservations, handles and native views. Only registered,
attested gateways may share exact grant/lock mounts. Startup leases cover the
interval before containers exist; detached exit/reload does not release an
owner. Unavailable engine inspection fails closed.

Protected sessions run the ordinary, unpinned Claude client with its normal
updates and project/local settings. Saved grants seed a fresh handle file and
minimal provider-derived account/onboarding metadata, which the setup hook
copies into a protected config store that lacks it. Agent memory retains its
normal scope.
No old Claude auth/config file is imported. Provider env credentials, conflicting
native auth sources, credential-helper settings and incompatible runtime modes
are rejected. The built-in GitHub feature has its own adapter; other native-auth
features are rejected. Non-native/env-secret features remain usable. GitLab's
env-secret behavior is unchanged and is not a native GitLab protection claim.

GitHub's existing `features/github-cli/auth/` store is gateway-only when enrolled;
compatible `hosts.yml`/`config.yml` are not copied into the workload. Its separate
`oauth-sessions/<incarnation>/features/github-cli/{view,runtime}/` directories
hold the projected `~/.config/gh` view and private handle registry respectively.
The feature identity is shared across tools/projects and permits concurrent
protected sessions across engines, independently of Claude's named identity. Neither startup
nor finalization uses ordinary feature-file reconciliation. Saved protection
blocks later ordinary feature mounts; transient state is removed only after
verified session end.

Protected-only proxy capabilities preserve the separate proxy UID while allowing
private host-owned storage writes. Atomic writes preserve directory ownership.
SELinux relabels and rootless mappings require real-engine verification. Protected
mode suppresses raw DNS query logging and free-text proxy server diagnostics;
audit events omit request paths and untrusted diagnostic fields. Never enable
unsanitized OAuth wire recording to debug a login.

Reset clears the grant but preserves protection; downgrade requires an idle,
empty identity. Protected import/export fails. Cleanup retains protected/corrupt
identities while removing unrelated state; session teardown removes transient
views only after verifying the owner ended. Existing legacy parallel-session
behavior is retained. Native logout removes the local view, not the durable
grant or copied live handles; neither logout nor host reset proves revocation.

## Current Claude fixture contract

These are the active adapter limits, **not a verified provider response schema
or a supported-client compatibility claim**:

| Area | Candidate behavior |
|------|--------------------|
| Capture | JSON `POST https://platform.claude.com:443/v1/oauth/token`; the default port may be implicit. Only authorization-code and refresh grants with the Claude Code client ID are accepted; an optional integer `expires_in` request field from 1 second to 400 days is forwarded. |
| Body limits | 64 KiB maximum for both encoded and decoded token responses; JSON with identity or gzip encoding. Requests are bounded JSON without content encoding. Unknown/duplicate/case-mismatched keys, unexpected nested structures and null fields are rejected. |
| Exchange deadline | Two minutes from upstream dispatch through token response-body consumption. Token exchanges use a dedicated fresh connection with keep-alives disabled, avoiding stale pooled-connection failures without replaying exchanges. Failure after durable intent leaves an uncertain result. |
| Success fields | `access_token`, `refresh_token`, `token_type`, `expires_in`, `scope`, `account`, `organization`; no other fields. `token_type` is exactly `Bearer`; expiry is an integer from 1 second to 366 days. |
| Account metadata | On enrollment, `account` requires UUID-shaped `uuid` and a validated `email_address`; `organization` requires UUID-shaped `uuid`. Refresh may omit these objects, but supplied values must match the saved identity. |
| Scope metadata | Space-separated values drawn only from `user:profile`, `user:inference`, `user:sessions:claude_code`, `user:mcp_servers` and `org:create_api_key`, matching the subscription authorization scope union. The API-key creation endpoint is not approved. |
| Missing refresh replacement | Retain the saved refresh token. Enrollment requires one. |
| Handle shape | Distinct access/refresh handles use candidate prefixes `sk-ant-oat01-` and `sk-ant-ort01-`, each followed by 256 random bits encoded as hexadecimal. Prefix resemblance is not proof of client compatibility. |
| First-party mediation | Every request to `anthropic.com`, `claude.com`, `claude.ai` or a subdomain is intercepted. Other `platform.claude.com` paths, paths containing `oauth/token`, `oauth2/token`, `api_key` or `api-key`, and paths ending in `/token` are denied, including token revocation. Mediated requests ask for `Accept-Encoding: identity`. |
| Access release | Only `Authorization: Bearer <access-handle>` to `https://api.anthropic.com:443` or `https://mcp-proxy.anthropic.com:443`, any method, path and query outside the denied credential paths. |
| Response tripwire | Released responses fail before the real access or refresh token would reach the workload, in the body or a header. Structured responses (JSON, form, plain text or untyped) outside `/v1/messages`, `/v1/files`, `/v1/complete` and the MCP proxy fail on `"access_token"`, `"refresh_token"` or `sk-ant-`. Encoded responses that need this check fail. Only a trailing partial match is held back, so streamed events are not delayed. Aborts are audited as `oauth-broker-response` denials; during real-client runs, check the network log for them to catch false positives. |
| Host-configured secrets | Placeholders released by declared rules pass the broker; handles beside them are denied. Responses on any host that echo a released secret fail like a released token. `CLAUDE_CODE_OAUTH_TOKEN` is released as `Authorization: Bearer` to `api.anthropic.com` and `mcp-proxy.anthropic.com`. |
| ChatGPT capture | `POST https://auth.openai.com:443/oauth/token` with client `app_EMoamEEZ73f0CkXaXp7hrann`, form or JSON: authorization-code and refresh grants only; the token-exchange grant is denied. Responses allow `access_token`, `refresh_token`, `id_token`, `expires_in`, `token_type`, `scope`; tokens must be JWT-shaped with a ChatGPT account claim, and a refresh may not change the account. Handles keep the JWT header and payload and replace the signature; the refresh handle is opaque. Release: `Authorization: Bearer` at `https://chatgpt.com:443`, including WebSocket handshakes. Native files: Codex `auth.json` (`auth_mode` `chatgpt`, `last_refresh`), OpenCode `auth.json` `openai`, pi `auth.json` `openai-codex`. |
| Mistral sign-in capture | `POST https://console.mistral.ai:443/api/vibe/sign-in/<id>/exchange` with exactly `exchange_token` and `code_verifier`; the success response must be exactly `{"api_key"}`. Errors keep only a numeric `error_code`. The key is non-expiring; a failed exchange can be retried, a second sign-in over a ready grant is refused. Release: `Authorization: Bearer` at `api.mistral.ai`, `console.mistral.ai` and `chat.mistral.ai`, including WebSocket handshakes. Native file: Vibe `.env` with `MISTRAL_API_KEY`. |
| Copilot capture | OpenCode's device login at `https://github.com:443/login/device/code` and `/login/oauth/access_token` with client `Ov23li8tweQw6odWQebz`, JSON or form, validated like the GitHub CLI adapter's; other clients' logins pass to that adapter or are refused. The non-expiring token is kept; a second login over a ready grant is refused. Release: `Authorization: Bearer` at HTTPS `githubcopilot.com` hosts only. Native file: OpenCode `auth.json` `github-copilot` with the handle as `access` and `refresh`. |
| Refresh release | Only the JSON `refresh_token` field at the exact token endpoint. Session/identity selection comes from trusted configuration, not the request. |

Grant commits update token values, expiry and generation together. Session
handles remain stable through refresh and proxy reload; a new runtime directory
and incarnation produce new handles. A lost response can be served from the
usable committed generation with remaining lifetime recomputed. Within five
minutes of expiry, retry projection lasts for the first 30 seconds after a
successful token commit, or half the issued lifetime if shorter, and never
extends beyond expiry. After that grace period, every generation can refresh
early; grants farther from expiry remain usable without rotation. Commit time
is persisted with subsecond precision. These timing choices still require
real-client tests.

The native view projects access/refresh handles, expiry and scopes, with
`subscriptionType` and `rateLimitTier` explicitly null (the inspected loaders
accept null and fetch profile metadata). Saved ready grants also project minimal
account/onboarding state. Workload metadata is never imported into the grant.

## Local validation evidence

Run these local checks after changing either adapter. Record their result and
checkout revision separately from engine evidence:

```bash
make build
env -u NO_COLOR -u ENCLAVE_COLOR make test
make lint
go test -race ./internal/gateway/... ./internal/backend/hoststore ./internal/backend/docker ./internal/backend/qemu ./internal/runtime ./internal/app
git diff --check
```

The full lint target includes license headers, vet, golangci-lint, gosec,
Windows compilation and shellcheck.

The environment supplied `NO_COLOR=1`, which suppresses terminal-tint output in
pre-existing tests that mock a terminal without clearing that variable. The
test command removes that inherited suppression; it does not skip tests or
change application behavior. None of these commands exercises real container
mounts, SELinux, Claude login or GitHub device login. The Docker/Podman experiment and human
compatibility gates remain **NOT RUN**.

## Claude client compatibility: static evidence

Protected sessions do not pin Claude: it releases too often for Enclave to
follow. Custody instead rests on first-party mediation, the captured exchange
and the response tripwire above. A client that changes its login flow fails
closed. These static observations guide the real-client gate; they are **not a
verified server schema**. Packages were downloaded and inspected without
executing them:

| Item | 2.1.63 (npm `cli.js`) | 2.1.300 (native `linux-x64`, 2026-10-14) |
|------|-----------------------|-------------------------------------------|
| Token exchange | JSON `POST https://platform.claude.com/v1/oauth/token`, client ID `9d1c250a-e61b-44d9-88ed-5944d1962f5e` | Unchanged |
| Token request fields | `grant_type`, `code`, `redirect_uri`, `client_id`, `code_verifier`, `state`; refresh adds `refresh_token`, `scope`; optional `expires_in` on login | Same, with optional `expires_in` on refresh too |
| Logout | Local only | Also `POST <token URL>/revoke` with the refresh token; denied, and the client continues with local logout |
| Credential file | `(CLAUDE_CONFIG_DIR or ~/.claude)/.credentials.json`, `claudeAiOauth` object | `<CLAUDE_SECURESTORAGE_CONFIG_DIR>/.credentials.json`, same object |
| Bearer API calls | `/v1/messages`, `/api/oauth/profile`, `/api/oauth/claude_cli/roles` and a few more | Many more `/api/oauth/*` paths (usage, organizations, files, validate) on `api.anthropic.com`, plus `mcp-proxy.anthropic.com` for connectors |
| Credential minting | `/api/oauth/claude_cli/create_api_key` | Unchanged; denied |
| Host credential variables | — | `CLAUDE_CODE_HOST_CREDS_FILE`, `CLAUDE_CODE_HOST_AUTH_ENV_VAR`, `CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST`; rejected in protected sessions |

Repeat this comparison when Claude changes its login or storage, and record
`claude --version` in every engine and human run. Static searches cannot prove
token-shape acceptance, absence of JWT/ID-token processing, TLS compatibility
or logout semantics.

## GitHub client pin and required coverage

Protected feature startup selects **GitHub CLI 2.87.3** from
`/opt/enclave/gh-2.87.3/bin/gh`, alongside the ordinary apt installation. The
[feature installer](../../extensions/features/github-cli/install.sh) checks
the official Linux release archives against these SHA-256 values:

| Architecture | SHA-256 |
|--------------|---------|
| amd64 | `c6e5537631fca45f277ef405ce8751d139b491e9402cc20891a003525a8773b2` |
| arm64 | `5f5d89563bf26751e2173b37e594065504e85b6b781c1f1832d24bf2c2b4554f` |

The runtime request checks this binary independently of image-provided hooks;
`--no-rebuild` with an old image missing the pin fails at startup. Include that
case when testing ordinary agent and shell starts. `enclave exec` derives the
same selection from the target session's creation environment; verify that an
exec shell and direct `gh` resolve to the pinned GitHub CLI.

Offline validation passed with the official **2.87.3 Linux amd64** archive after
verifying the SHA-256 above. In an isolated config directory containing a
synthetic two-account `hosts.yml`, `gh auth token`, `gh auth switch` and
`gh auth git-credential get` successfully loaded the native handles, switched
accounts and returned the selected handle in Git's credential format. These
checks used no network or real credentials and did not exercise device login.

Version checks, source inspection and synthetic fixtures are not evidence that
the current GitHub service accepts the complete flow. The GitHub engine and
human gates are **NOT RUN**. Changing this pin or its adapter requires repeating
both gates, including restricted and both unrestricted network selectors.

Existing compatible GitHub.com native files in Enclave's shared feature store
are an input to the gateway, not a host `~/.config/gh` import command. New login
uses the device flow; PAT paste is unsupported. Enterprise, keyring-only
credentials and unsupported native/token schemas must fail closed. Do not
expand the adapter to generic hosts or raw credential forwarding to make a
test pass.

The [GitHub adapter](../../internal/gateway/oauthbroker/github.go) and its
[native-file parser](../../internal/gateway/oauthbroker/github_native.go)
currently enforce this fixture contract, not a verified live-service schema:

| Area | Implemented limit |
|------|-------------------|
| Native input | GitHub.com only; bounded private `hosts.yml`/`config.yml`. Up to 16 named accounts with plaintext token fields; keyring-only accounts have no fallback. Only the parser's declared native fields and preference keys are accepted. |
| Client | `gh` 2.87.3, OAuth client ID `178c6fc778ccc68e1d6a`; the workload cannot choose a different adapter identity or provider host. |
| Device exchange | Form-encoded `POST https://github.com:443/login/device/code`, then `POST /login/oauth/access_token` with the device grant. JSON token responses are parsed and projected, never passed through raw. |
| Token success | Only `access_token`, `token_type` and `scope`; `token_type` is exactly `bearer`. This adapter supports non-expiring tokens, not `offline_access`, refresh tokens or expiry-bearing response variants. |
| Identity | After capture the gateway obtains the account login through an authenticated `GET https://api.github.com/user` before committing the account. Imported native account names remain host-provided input. |
| API release | Session handles in the accepted Authorization schemes at `https://api.github.com:443`; GET, HEAD, POST, PUT, PATCH and DELETE, including REST and GraphQL. This is not a repository- or operation-level allowlist. |
| HTTPS Git release | A handle in the Basic-auth password field at `https://github.com:443/<owner>/<repo>/` or `<repo>.git/`: GET `info/refs` with the exact upload-pack or receive-pack service query, or POST `git-upload-pack`/`git-receive-pack` without a query. SSH and other Git hosts are outside this adapter. |
| Native view | Fresh `gho_enclave_` handles per account/session. Proxy-process reload keeps them; native account/config edits never overwrite the gateway grant. |
| Device replay/recovery | A committed exchange can be reprojected only in its original session/incarnation, including proxy reload. Prior-session completed codes are rejected; uncertain attempts cannot be blindly redispatched. Records retire two hours after their first poll, beyond the one-hour maximum lifetime of accepted device codes; a retired code is redispatched and GitHub rejects it. At most 64 unretired completed/uncertain records exist at once; validated pending/slow-down polls do not consume that capacity. |

Unknown/duplicate response fields, unsupported encodings and malformed token
errors fail closed. Ordinary API/Git responses still stream without masking;
the approved provider remains trusted not to reflect its credential.

## Acceptance owners and order

| Stage | Owner | Required result | Current evidence |
|-------|-------|-----------------|------------------|
| Local implementation | Development agent without an engine | Runtime/CLI integration, unit/fixture checks, precise adapter schema | See implementation test output; not engine evidence |
| Container experiment | Agent with Docker and Podman access | Synthetic provider and mounted-view harness; inspect real mounts, ownership, lifecycle and egress | Not run in the implementation environment |
| Real-client gate | Human, after the container experiment passes | Browser consent and recorded pinned-client lifecycle on each backend | Not run; no supported client version |
| Production support | Maintainer reviewing both evidence sets | Implemented opt-in and unrestricted mediation verified on each supported matrix row | Pending the preceding gates |

The engine-capable agent can prepare and test the experiment harness without a
real account. It must not mark the human gate passed or enable a supported flow
based on synthetic exchanges. A human must complete browser consent; no real
provider tokens, authorization codes, PKCE verifiers or redeemable handles belong
in committed fixtures, screenshots or logs.

## 1. Engine-capable agent handoff

Use disposable host data and a fresh identity on each backend. Run Docker
rootful and Podman rootless; include a Podman host with SELinux enforcing.
Record any unavailable matrix row as **not run**, not a pass. Host data roots are
platform-resolved; Linux examples use `$XDG_STATE_HOME/enclave/` and do not
establish macOS coverage.

Before the harness run:

```bash
make build
make test
make lint
make check-license-headers
docker version
podman version
```

Run each engine command only on a host with that engine. Record the exact
checkout revision, dirty diff, Go version, OS, engine version, rootless status,
SELinux status and image digests. Use the in-tree `bin/enclave` and checkout
assets; do not accidentally exercise an installed release.

The harness must use the actual gateway entrypoint, proxy, firewall and workload
network namespace. A host-only `httptest` server is useful fixture coverage but
cannot satisfy container isolation, UID mapping, reload or mount checks. The real runtime now activates the fixed adapter via `--protect-auth`; synthetic
provider fault injection still requires a test-only upstream transport/fixture
harness. Do not add a production endpoint override or generic credential-release
route to facilitate testing. The existing Go broker/proxy fixtures supply the
synthetic schemas and failure cases; run the same cases through real namespaces
and binds in the engine report.

### Scripted engine checks

`scripts/protected-auth-engine-check.sh` covers the matrix rows that need no
provider account, against disposable XDG roots and a disposable project:

```bash
make build
scripts/protected-auth-engine-check.sh --engine docker --tool all --report docker.md
scripts/protected-auth-engine-check.sh --engine podman --tool all --report podman.md
```

`--tool` takes a built-in tool name or `all` (default `claude`); trailing
arguments select checks, such as `handles-only reload`. Per tool, it enrolls a
fresh identity, then seeds a synthetic grant for each of the tool's login
adapters (accepted by the gateway, useless to a provider). Go tests keep the
grants and the script's copy of the tool table in step with the gateway. It
checks protection, the handles in the tool's native credential file, release
refusals, workload and gateway mounts, ownership, exec selection, proxy reload,
concurrent sessions, cleanup, continuity, project and ephemeral scopes, reset
and downgrade. Theia and Theia Next have no login adapter and run only the
checks that need no named identity. Each check passes only on positive
evidence; the report records checkout, engine, rootless and SELinux state. Run
it on rootful Docker, rootless Podman and an SELinux enforcing host. It does not
replace the remaining rows or the human gate.

### Required experiment matrix

| Case | Assertion and evidence |
|------|------------------------|
| Gateway compatibility | With a current runtime but a legacy gateway image, protected `--no-rebuild` refuses startup and removes the sidecar before launching the workload. Generic gateway readiness, stale readiness files, mismatched sessions and missing requested GitHub adapters must not pass the private broker attestation gate. Schema-1 single-owner brokers cannot join concurrent identities; stop and rebuild older protected gateways. An ordinary gateway retains its existing readiness behavior. |
| Fresh capture | CA trust and capture registration exist before login. Only access/refresh handles appear in every workload-visible response and auth file. Trusted grant is committed before the response is delivered. |
| Scoped release | Provider receives the real token only at the exact HTTPS origin, port, method, path and declared field. Wrong endpoint, field, identity and session fail without releasing it. Test alternate Host/SNI, encoded paths, query strings and redirects. |
| Bounded token responses | Truncation, duplicate JSON keys, unknown top-level/nested fields, oversized encoded/decoded bodies and unsupported encodings fail closed. Synthetic credentials in token-exchange errors, headers and trailers are not reflected. |
| Streaming | An SSE event reaches the workload before upstream EOF. Workload cancellation reaches upstream. A truncated stream is not reported as a successful complete response. |
| Rotating refresh | Concurrent requests from separate sessions/gateways send the consumed refresh token upstream at most once. Each waiter receives its own stable handle projection of the committed grant. Exercise replacement and omission of the refresh token. |
| Lost success response | Drop the reply after commit, then retry before and after proxy reload. The retry uses the committed grant, never the consumed token; returned lifetime is recomputed. |
| Uncertain refresh | Interrupt dispatch, receipt and persistence before a durable result. Every live sibling and restarted broker sees an explicit uncertain/reauthentication state, never a silent retry of the old refresh token. |
| Commit boundaries | Inject failures before write, file sync, rename and directory sync. Test initial grant/registry publication and refresh separately. No uncommitted grant is acknowledged. Live sibling readers fence visible commits after a writer loses its directory-sync acknowledgement; recovery must not require restarting all siblings. |
| Live reload | Apply the gateway bundle with the workload alive. The proxy PID changes but gateway/network namespace and handles remain valid. Do not substitute gateway replacement for process reload. |
| Proxy death | Kill the proxy unexpectedly and demonstrate fail-closed egress. End the affected logical session; start a new one from durable state. |
| Session reuse | Stop, start again with the same name, and verify fresh handles. Prior-session handles fail even when identity and container name are reused. `--continue` and `--resume` find the previous protected conversation; the `.protected` config store holds no real credential, and the ordinary config store is not mounted. |
| Admission | Simultaneous starts across projects and engines admit multiple protected sessions for the same identity with distinct handles/views. Legacy access still conflicts. Detached CLI exit and proxy reload do not release reservations. Stopping one session or reconciling its stale reservation must preserve sibling grants, handles, views and reservations. Reset, startup reset and downgrade refuse until all selected-identity sessions have ended. Stalled engine inspection must not block sibling credential access. Vanished-container races retry within a bound; unavailable engines and unresolved bind sources still fail closed. |
| Persistent protection | Omitting opt-in cannot reopen an identity raw. Reset preserves protection. An explicit host-side downgrade is allowed only without an owner; no implicit downgrade on an error. |
| Scopes | `--auth-scope project` enrolls and reuses `project.<hash>` across sessions of one project only; `enclave auth reset/downgrade --auth-scope project` act on it. `--ephemeral` enrolls a fresh `ephemeral.<random>` identity whose grant, metadata and lock disappear after the session ends and its containers are removed; kill the CLI mid-session and verify the next session start sweeps them. GitHub custody stays on the global feature identity in every scope. |
| Mount isolation | Inspect workload, gateway and every reconcile helper. The real store and its parents are absent from workload/helper mounts, including symlink aliases and config overrides. Attempt added-dir mounts from later ordinary sessions too, including concurrent enrollment/start. Preexisting workloads exposing selected grant or trusted control files prevent enrollment. Cover QEMU directory/file staging and copy-back; active/preparing/finalizing QEMU sessions, including older guests without leases, prevent protected admission/reset/downgrade. Kill the parent CLI while QEMU stays alive and verify the inherited lease remains effective. Only the selected identity is mounted into the gateway. |
| Ownership | Created/replaced grants remain owned by the invoking host user with restrictive modes after capture, refresh and restart. Verify rootless UID mapping and SELinux relabels. Workload UID never gains the proxy firewall exemption. |
| Workload tampering | Overwrite view/config files, expiry and metadata; replace auth symlinks. None changes the trusted grant. Neither inspected-mount nor foreground request-driven finalization can copy these files into it. |
| Lifecycle | Exercise foreground exit, `exec` exit, detached stop/remove and cleanup. Remove only the selected session's views/mappings; ordinary exit preserves the grant. |
| Reset/import/export | Active ownership prevents conflicting mutations. Selected-identity reset invalidates handles and clears its grant/copies without touching another identity. Unsupported import/export fails before exposing state. |
| Known bypasses | Reject conflicting raw credentials, native auth sources, unsupported scopes/backends and incompatible mounts before launch. Do not fall back to legacy injection. |
| Regression | Env-secret substitution is restricted to each rule's declared header, matched case-insensitively; known placeholders in other headers deny the whole request, even at an approved host. Verify undeclared debug/correlation headers are denied before dispatch to a reflecting endpoint. Legacy auth and unprotected parallel-session behavior retain regression coverage. |

### GitHub feature matrix

Run the shared admission, mount, ownership, reload, session-reuse and negative
release cases for GitHub too. Add these feature-specific checks:

| Case | Assertion and evidence |
|------|------------------------|
| Existing native store | A compatible GitHub.com feature store enrolls only while idle. Workload mounts contain projected `hosts.yml`/`config.yml`, not the originals or their parents. Unsupported hosts, credential sources and schema variants fail before raw credentials are exposed. |
| Device login | Run the pinned `gh` GitHub.com browser/device flow from an empty store. Token polling returns native-format handles only after durable capture. Exercise pending/denied/expired authorization, malformed and oversized token responses, uncertain outcomes and unsupported expiry/refresh variants. Verify same-session lost-reply recovery, rejection of prior-session device-code replay, failure at the bounded history limit, and successful login after older records retire. No token-response passthrough on error. |
| API and Git release | Exercise `gh` REST/GraphQL and HTTPS Git against a disposable repository, with remotes both with and without the `.git` suffix. Verify each accepted authorization format and Git credential field at its declared endpoint. Wrong hosts, ports, paths, fields, account handles and sessions fail without release. |
| Account/config view | Verify every supported imported account and account-switch behavior, plus preserved supported preferences. Unsupported native structures do not get copied verbatim into the workload. Rewriting the native view cannot update the trusted grant or widen release. |
| Shared feature identity | Concurrent sessions with the same or different Claude auth names share the global GitHub grant with separate handles/views. Concurrent device logins preserve both account updates; device-code replay cannot cross sessions. Replacing an account token is visible to sibling gateways without replacing their handles or local account selection. A live legacy feature bind prevents enrollment; saved protection prevents later ordinary sessions from exposing it. Failed multi-identity admission does not strand another identity. |
| Lifecycle/reset | Foreground, `exec`, detached stop/remove and cleanup never reconcile projected feature files into real credentials. Feature reset clears trusted credentials without reimporting them on the next launch. Feature downgrade requires idle, empty state; neither command mutates the Claude identity. |

Do not record device codes, access tokens, authorization headers, Git credential
helper output or live handles. The device flow still exposes bootstrap material
to the client and does not defend enrollment against an already-malicious agent.

Ordinary provider responses are streamed without credential masking. Restricting
release to a declared auth header does not prove that a provider will never
reflect that header's value; approved providers remain trusted with credentials.
The bounded token-response adapter does not provide general response filtering.

For mount inspection and synthetic-secret searches, emit assertions and paths,
not file contents. Log neither real tokens nor live handles. Ordinary HTTP
audit logs omit query strings but are not a safe OAuth wire recorder: bootstrap
values and tokens may also occur in bodies, errors and headers. Create sanitized
schema records before writing artifacts.

### Independent unrestricted-network prerequisite

Destination policy and credential mediation are now separate (`RequireGateway`
and `UsesGateway`). Ordinary unrestricted runs still bypass the gateway; protected
runs retain DNS/proxy interception and unrestricted IPv4 destination access.
IPv6 remains disabled, matching the gateway design. A true gateway bypass is
incompatible with protected auth. Live reload preserves the active mode and
its CLI-override/policy source; mode changes require restarting the session.

Repeat capture, streamed use, refresh, reload,
new-session and negative-release checks for both unrestricted selectors. An
unrelated controlled destination must resolve and be reachable without an
allowlist entry across DNS/firewall/proxy checks, but must never receive a real
protected credential. Verify selective credential MITM remains active. Passing
only restricted mode is not completion of the parent feature.

## 2. Human real-client gate

After the engine agent provides the experiment report, use a disposable fresh
Claude subscription identity and record the client version. Do not migrate an
existing login. Refuse raw credentials in selected native config/auth files,
provider env credentials and `apiKeyHelper` or alternative-provider settings.
Record the effective config and mounts without credential values.

### Human smoke commands

Use a disposable project, host data roots and account. On Linux this avoids
existing native config files and provider secrets. Keep the shell variables for
the whole test; do not delete these roots until sessions have been stopped.

```bash
make build
export XDG_CONFIG_HOME="$(mktemp -d)"
export XDG_STATE_HOME="$(mktemp -d)"
export XDG_CACHE_HOME="$(mktemp -d)"
# Unset host ANTHROPIC_* / CLAUDE_CODE_OAUTH_* credentials first.
unset GH_TOKEN GITHUB_TOKEN GH_ENTERPRISE_TOKEN GITHUB_ENTERPRISE_TOKEN GH_HOST GH_CONFIG_DIR
./bin/enclave --backend docker --tool claude --protect-auth \
  --auth-scope shared --auth-name broker-docker --name broker-review
```

Inside Claude, choose subscription login, then ask for a streamed response.
From a second terminal with the same XDG values, run
`./bin/enclave network apply --tool claude`; repeat the prompt without replacing
the gateway. Exit, rerun the launch and confirm saved-login reuse. Compare handle
hashes privately to confirm new handles; never print live credentials/handles.
Record the output of `claude --version` inside the container.

The default built-in GitHub feature now remains enabled. Inside the same
container, confirm `gh --version` starts with `gh version 2.87.3`, then run:

```bash
gh auth login --hostname github.com --git-protocol https --web --insecure-storage
gh api user --jq .login
```

Here `--insecure-storage` selects native-file storage for gateway handles, not
real tokens. Complete browser consent yourself. Check a disposable repository with `gh` and
HTTPS Git, reload the proxy and repeat, then verify saved login and fresh handles
in a new session. Do not use `gh auth token` or print credential files as a test.
Record assertions about handles and mounts without their values. PAT paste and
Enterprise login are not substitutes for this device-flow gate.

Repeat using `--backend podman --auth-name broker-podman` on rootless Podman,
including SELinux enforcing where available. Repeat each backend with
`--allow-all-network`, then with that flag omitted and
`enclave network set-mode unrestricted --global --tool claude --no-apply`. Confirm an
unrelated controlled host is reachable without a domain rule and no protected
credential reaches it. Do not mistake a successful unrelated request for proof
that credential capture/refresh still works; repeat the authenticated lifecycle.

After first login, launch a second protected session with the same `--auth-name`
and a different `--name`; both Claude and GitHub must work concurrently. Repeat
across projects and engines, verify distinct handles and cross-session handle
rejection, then stop one session and confirm the sibling still works. Reset,
`--reset-auth` and downgrade must fail while any selected-identity session remains.
After stopping all of them, verify omission of `--protect-auth` fails, then run:

```bash
./bin/enclave auth reset --backend docker --tool claude --auth-name broker-docker
# Omission must still fail: reset did not remove protection.
./bin/enclave auth downgrade --backend docker --tool claude --auth-name broker-docker
```

The feature has a separate global identity. After all sessions using it have stopped:

```bash
./bin/enclave auth reset --backend docker --feature github-cli
# Protection still blocks an ordinary feature-auth mount.
./bin/enclave auth downgrade --backend docker --feature github-cli
```

Do not combine `--feature` with `--tool`, `--auth-name` or `--auth-scope`. Check
that resetting/downgrading GitHub leaves the Claude identity untouched, and vice
versa. Native logout is not a replacement for either host reset.

Use the matching `--backend` and identity for Podman. Every installed engine
must be inspectable for admission; unavailable daemons cause a refusal. Downgrade
must fail before reset if a grant remains. Test native logout separately from
host reset. The smoke commands do not replace the engine failure matrix or a
real refresh; complete the following checks and retain a sanitized report.

For every supported backend:

1. Complete normal browser login. Observe the exact token endpoint and native
   file locations. Verify that only synthetic access/refresh handles reach the
   workload, and that trusted metadata has a provider-controlled source.
2. Send a prompt requiring an authenticated streamed response. Confirm early
   streaming and record required secondary API endpoints/fields. Check token
   prefix/length validation, JWT/ID-token processing, certificate trust/pinning
   and whether the client rejects or transforms a handle.
3. Exercise a real provider refresh after expiry or through a verified client
   mechanism. Editing the workload's expiry alone does not prove real refresh:
   the broker trusts its committed expiry. Confirm at the trusted boundary that
   a real refresh occurred, including replacement/omission behavior.
4. Reload the proxy with the workload alive, repeat authenticated use, then end
   the session. Start another session using the saved identity, without another
   browser login, and verify new handles and rejection of the old ones.
5. Exercise native logout and record what it actually does. Deleting a view is
   local sign-out, not proof of revocation. Verify the behavior of a copied
   handle separately; only a declared upstream revocation path can establish
   provider revocation. Exercise trusted reset independently.
6. Repeat the supported lifecycle under
   both unrestricted selectors and verify an unrelated destination works while
   credential release remains scoped.

If any required exchange needs additional destinations, fields, headers or
metadata, stop and revise the explicit adapter contract and synthetic fixtures.
An unexpected server schema must fail closed. Never solve a compatibility failure by passing through raw tokens.

## Evidence record template

Copy this into the experiment report and fill it without credentials. Attach
synthetic reproductions, not real wire dumps. Keep the test harness and exact
invocations reviewable with the report.

```text
Status: NOT RUN | PASS | FAIL | BLOCKED (with reason)
Enclave revision / dirty diff:
Tester / date:
Stage: engine synthetic | human real client
Host OS / architecture / UID-GID / SELinux:
Engine / version / rootless status:
Runtime and gateway image digests:
Claude version / distribution / artifact digest:
GitHub CLI version / architecture / artifact digest:
Automatic updates prevented and version rechecked:
Auth scope / disposable identity label / network selector:
Harness revision / exact invocations:

Exchange name: authorization | device | refresh | profile | model | API | Git | logout | other
Trusted origin (scheme, host, port), method, exact path:
Request content type/encoding and allowed field names/types:
Response status, content type/encoding, allowed headers/trailers:
Response field names/types/requiredness, including every nested object:
Provider source of issuer/account/scope/expiry metadata:
Native auth path, field schema and expiry units:
Handle-shape constraints / JWT or ID-token processing:
Secondary exchanges and native logout/revocation observations:

Each required matrix row: PASS | FAIL | NOT RUN + artifact reference
Confirmed absence of credentials/bootstrap material/handles in artifacts:
Outstanding blockers and unsupported combinations:
Engine agent handoff sign-off:
Human compatibility sign-off (separate):
```

Only promote a version/backend/network-mode combination to supported after both
its engine evidence and human gate pass. List omissions explicitly; no engine
access in the development environment is a deferred check, not an exemption.
