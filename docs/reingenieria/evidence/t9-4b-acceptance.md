# T9.4b — acceptance matrix

Completed before delegation, 2026-09-01, against branch `f9-t9-4b`.

## The calibrated change

| Field | Value |
|---|---|
| Metric | Total duration of one reviewer invocation, `ExecutionTiming.TotalDurationNanos` |
| Period | 2026-08-29..2026-09-01, the window in which metrics snapshots exist |
| Sample | 325 measured runs |
| Coverage | 325 / 325, complete |
| Population | Homogeneous: every one of the 325 is a `review <dimension>` operation. No validation run carries a snapshot |
| Old value | `internal/config/parser.go:180` ships `Review.Timeout = 300 * time.Second`; `.vas_sentinel/vassentinel.yml` overrides it to `600` |
| New value | `900` seconds in both places |
| Expected effect | Among the 325 measured runs, exceedance falls from 12.3% (40/325) at 300s and 1.8% (6/325) at 600s to 0% (0/325) at 900s. Seven runs exceeded 600s across all 850 logical runs; the seventh carries no metrics snapshot and is outside this denominator |
| Risk | A genuinely hung provider is detected up to 600s later than at 300s. Sentinel exists to review work, so killing real review work is the worse failure |
| Rollback | Restore both values. No schema, storage, or contract change accompanies this, so a revert is complete |

## Why the shipped default may be judged from a sample collected under larger budgets

None of the 325 runs ran under a 300s budget; every one had 600s or more. The
extrapolation is nonetheless sound in one direction only, and only that
direction is used: **an observed completion duration is a property of the work,
not of the budget that bounded it.** A run that finished in 334s required 334s;
under a 300s budget it would have been killed. A budget can only truncate the
tail, so a lower budget kills more runs, never fewer. The 40 completions above
300s are therefore direct evidence against the shipped default.

The reverse inference is not available and is not made: runs killed at ~606s
might have needed far more than 900s, and the store records no per-run budget,
so the tail is censored at an unknown mix of levels. The claim is that 300s and
600s are too low, not that 900s is provably sufficient.

## Correlation is not causality

The per-dimension breakdown below is recorded as observation, not as a causal
claim about dimensions. Different dimensions reviewed different candidates.

| Dimension | n | p50 | p95 | max | over 300s |
|---|---|---|---|---|---|
| design | 55 | 38.4s | 78.2s | 135.4s | 0 |
| logic | 93 | 91.7s | 606.4s | 718.7s | 23 |
| security | 25 | 42.6s | 107.6s | 129.8s | 0 |
| spec | 84 | 60.4s | 157.0s | 191.5s | 0 |
| tests | 68 | 147.3s | 436.1s | 607.3s | 17 |

The cost concentrates in `logic` and `tests`; three dimensions never approach
300s. `review.timeout` is a single scalar, so this change cannot exploit that.
It is recorded because a per-dimension budget is the obvious next question, and
this is the evidence a future task would start from.

## Matrix

| ID | Criterion or obligation | Observable check | Owner | Exact evidence | Disposition |
|---|---|---|---|---|---|
| C-01 | At least one default changes | The shipped default differs from `main` | Writer | `internal/config/parser.go:180`, 300s → 900s | pending |
| C-02 | Focused RED before production code | A behaviour-level check fails for the requested default | Writer | Exact command, exit status, observed failure | pending |
| C-03 | GREEN after production code | The same check passes | Writer | Exact command, exit status, observed success | pending |
| C-04 | Change cites a reproducible metric | The metric is re-derivable from the store | Coordinator | `sentinel metrics --json` duration coverage 325/325; committed at T9.4a as `evidence/t9-4a-metrics.json` | met |
| C-05 | Change cites period, sample, coverage | Named above | Coordinator | 2026-08-29..2026-09-01, n=325, coverage 1 | met |
| C-06 | Change cites old and new value | Named above | Coordinator | 300s and 600s → 900s | met |
| C-07 | Change cites expected effect | Named above | Coordinator | Exceedance 12.3% / 1.8% → 0% | met |
| C-08 | Change cites risk | Named above | Coordinator | Hung-provider detection delayed by up to 600s | met |
| C-09 | Change cites rollback | Named above | Coordinator | Restore both values; nothing else accompanies the change | met |
| C-10 | Correlation is not presented as causality | No causal claim from the per-dimension split | Coordinator | Stated explicitly above | met |
| C-11 | No test, fixture, golden, or verification asset changed to make the calibration pass | Only assertions that pin the old default value change | Writer | `secciones_test.go:79` and `:178-179` pin 300s and are updated. `validacion_test.go:218,246` is a **capability** timeout that happens to be 300 and must stay untouched | pending |
| C-12 | Full suite | Repository check | Writer | `go test -count=1 ./...` | pending |
| C-13 | Race on touched packages | Explicit package list | Writer | `go test -count=1 -race ./internal/config` | pending |
| C-14 | Build and vet | Repository checks | Writer | `go build ./...`, `go vet ./...` | pending |
| C-15 | Every task commit reviewed | One ficha per commit | Sentinel | This task ships Go code, so a non-empty dimension set is expected here, unlike T9.4a | pending |
| C-16 | Staged volume enforced | The enforcing boundary | Sentinel | `sentinel check --staged` before each commit | pending |
| C-17 | Final gate | Pre-push gate | Sentinel | `sentinel gate --stage pre-push` | pending |
| C-18 | Durable runs settled and verified | Terminal state plus verification per run | Sentinel | Every emitted run `succeeded` with a `runs verify` result | pending |
| C-19 | Effective writer identity recorded from execution evidence | Observed, not requested | Coordinator | Recorded from the run evidence, never from the flags passed. FU-9 is exactly this gap | pending |
| C-20 | Worktree clean, unrelated work untouched | No residue | Coordinator | `git status --short`; the main worktree's `.claude/skills/...` changes stay untouched | pending |
| C-21 | Language scan | English artifacts, legacy Spanish preserved | Coordinator | Every added line scanned | pending |
