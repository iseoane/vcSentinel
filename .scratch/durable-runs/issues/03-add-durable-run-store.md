# 03: Add the Durable Run Store

**What to build:** Persist run identities and normalized lifecycle events so a
controller can recover status, failures, and evidence after process exit.

**Blocked by:** 02: Define Durable Run Contracts.

**Status:** completed

- [x] Persist immutable request and policy identity records.
- [x] Append ordered, integrity-checked event frames with expected revisions.
- [x] Maintain atomic projections and receipts for durable state changes.
- [x] Support paged event reads, corruption detection, and explicit final-tail recovery.
- [x] Protect concurrent writers with OS-backed cross-process locking on supported platforms.
- [x] Prove live-lock ownership and process-exit release with subprocess tests.

**Evidence:** R2 is closed on `r2-durable-run-store` in commits `9fd204f`,
`a8e3289`, `d4a585c`, and `e17f9a9`. Direct review found no remaining blocking
findings; build, vet, full tests, Windows cross-build, and guardian checks passed.

**Rollback boundary:** Revert the R2 store commits without changing the R1
contract package or R0 gate evidence.
