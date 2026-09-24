# Public CLI end-to-end coverage

Status: complete — PR evidence convergence is resolved in the coordinator-owned PR slice; setup seams and public setup-flow coverage are implemented here, including the Windows manual-upgrade recommendation path and its documented native limitation.

## Goal

Exercise the public `vcsentinel` binary across every safe command family in disposable environments. The suite must build and execute a real CLI process, isolate Git/configuration/home/PATH state, replace external agents and services with deterministic doubles, and verify exit codes, stdout/stderr, and durable filesystem/Git effects.

## Safety boundary

- Never touch the real installed binary, `/usr/local/bin`, the real home directory, global Git configuration, live hooks, real GitHub, real agents, or publication endpoints.
- Use temporary repositories, isolated environment variables, local bare remotes, fake `gh`/agent/`rg`/CodeGraph executables, and local HTTP servers or transports.
- Keep destructive/global operations behind disposable roots or narrow test seams; do not weaken production validation to make tests pass.
- Preserve the existing uncommitted upgrade fix and `odd/tasks/upgrade-e2e-hardening.md`.
- This feature is intentionally split into reviewable slices; do not create one giant `e2e_all_test.go`.

## Baseline evidence

- `vcsentinel check` exits 0 with 286 authored candidate lines before this feature.
- Existing tests provide extensive handler/package coverage and a strong `internal/durableruns_e2e` suite, but almost no separately built public CLI binary execution.
- `internal/setup/upgrade_e2e_test.go` covers the Linux cross-filesystem upgrade regression and invalid staged-artifact rollback; this feature adds the broader CLI harness without replacing it.
- `internal/cli_e2e/setup_e2e_test.go` covers loopback release fixtures, disposable install roots, PATH-file/shell preservation, positive install/upgrade/uninstall behavior, idempotency, release failures, and invalid override rejection.
- The exploration map identified reusable Git, subprocess, daemon, fake-agent, fake-`gh`, and setup helpers and recorded command-family gaps.

## Acceptance matrix

| ID | Criterion | Evidence | Status |
|---|---|---|---|
| E2E-01 | A reusable runner builds and executes the real public binary with isolated HOME/Git/PATH state and captures exit code/stdout/stderr. | `internal/cli_e2e/harness_test.go`; `TestCLIRunnerExecutesIsolatedBinary` passed with disposable Git state and real-repository/home preservation assertions. | met |
| E2E-02 | Foundation/setup commands are covered: `version`, `help`, `init`, `uninit`, including idempotency and real disposable hook enforcement. | `internal/cli_e2e/foundation_test.go`; `TestCLIFoundationSetup` passed, including nested init, idempotency, oversized staged commit rejection, uninit cleanup, and HEAD preservation. | met |
| E2E-03 | Volume/slicing/consent commands are covered: `check`, `check --staged`, `slice plan/apply`, `consent-diff`. | `internal/cli_e2e/volume_slice_consent_test.go`; public-process tests passed for JSON volume, staged rejection, deterministic plan/apply, stale reapply protection, and consent grant/revoke state. | met |
| E2E-04 | Validation/analysis/state commands are covered: `lint`, `gate`, `doctor`, `explain`, `status`, `metrics`. | `internal/cli_e2e/validation_state_test.go`; public-process pass/failure tests, fake-agent doctor probes, explain range JSON, and status/metrics store identity all passed. | met |
| E2E-05 | Review/disposition commands are covered: `review`, `refute`, `accept`, `reopen`. | `internal/cli_e2e/review_dispositions_test.go`; three isolated public-process scenarios prove persisted CRITICAL findings, refutation clearing, reopen restoration, and acceptance retaining the block. | met |
| E2E-06 | Durable execution commands are covered: `runs start/status/logs/respond/abort/retry/recover/verify/prune`, daemon and attach. | `internal/cli_e2e/runs_test.go`, `runs_controls_test.go`, and `daemon_e2e_test.go` cover every listed run command, daemon start/status/stop, plain-text attach, and post-prune not-found behavior; interactive attach follow remains intentionally bounded by package-level TUI tests. | met |
| E2E-07 | PR/rebase/setup boundaries are covered safely: `pr review/create`, `rebase`, `install`, `upgrade`, `uninstall`. | `rebase_e2e_test.go` covers public rebase success and cancellation against disposable bare remotes; `pr_e2e_test.go` covers public `pr review --json`, missing-review and stale-review refusals, and the evidence-only review → create convergence path. `setup_boundary_test.go` proves unsupported setup arguments fail before side effects. `setup_e2e_test.go` covers the green disposable install → upgrade → uninstall lifecycle on Linux, repeated install/uninstall, and the Windows non-zero manual-upgrade recommendation with unchanged version/configuration/PATH followed by driver-launched uninstall. Invalid downloaded-artifact rollback remains Linux-only because Windows intentionally does not download an artifact. | met |
| E2E-08 | TUI receives deterministic process-level smoke coverage through supported seams; full PTY coverage is separately identified if not portable. | `internal/cli_e2e/tui_e2e_test.go` covers `tui --help`, `help tui`, isolated registry preflight failure, and daemon cleanup; Bubble Tea/keyboard interaction remains covered by package-level TUI/attach tests because portable PTY orchestration is out of scope. | met |
| E2E-09 | The suite is deterministic, bounded, and runnable through `go test ./...`; failures include captured process diagnostics and no leaked temporary state. | Focused setup tests use loopback fixtures, isolated HOME/TMPDIR, disposable install roots, and cleanup assertions. The Linux release-asset install/upgrade/uninstall lifecycle, expected Windows manual-upgrade path, release failures, and invalid overrides are green. PR convergence is resolved in the coordinator-owned PR slice. | met |

