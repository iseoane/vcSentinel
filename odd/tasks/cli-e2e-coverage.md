# Public CLI end-to-end coverage

Status: in progress — E2E-07 now has a confirmed public PR evidence-convergence blocker; positive setup flows remain blocked by production endpoint/path seams.

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
- `internal/setup/upgrade_e2e_test.go` already covers the Linux cross-filesystem upgrade regression; this feature adds the broader CLI harness without replacing it.
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
| E2E-07 | PR/rebase/setup boundaries are covered safely: `pr review/create`, `rebase`, `install`, `upgrade`, `uninstall`. | `rebase_e2e_test.go` covers public rebase success and cancellation against disposable bare remotes; `pr_e2e_test.go` covers public `pr review --json`, missing-review and stale-review refusals, plus a black-box review → evidence-only commit → re-review → create reproducer. That reproducer fails at the documented success assertion: `pr create` rejects `.vcsentinel/evidence/.../pr-review.log` as uncommitted after the second review, and fake `gh` is not invoked. `setup_boundary_test.go` proves unsupported setup arguments fail before side effects; positive setup flows remain pending because endpoints/paths are not injectable. | partial |
| E2E-08 | TUI receives deterministic process-level smoke coverage through supported seams; full PTY coverage is separately identified if not portable. | `internal/cli_e2e/tui_e2e_test.go` covers `tui --help`, `help tui`, isolated registry preflight failure, and daemon cleanup; Bubble Tea/keyboard interaction remains covered by package-level TUI/attach tests because portable PTY orchestration is out of scope. | met |
| E2E-09 | The suite is deterministic, bounded, and runnable through `go test ./...`; failures include captured process diagnostics and no leaked temporary state. | Baseline verification passed 20 top-level CLI E2E tests/27 cases, the full Go suite, vet, build, focused race tests, full race rerun, and diff checks before the convergence reproducer was added. The new focused black-box regression is intentionally red: `go test ./internal/cli_e2e -run TestCLIPublicPRCreateE2E -count=1 -v` exits 1 with the exact evidence-not-committed diagnostic; `git diff --check` remains clean. | partial |

## Vertical slices

1. **CLI runner and isolation fixture** — **Done:** build a temporary binary; run it with isolated HOME, Git config, PATH, repository, and captured process results.
2. **Foundation and setup** — **Done:** version/help/init/uninit, hook installation, idempotency, and real disposable commit enforcement.
3. **Volume, slicing, and consent** — **Done:** check/staged check, plan/apply decisions, consent lifecycle, and Git effects.
4. **Validation, analysis, and state** — **Done:** lint/gate/doctor/explain/status/metrics with fake external probes and JSON/human contracts.
5. **Review and dispositions** — **Done:** review persistence plus refute/accept/reopen transitions through separate binary invocations, with a deterministic fake agent and persisted fingerprint evidence.
6. **Durable runs and service boundaries** — **Done:** public-process coverage spans start/status/logs/respond/abort/retry/recover/verify/attach/prune and the repository daemon start/status/stop lifecycle; interactive attach follow remains outside this portable process slice.
7. **PR, rebase, and installation boundaries** — **Blocked at the public PR convergence boundary:** public rebase success/cancellation and review/refusal paths are covered with disposable remotes and fake agents; the black-box evidence-only commit/re-review/create flow now reproduces the evidence mismatch before fake publication; setup commands reject unsupported arguments safely, while positive install/upgrade/uninstall require future injectable seams.
8. **TUI smoke and suite hardening** — **Done before the new blocker:** deterministic public TUI help/preflight smoke is covered, PTY limits are documented, and the runner reuses one immutable binary per package with cleanup; the current suite is intentionally red only at the new PR convergence reproducer.

## Verification evidence

- Before adding the intentional convergence blocker, the isolated CLI package ran 20 top-level tests (27 cases) in 41.162s after `harness_test.go` changed `newCLIRunner` to build one shared immutable binary per package process and remove it in `TestMain`.
- The stale-review and rebase-cancellation focused E2Es pass, with `go vet ./internal/cli_e2e` and `git diff --check` clean. The complete Go suite, build, focused race set, and successful full race rerun were previously green; the current package is intentionally red only at the new public PR convergence reproducer.
- No commits, staging, publication, system installation, network release, real agent, or real GitHub side effects were used.

## Current blocker

- `TestCLIPublicPRCreateE2E` is a black-box regression, not an adapted expected-failure test. It runs public `pr review --base <base> --json`, commits only the generated evidence, runs public review again, and then asserts that public `pr create --base <base>` succeeds and reaches fake `gh`.
- The current command exits `1` with `The pr review evidence is not committed: .vcsentinel/evidence/.../pr-review.log`. The second review rewrites the evidence after the evidence commit because its body embeds the current branch/head attestation, so `EvidenceAtHEAD` correctly rejects the dirty evidence.
- The production contract must be fixed or redesigned before this E2E can turn green. Do not weaken the test or seed the persisted entry internally as a workaround.

## Non-goals

- No real GitHub requests, PR publication, release publication, real model calls, or writes to system installation paths.
- No claim that a process-level smoke test replaces semantic unit tests or the existing durable-run E2E scenarios.
- No broad production refactor unless a narrowly scoped test seam is required and independently justified.
- No commit, push, or pull request without explicit user authorization.
