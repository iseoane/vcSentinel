# Prevent awaiting-run test teardown races

## Objective
Make `internal/execution` awaiting-run tests wait for detached receipt writers before `t.TempDir` cleanup, so unrelated CI pushes do not intermittently fail with `directory not empty`.

## Scope and constraints
- User authorized a separate scoped fix for the recurring CI failure.
- Edit only `internal/execution/instrumentation_test.go`, `internal/execution/controller_retry_test.go`, and this task file; no production behavior, workflow, README or Pages changes.
- Baseline CI run 36128005121 failed `TestFinalizeMetricsRejectsAwaitingAndRetryableHeads/awaiting` on `TempDir RemoveAll cleanup .../receipts: directory not empty`; earlier run 36104560396 failed the corresponding retry awaiting test, while 36127320152 passed.
- Local focused baseline `go test ./internal/execution -run '^(TestFinalizeMetricsRejectsAwaitingAndRetryableHeads|TestRetryRejectsRunsOutsideRetryableTerminalEvidence)$' -count=100` passed in 3.440s; `-race -count=20` passed in 2.405s. This is an intermittent failure, not a deterministic RED.
- Read-only code mapping identifies asynchronous `Start` writer and `WaitForActiveRuns` as a completion barrier; do not use sleeps or ignore cleanup errors. Verify existing helper behavior before editing.
- Route: one delegated writer for two non-trivial test files; separate independent verification after writer. TDD: not activated for this task; baseline and CI failure provide observed red evidence, focused and full verification required.
- Delivery: one focused work-unit commit on feature branch, push only with explicit user authorization already given in CI babysitting context; preserve unrelated work. Native RDD switch is off.

## Tasks
- [x] C1: Register bounded controller-worker cleanup barriers in awaiting-run fixtures, preserving behavior assertions. Check: writer's focused stress (-count=100) and race (-count=20) tests passed; scope only test files.
- [ ] C2: Independently verify fix, run full Go tests/vet/build, commit and publish for CI, then observe run and record outcome.

## Progress and evidence
- Repeated baseline focused tests passed locally; failures reproduced only in CI logs. The failure can arise because awaiting state is visible before the receipt writer finishes.
- Writer added bounded `t.Cleanup` waits in both affected awaiting fixtures, after `t.TempDir` registration; focused stress (`-count=100`), race (`-race -count=20`) and `git diff --check` passed.
- Independent verifier confirmed LIFO cleanup and receipt persistence ordering; `go test ./internal/execution`, `go vet ./...` and `git diff --check` passed. No findings.
- C1 complete, C2 in progress; full suite and CI still pending.

## Next step
Run the whole Go suite and build independently, then commit the scoped fix and observe CI.