## Vertical slices

1. **CLI runner and isolation fixture** — **Done:** build a temporary binary; run it with isolated HOME, Git config, PATH, repository, and captured process results.
2. **Foundation and setup** — **Done:** version/help/init/uninit, hook installation, idempotency, and real disposable commit enforcement.
3. **Volume, slicing, and consent** — **Done:** check/staged check, plan/apply decisions, consent lifecycle, and Git effects.
4. **Validation, analysis, and state** — **Done:** lint/gate/doctor/explain/status/metrics with fake external probes and JSON/human contracts.
5. **Review and dispositions** — **Done:** review persistence plus refute/accept/reopen transitions through separate binary invocations, with a deterministic fake agent and persisted fingerprint evidence.
6. **Durable runs and service boundaries** — **Done:** public-process coverage spans start/status/logs/respond/abort/retry/recover/verify/attach/prune and the repository daemon start/status/stop lifecycle; interactive attach follow remains outside this portable process slice.
7. **PR, rebase, and installation boundaries** — **Done:** public rebase and PR convergence/refusal paths use disposable remotes and fake publication; setup commands reject unsupported arguments, use validated loopback/path seams, cover the Linux release-asset lifecycle, and exercise the Windows manual-upgrade recommendation without claiming automatic self-replacement.
8. **TUI smoke and suite hardening** — **Done:** deterministic public TUI help/preflight smoke is covered, PTY limits are documented, and the runner reuses one immutable binary per package with cleanup.

## Verification evidence

- The isolated CLI package has focused setup coverage for the green Linux release-asset lifecycle, the Windows manual-upgrade recommendation and unchanged-state assertions, release failures, and invalid override rejection.
- The PR evidence-only review → create convergence path is resolved in the coordinator-owned PR slice; this setup-seams worktree does not modify PR implementation paths.
- No commits, staging, publication, real GitHub, system installation path, real HOME, real go install, sudo, or live hook side effects were used by the setup tests.

## Current limitations and follow-up

- Native Windows automatic self-upgrade is intentionally not claimed: the installed executable is locked while it executes `upgrade`. The public Windows E2E instead verifies the non-zero source-based recommendation, unchanged installed state, and driver-launched uninstall. The printed manual command requires Go and scopes GOBIN to the resolved install root.
- The invalid downloaded-artifact rollback E2E is Linux-only because the production Windows path intentionally fails before release lookup and download.
- The PR evidence-only review → create convergence blocker is resolved in the coordinator-owned PR slice and is no longer the setup coverage blocker.

## Non-goals

- No real GitHub requests, PR publication, release publication, real model calls, or writes to system installation paths.
- No claim that a process-level smoke test replaces semantic unit tests or the existing durable-run E2E scenarios.
- No broad production refactor unless a narrowly scoped test seam is required and independently justified.
- No commit, push, or pull request without explicit user authorization.
