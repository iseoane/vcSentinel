# Feature: Preserve and chain existing pre-commit hooks

## Goal
Make `vcsentinel init` preserve an existing repository pre-commit hook and chain vcSentinel's staged-volume check without silently destroying repository behavior.

## Problem
The current initializer writes `<git-common-dir>/hooks/pre-commit` unconditionally. A foreign hook is truncated, and `uninit` cannot restore it.

## Decisions
- When a foreign pre-commit hook exists, preserve it and install a controlled wrapper that runs the original hook and vcSentinel's `check --staged`.
- When the hook is already vcSentinel-owned, keep initialization idempotent and avoid nesting wrappers.
- `uninit` removes vcSentinel from the chain and restores the original hook exactly, including its executable mode where applicable.
- A hook failure must retain normal pre-commit failure semantics; vcSentinel must not mask a failing original hook, and the original hook must not mask a failing vcSentinel check.
- Keep the implementation cross-platform for POSIX shells and Git for Windows; all new technical artifacts remain in English.
- Keep the full interrupted-install/restoration recovery path; the user explicitly chose to continue without artificial review slices despite the current advisory volume.

## Scope
- `cmd/vcsentinel/main.go`
- `cmd/vcsentinel/hook_install_test.go`
- Any narrowly scoped hook helper or fixture file required by the tests.

## Tasks

- [x] T1: Implement safe hook ownership and chaining for `init` and `uninit`.
  - Acceptance: no foreign hook is truncated; repeated `init` does not add nested wrappers; `uninit` restores the original hook.
  - Checks: focused hook lifecycle tests; inspect generated shell scripts for Linux and Windows path handling.
  - Route: delegated writer because the change spans production code and tests.

- [x] T2: Add regression coverage for foreign, vcSentinel-owned, failing, and absent hooks.
  - Acceptance: tests cover preservation, execution order/failure propagation, idempotence, restoration, and the existing no-hook flow.
  - Checks: `go test ./cmd/vcsentinel -run 'Test(Init|Uninit|Hook)'` or the exact narrower commands selected by the writer.
  - Route: same delegated writer, bounded to the scope above.

- [x] T3: Independently verify the task and record evidence.
  - Acceptance: focused tests, `go build ./...`, and `go vet ./...` pass; changed paths are limited to the task scope.
  - Checks: exact command outcomes recorded in this document and the final handoff.
  - Route: delegated verification where command execution is required.

## Acceptance Matrix

| ID | Criterion | Evidence | Status |
|---|---|---|---|
| C-01 | Existing foreign hooks are never silently destroyed. | Foreign-hook preservation, spoofing, orphan-sidecar, and modified-wrapper tests passed. | met |
| C-02 | vcSentinel and the original hook execute with deterministic failure propagation. | Chained execution tests passed for success, original failure, check failure, and both failures. | met |
| C-03 | Repeated `init` is idempotent and `uninit` restores the original hook. | Idempotence, restoration, interrupted install/restore, and post-rename validation tests passed. | met |
| C-04 | Cross-platform hook path/script behavior remains valid. | Linux generation/execution checks passed; Windows generation remains compile-time-only on this host. | met with limitation |
| C-05 | Build and vet pass after the change. | `go build ./...` and `go vet ./...` passed. | met |

## Verification Evidence
- Baseline `go run ./cmd/vcsentinel check`: passed; 0 added authored code lines.
- Native `vcsentinel check`: unavailable because `vcsentinel` is not on PATH; repository guidance fallback was used.
- Branch: `fix/init-preserve-precommit`.
- Writer RED: focused foreign-hook regression failed before production changes; subsequent correction RED checks failed before each confirmed fix.
- Writer GREEN: `go test -count=1 ./cmd/vcsentinel -run 'Test(Init|Uninit|Hook|StagedCheck)'` passed.
- Writer package verification: `go test -count=1 ./cmd/vcsentinel` passed.
- Independent verification: focused tests, full package tests, `go build ./...`, and `go vet ./...` all passed in the parent worktree.
- Final pre-commit verification before delivery: `go test -count=1 ./cmd/vcsentinel -run 'Test(Init|Uninit|Hook|StagedCheck)'` passed; `go test -count=1 ./cmd/vcsentinel` passed; `go build ./...` passed; `go vet ./...` passed; `git diff --check` passed.
- Final `go run ./cmd/vcsentinel check` after delivery: exit 0, 0 added authored lines, `SMALL`.
- Native semantic review was attempted after inspect/start; start was skipped because clone-local receipt-driven development is disabled. This is unavailable evidence, not a pass.
- Accepted limitation: transaction markers and hashes are integrity-checked local coordination state, not cryptographically authenticated; no ordinary-user data-loss path was demonstrated.
- Work-unit commit: `2703162` (`chore(slice): bypass AI for semantic unit 40ab36316513973a`) contains the hook implementation and regression tests; the task evidence is committed alongside the completed unit.
- Slice plan/apply used plan `3e1c2a58604209ff7fcd7f59d524a334e41eeb51261b43d72c6a1a8e2f40c58d`, with no pending decisions; the 1,133-line semantic unit received the plan's automatic bypass because it was not divisible by diff atoms.

## Next Step
The unit is closed and the working tree is clean. The native semantic review remains unavailable while clone-local receipt-driven development is disabled.
