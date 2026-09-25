# Prevent awaiting-run test teardown races

## Objective
Make `internal/execution` awaiting-run tests wait for detached receipt writers before `t.TempDir` cleanup, so unrelated CI pushes do not intermittently fail with `directory not empty`.

## Scope and constraints
- User authorized a separate scoped fix for the recurring CI failure.
- Fix scope: `internal/execution/instrumentation_test.go`, `internal/execution/controller_retry_test.go`, `internal/execution/controller_orphan_test.go`, and this task file; no production behavior, README or Pages changes. A separate user-requested CI trigger change lives in `.github/workflows/ci.yml` as commit `3cbd886`.
- Baseline CI run 36128005121 failed `TestFinalizeMetricsRejectsAwaitingAndRetryableHeads/awaiting` on `TempDir RemoveAll cleanup .../receipts: directory not empty`; earlier run 36104560396 failed the corresponding retry awaiting test, while 36127320152 passed.
- Local focused baseline `go test ./internal/execution -run '^(TestFinalizeMetricsRejectsAwaitingAndRetryableHeads|TestRetryRejectsRunsOutsideRetryableTerminalEvidence)$' -count=100` passed in 3.440s; `-race -count=20` passed in 2.405s. This is an intermittent failure, not a deterministic RED.
- Read-only code mapping identifies asynchronous `Start` writer and `WaitForActiveRuns` as a completion barrier; do not use sleeps or ignore cleanup errors. Verify existing helper behavior before editing.
- Route: one delegated writer for two non-trivial test files; separate independent verification after writer. TDD: not activated for this task; baseline and CI failure provide observed red evidence, focused and full verification required.
- Delivery: one focused work-unit commit on feature branch, push only with explicit user authorization already given in CI babysitting context; preserve unrelated work. Native RDD switch is off.

## Tasks
- [x] C1: Register bounded controller-worker cleanup barriers in awaiting-run fixtures, preserving behavior assertions. Check: writer's focused stress (-count=100) and race (-count=20) tests passed; scope only test files.
- [ ] C2: Resolve review BLOCK on `5e8fd9b`. Deterministic receipt-window test was not possible without a production seam; the user chose to consider an evidence-bound refutation, but repository-local vcSentinel setup disappeared before it could be submitted. Block remains open.
- [ ] C3: Verify tests/vet/build and publish the fix with the CI-only-PR trigger. PR CI remains unobserved until a separate pull request is opened.

## Progress and evidence
- Repeated baseline focused tests passed locally; failures reproduced only in CI logs. The failure can arise because awaiting state is visible before the receipt writer finishes.
- Writer added bounded `t.Cleanup` waits in both affected awaiting fixtures, after `t.TempDir` registration; focused stress (`-count=100`), race (`-race -count=20`) and `git diff --check` passed.
- Independent verifier confirmed LIFO cleanup and receipt persistence ordering; `go test ./internal/execution`, `go vet ./...` and `git diff --check` passed. No findings.
- Full `go test ./...`, `go vet ./...`, and `go build ./...` passed before commit `5e8fd9b`.
- First semantic review CLI timed out; user authorized orphan settlement and run `421292ea...` was canceled with valid stream. Second review persisted a design BLOCK for `5e8fd9b`: it claims the cleanup barrier is a no-op because awaiting state cannot be seen before receipt persistence. Source inspection disproved that premise: store appends event before receipt, while the worker holds `state.mu` through receipt persistence. The user chose a deterministic proof test and re-review instead of refutation.
- User requested CI only on pull requests; commit `3cbd886` removes the `push` trigger. Neither commit is published yet.
- A separate writer reported no reliable deterministic receipt-window test without adding a production test seam. The user chose to evaluate a refutation instead.
- Repository-local vcSentinel setup unexpectedly disappeared (configuration, managed skill, hook, guidance markers) in a pattern consistent with `uninit`; ownership is unknown. The user explicitly decided to preserve that removal, not restore or run `init`. The ledger and design BLOCK remain; `vcsentinel refute` cannot run without repository initialization. Preserve unrelated changes and do not stage or publish either commit.
- The user subsequently requested an independent code review in place of vcSentinel's review and explicitly requested committing and pushing all pending work to `main`. The independent review finds the stored design finding's main factual premise incorrect: awaiting is derived from an event appended before receipt persistence; `finish` holds `state.mu` until that persistence completes. The two-second wait can still time out; this residual risk remains. The historical vcSentinel BLOCK is not erased or represented as cleared.
- The user requested PR-only CI; a direct push to `main` will not trigger that CI. Full `go test ./...`, `go vet ./...`, `go build ./...`, and `git diff --check` passed on the complete candidate before delivery.
- User-chosen vcSentinel removal was committed as `7ed1787` (`chore(repo): remove repository-local vcSentinel setup`), without deleting the common-directory review ledger.
- C1 complete; C2 remains open in the historical ledger; C3 pending publication and later PR-only CI evidence.

## Next step
Commit the intentionally retained vcSentinel removal and updated task evidence, then fast-forward and push `main` under the user's explicit instruction. Report that PR-only CI did not run on this push and that the historical review block remains.
