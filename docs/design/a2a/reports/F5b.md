# F5b report: lifecycle, reconcile and signal fixes from I2

Branch `a2a-f5b`, based on `a2a` at `133a7e1`, with the `a2a` tip
`9474c47` (F5a) merged in for CR-F5a-4.

## Fixes

| Bug | Change | Proving test |
| --- | --- | --- |
| B2 | `group.json` gets an optional `engine` (`name` docker or podman, `root` the engine's storage root from its info; `schemaVersion` stays "1"). Join writes it in the record's first write (`GroupStore.CreateOn`). `claimEngine` runs under the group lock in `Reconcile`, `Join`, `Leave`, `IssueHostCredential` and `RevokeHostCredential`: another engine or daemon is refused (`ForeignEngineError`), and reconcile reports the group `skipped` and touches nothing. A record without `engine` adopts this engine when its hub or a member session of its generations exists here; otherwise it is never torn down, only reported (`UnknownEngineError`). `a2a send/task` and `a2a reconcile --peer-group` switch to the recorded engine, or refuse when `--backend` names another one; `stop --peer-group` refuses (its sessions are stopped through the caller's backend). The "hub is not running" hint now reads `enclave a2a reconcile --peer-group <g> --backend <engine>`. | `TestReconcileSkipsGroupOfAnotherEngine` (other engine, other daemon), `TestLifecycleRefusesGroupOfAnotherEngine`, `TestReconcileOfRecordWithoutEngine`, `TestHostCredentialHintNamesTheEngine`, `TestJoinRecordsEngine`, `TestGroupStoreRecordsEngine`, `TestEngineSameDaemon`, `TestA2AEngineForFollowsTheRecord`, `TestRunA2AHostVerbUsesTheGroupEngine`, `TestRunStopPeerGroupRefusesAnotherEngine`, `TestReportA2AReconcileSkippedGroup`; engine assertion in `TestEngineA2AJoinLeaveTeardown` |
| B3 | `Reconcile` reads each running active worker's `restrictions.json` (header must match group and worker generations) and runs the release path for a quarantine that has no `quarantine-release` obligation. A surviving descendant keeps the quarantine and records the obligation. A stopped worker is not asked. | `TestReconcileReleasesQuarantineWithoutObligation`, `TestReconcileLeavesQuarantineOfStoppedWorker` |
| B4 | An orchestrator whose container ended (`exited`, `dead`, `stopped`) is removed with an authoritative departure; `created` and `paused` are not departures. Workers: a stopped worker stays a member and keeps the hub, per proposal 15 ("A stopped background worker keeps its network and therefore keeps the hub alive") and proposal 14 ("Worker session stopped": delivery error, the table is what reconcile reads). | `TestReconcileTreatsExitedOrchestratorAsDeparture`, `TestReconcileLeavesQuarantineOfStoppedWorker` |
| B5 | `RunWithIOAndStartHook` relays SIGINT, SIGTERM and SIGHUP to the engine child as the TTY path does. A terminal Ctrl-C now reaches the engine twice (process group plus relay); the adapter ignores the second one because its context is already cancelled. The auth sync helper uses the same function; its `sh` is PID 1 without handlers, so a relayed signal does not end it. | `TestRunWithIOForwardsProcessSignalsToChild` (TERM, INT, HUP with a stub CLI; TERM hangs 10 s without the fix) |
| CR-F5a-4 | `runStopPeerGroup` reconciles the group after the stop and exits 3 only when that reconcile errored, left a problem, or found the group busy after a degraded leave. | `TestRunStopPeerGroupExitReflectsStateAfterReconcile`; `TestRunStopPeerGroupExitCodes` updated |

## Join race

I found a real race, though not the one I2 logged. The foreground path calls
`Join.Started()` before the engine creates the session container
(`docker.go` `Run`), so the group lock was released while the member was
`active` and its container did not exist. A reconcile in that window removed
the member with "session container is gone". `Started()` now keeps the lock
until a container with the member generation label exists (polled every
100 ms, at most 30 s); `LeaveOnExit` ends the hold first. The background path
already calls `Started()` after the container exists and releases at once.
Proved by `TestStartedHoldsGroupLockUntilSessionContainerExists` and
`TestLeaveOnExitEndsStartedLockHold`.

The I2 log ("join did not finish", so state `joining` with the lock free)
remains unexplained. Every save of a `joining` state happens under the lock,
activation is saved before `Started()`, and rollback runs before the unlock.
The lock is a flock on a file outside the group directory that nothing
deletes. The adapter log in I2 shows the container started, which needs
activation first. My best guess is that the wrapper-shell kill of the old
script version raced with something not in the current code. Not reproduced
in the smoke runs below.

## Edits outside the listed files

- `internal/backend/docker/a2a_secrets.go`: `secretsOwner` became
  `secretsOwnerFromInfo` so `PeerGroups` reads the engine info once for both
  the secrets owner and the engine root.
- `internal/backend/docker/a2ahub_engine_test.go`,
  `internal/backend/docker/a2a_lifecycle_test.go`: test updates (fake engine
  mutex, fixture engine, record engine assertion).
- `internal/app/a2a_engine_test.go` (new) and `internal/app/a2a_cmd_test.go`.
- Switching the CLI wrapper from `internal/app` goes through
  `backenddocker.UseEngine`, because `internal/arch` forbids importing
  `internal/docker` from `a2a_cmd.go`.

## Notes and limits

- `root` cannot tell two remote daemons with the same data root apart
  (`DOCKER_HOST` or contexts). The Docker daemon ID would, but `SystemInfo`
  lives in `internal/docker/types.go`, outside this package's files.
- The engine name comes from the CLI wrapper (`IsPodman`), not the backend
  name, so the podman-docker shim records `podman`.
- `logA2AReconcile` in `a2a_wiring.go` (not owned) does not print `Skipped`;
  start, stop and cleanup reconciles skip a foreign group silently. Only
  `enclave a2a reconcile` prints it.
- An engine-less record with no hub or member anywhere (for example an empty
  record left by a crash before this change) stays until removed by hand.

## Gates

- `make build`, `make test` (all packages, after the merge), `make
  lint-changed`, `golangci-lint run --build-tags docker` on
  `internal/{backend/docker,docker,peergroup,app}`, `make
  check-license-headers`: pass.
- `go test -tags docker ./internal/backend/docker ./internal/docker -run Engine`:
  pass on Docker; pass on Podman (`ENCLAVE_TEST_ENGINE=podman`). One Podman
  run failed once in `TestEngineA2AHostCredentialAndHubCall` (the `/fail`
  call's error lacked the hub's stderr). It passed on 3 reruns, the code is
  untouched here, and it looks like a stderr race in `podman exec`.

## Smoke

`scripts/a2a-smoke.sh kill9_reconcile foreground_signal` with
`A2A_SMOKE_REAL=0` on both engines: every check passes except "quarantine-prone
worker got the 1us termination timeout", which is CR-F5a-2 (F5a's minimum
refuses 1us). Foreground SIGTERM to the enclave pid: the session ended
3.0 s after the signal on Docker and Podman and the worker left the group
(I2: no exit within 300 s).

Manual B2/B4 run on a Podman group `a2a-f5b-m` (worker and orchestrator),
driven with the Docker backend:

```text
$ enclave a2a reconcile --backend docker
a2a-f5b-m: skipped: peer group a2a-f5b-m runs on podman, not docker; use --backend podman   (exit 0)
$ enclave a2a reconcile --backend docker --peer-group a2a-f5b-m
error: peer group a2a-f5b-m runs on podman, not docker; pass --backend podman                (exit 1)
$ enclave a2a reconcile --peer-group a2a-f5b-m           # configured backend docker
info: Peer group a2a-f5b-m runs on podman; using podman.
a2a-f5b-m: nothing to do
$ enclave a2a send --backend docker --peer-group a2a-f5b-m --json a2a-f5b-mw1 hi   -> refused, exit 1
$ enclave stop --backend docker --peer-group a2a-f5b-m                              -> refused, exit 1
$ enclave run --backend docker ... --peer-group a2a-f5b-m --name a2a-f5b-mw2 ...    -> join refused, no Docker container left
$ podman kill <orchestrator>; enclave a2a reconcile --peer-group a2a-f5b-m
a2a-f5b-m: removed member a2a-f5b-mo: session container has exited   (departure authoritative, complete)
```

The group and both members kept running through every Docker command.

## Cleanup

All `a2a-f5b-*` containers, networks, group directories, locks and
per-project state were removed on both engines. The `enclave-a2a-hub` and
`enclave-claude:a2a-*` images built by the runs were removed;
`enclave-gateway-claude:latest` was rebuilt from this branch on both
engines and stays, as in I2. The pre-existing exited `-hs-` Docker
containers were not touched.

## Change requests

None.
