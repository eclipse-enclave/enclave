# F5a report: client, hub and option fixes from I2

Branch `a2a-f5a`, based on `a2a` at `133a7e1`.

## Fixes

| Bug | Change | Proving test |
| --- | --- | --- |
| B1 | `a2a_host.go` (`decodeA2AResult`) and `mcp/hub.go` (`decodeMethodResult`) decode `GetTask` and `CancelTask` results as a bare `Task`; other methods keep `a2a.DecodeResult`. New package `internal/a2aagent/mcp/adaptertest` runs the real A10 adapter (real executor, fake harness, minimal control hub) and forwards requests with the hub proxy's headers. The unit-test fakes now answer bare Tasks. | `TestGetAndCancelAgainstRealAdapter` (mcp), `TestA2AHostTaskVerbsAgainstRealAdapter` (app); both fail on the old decoder |
| B6 | `proxy.go`: the stream deadline is `WithTimeoutCause(expiresAt - Config.Now())`, so expiry is measured on the injected clock like every other check. | `TestCallThroughProxyAsHost`, `TestHostCredentialForwardsHostPrincipal` (fail on the old code after 12:15 UTC today, pass now) |
| B8 | `a2a_wiring.go`: the leaver records degraded leaves per group; `reconcileLeftGroups` and `reconcile` return the reconcile's own result. A degraded leave still counts when the reconcile errored or found its group busy. | `TestStopExitReflectsStateAfterReconcile` (resolved, still open, busy, error); `TestStopMembersLeaveBeforeStopOrchestratorFirst` updated |
| B9 | Host `task get|cancel|retry` compare the handle's (or journal entry's) worker generation with the group record before issuing a credential. MCP `get_task`/`cancel_task` compare it with the hub's peer listing, and `send_task` does the same for a `context` handle. The adapter does not check: it answers any task ID it knows, so only the client can refuse. | `TestA2AHostRefusesForeignWorkerGeneration`, extra cases in `TestA2AHostSendRejectsBeforeIssuing`, foreign-generation part of `TestGetAndCancelAgainstRealAdapter` |
| B10 | `proxy.go`: a reload that finds `current` valid and equal to the applied revision clears `rejectedRevision` and `error`, as the supervisor does. The rejection is reported until `current` loads cleanly again (withdrawn or a newer revision applied). | `TestReloadRefusesMixedSnapshotAndKeepsLastRevision` (re-points `current` to the applied revision and expects a clean status) |
| B11 | `a2a_options.go`: every duration option has a minimum, checked for config files, flags (`--a2a-context-ttl`) and `ValidateA2AOptions`. | `TestA2ADurationMinimums` |

Minimums: `a2a_termination_timeout` 1s, `a2a_send_wait` 1s,
`a2a_host_send_wait` 1s, `a2a_commit_timeout` 1s,
`a2a_departure_ack_timeout` 1s, `a2a_reconnect_window` 5s,
`a2a_task_deadline` 10s, `a2a_status_staleness` 10s, `a2a_context_ttl` 1m,
`a2a_record_max_age` 1m, `a2a_credential_lifetime` 2m (the existing
cross-check already forces more than 1m: the host send wait must fit in the
lifetime minus 60s).

## Edits outside the listed files

- `internal/app/testmain_test.go`: `TestMain` calls `fakedriver.Main()` so
  the real-adapter test can re-execute the test binary as the harness.
- `cmd/enclave-a2a-agent/client_test.go` (test only): its fake hub answered
  `GetTask`/`CancelTask` with the wrapper shape B1 removes and had no peer
  listing for the B9 check. Without this `make test` fails. Separate commit
  `052a5b2`.

## Change requests

- CR-F5a-1 (`docs/design/a2a/interfaces.md` section 2): `rejectedRevision`
  reads "last revision refused since `appliedRevision`". Suggest: "last
  revision refused since the last clean load of `current`, else 0; a reload
  that finds `current` valid (again) clears it".
- CR-F5a-2 (`scripts/a2a-smoke.sh`): the quarantine step sets
  `a2a_termination_timeout=1us`, which is now refused. It needs another way
  to reach an unconfirmed termination (a harness that ignores SIGTERM and
  outlives the 1s floor).
- CR-F5a-3 (`docs/configuration.md`, owner of option docs): document the
  minimums above.
- CR-F5a-4 (`internal/app/a2a_cmd.go`): `stop --peer-group` still exits 3
  from `runStopPeerGroup`'s own leave result even when the following
  reconcile resolves everything. B8 is fixed for member stops
  (`stop --name`, which is the orchestrator stop I2 ran); the teardown path
  needs the same rule in `a2a_cmd.go`.

## Gates

- `make build`: pass.
- `make test`: pass, including `internal/a2ahub`.
- `make lint-changed BASE_REF=133a7e1`: pass (vet, golangci-lint 0 issues, gosec).
- `make check-license-headers`: valid.
