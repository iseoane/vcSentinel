# 19: Replace Snapshot Leases With OS Locks

**What to build:** Replace timestamp-based snapshot lease and purge markers with
recoverable operating-system-backed shared and exclusive locks.

**Blocked by:** Snapshot lease retention changes (complete through `2f270dc`).

**Status:** complete.

**Design contract:**

- Validation holds a shared lock for the complete lifetime of an acquired
  snapshot.
- Purge takes an exclusive non-blocking lock before deleting one snapshot and
  skips snapshots currently held by validation or another purge.
- Process death releases ownership through operating-system lock semantics;
  elapsed wall-clock time never invalidates a live owner.
- Lock files remain administrative metadata outside `snapshots/` and are never
  removed while they may participate in lock acquisition.
- Linux and Windows expose the same ownership behavior.

**Acceptance criteria:**

- [x] A subprocess test proves an exclusive purge lock is denied while another
      process holds a shared validation lock and succeeds after that process
      exits without explicit cleanup.
- [x] Focused tests prove active snapshots are skipped and become purgeable
      after release.
- [x] Timestamp leases, purge markers, heartbeats, and stale-marker recovery are
      removed.
- [x] Linux tests and Windows cross-compilation pass.
- [x] Build, vet, focused tests, full tests, guardian, and independent review
      evidence are recorded before closure.

**Rollback:** Revert the OS-lock implementation as one work unit; do not restore
the unsafe fixed-age stale-marker protocol.

## Evidence

- RED at the pre-implementation worktree state: `go test -count=1
  ./internal/git -run '^TestSnapshotLockReleasedAfterProcessExit$'` failed
  because the required `lockSnapshot` seam did not exist. The final public-flow
  regression is named `TestAcquireSnapshotLockReleasedAfterProcessExit`.
- `go test -count=20 ./internal/git -run
  '^TestAcquireSnapshotLockReleasedAfterProcessExit$'` passes.
- `go test -count=1 ./internal/git`, `go test -count=1 ./...`, `go vet ./...`,
  and `go build ./...` pass.
- `GOOS=windows GOARCH=amd64 go test -c ./internal/git` passes.
- Guardian reported 224 authored lines (`PUNTO_OPTIMO`) for the implementation
  worktree and 0 after the commits.
- Implementer delegation was unavailable because configured model
  `x-preview-f-free` is not supported; implementation remained within this
  ticket's bounded scope.
- Implementation commits: `9e952ec`, `6a331be`, and `3a7fdc0`.
- Independent Standards and Spec reviews of `2f270dc...3a7fdc0` reported no
  findings. The only residual risk is lack of a native Windows runtime run.
- `sentinel review 6a331be` identified a racy readiness publication; `3a7fdc0`
  replaced it with atomic temp-file rename, and `sentinel review 3a7fdc0`
  completed with `logic`, `spec`, and `tests` all `ok` and no unavailable
  dimension.
- Rollback boundary: revert `9e952ec..3a7fdc0` together; do not restore the
  fixed-age stale-marker protocol.

## Follow-ups

- Run the subprocess harness on native Windows CI when a Windows executor is
  available; cross-compilation proves shape, not runtime timing.
