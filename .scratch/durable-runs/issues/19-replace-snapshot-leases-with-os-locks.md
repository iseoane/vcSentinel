# 19: Replace Snapshot Leases With OS Locks

**What to build:** Replace timestamp-based snapshot lease and purge markers with
recoverable operating-system-backed shared and exclusive locks.

**Blocked by:** Snapshot lease retention changes (complete through `2f270dc`).

**Status:** in-review.

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

- [ ] A subprocess test proves an exclusive purge lock is denied while another
      process holds a shared validation lock and succeeds after that process
      exits without explicit cleanup.
- [ ] Focused tests prove active snapshots are skipped and become purgeable
      after release.
- [ ] Timestamp leases, purge markers, heartbeats, and stale-marker recovery are
      removed.
- [ ] Linux tests and Windows cross-compilation pass.
- [ ] Build, vet, focused tests, full tests, guardian, and independent review
      evidence are recorded before closure.

**Rollback:** Revert the OS-lock implementation as one work unit; do not restore
the unsafe fixed-age stale-marker protocol.

## Evidence

- RED: `go test -count=1 ./internal/git -run
  '^TestSnapshotLockReleasedAfterProcessExit$'` failed because the required
  `lockSnapshot` seam did not exist.
- Focused subprocess and purge tests pass.
- `go test -count=1 ./internal/git`, `go test -count=1 ./...`, `go vet ./...`,
  and `go build ./...` pass.
- `GOOS=windows GOARCH=amd64 go test -c ./internal/git` passes.
- Post-change guardian reports 224 authored lines (`PUNTO_OPTIMO`).
- Implementer delegation was unavailable because configured model
  `x-preview-f-free` is not supported; implementation remained within this
  ticket's bounded scope.

## Follow-ups

*(recorded at closure)*
