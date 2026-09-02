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
| C-01 | At least one default changes | The shipped default differs from `main` | Writer | `internal/config/parser.go:180`, 300s → 900s, in `68a2910` | met |
| C-02 | Focused RED before production code | A behaviour-level check fails for the requested default | Writer | `go test -count=1 ./internal/config -run 'TestDefaultsPerfiles\|TestTimeoutInvalidoSeIgnora'` → FAIL, `review defaults = 5m0s/2, expected 900s/2` | met |
| C-03 | GREEN after production code | The same check passes | Writer | Same command → `ok github.com/ISeoane-Quental/vas.sentinel/internal/config`. Re-run independently by the coordinator | met |
| C-04 | Change cites a reproducible metric | The metric is re-derivable from the store | Coordinator | `sentinel metrics --json` duration coverage 325/325; committed at T9.4a as `evidence/t9-4a-metrics.json` | met |
| C-05 | Change cites period, sample, coverage | Named above | Coordinator | 2026-08-29..2026-09-01, n=325, coverage 1 | met |
| C-06 | Change cites old and new value | Named above | Coordinator | 300s and 600s → 900s | met |
| C-07 | Change cites expected effect | Named above | Coordinator | Exceedance 12.3% / 1.8% → 0% | met |
| C-08 | Change cites risk | Named above | Coordinator | Hung-provider detection delayed by up to 600s | met |
| C-09 | Change cites rollback | Named above | Coordinator | Restore both values; nothing else accompanies the change | met |
| C-10 | Correlation is not presented as causality | No causal claim from the per-dimension split | Coordinator | Stated explicitly above | met |
| C-11 | No test, fixture, golden, or verification asset changed to make the calibration pass | Only assertions that pin the old default value change | Writer | Coordinator inspected the staged diff: exactly four paths, and `git diff --cached --name-only \| grep -c validacion_test.go` returns `0` | met |
| C-12 | Full suite | Repository check | Writer | `go test -count=1 ./...` exit 0, zero `FAIL` lines, re-run independently by the coordinator | met |
| C-13 | Race on touched packages | Explicit package list | Writer | `go test -count=1 -race ./internal/config` ok 1.059s, re-run independently by the coordinator | met |
| C-14 | Build and vet | Repository checks | Writer | `go build ./...` and `go vet ./...` both exit 0, re-run independently by the coordinator | met |
| C-15 | Every task commit reviewed | One ficha per commit | Sentinel | `68a2910` → `warn` over 5 dimensions (security ok, design ok, spec/tests/logic warn). `8d659d7` and `5178e4f` are documentation-only and drew zero dimensions, per FU-10. **Re-read 2026-09-02: both still derive `none` under the resolved planner, so the zero scope was correct on its merits and not only a limit** | met; the FU-10 limit no longer applies |
| C-16 | Staged volume enforced | The enforcing boundary | Sentinel | `sentinel check --staged` run immediately before all three commits; every one within budget. The calibration commit reported 6 authored lines | met |
| C-17 | Final gate | Pre-push gate | Sentinel | `sentinel gate --stage pre-push --timeout 1200`; result recorded in the closure report | met |
| C-18 | Durable runs settled and verified | Terminal state plus verification per run | Sentinel | The rule that closes: every emitted run is settled and verified. The review of `68a2910` emitted 5 — `60b3d266`, `e668c07f`, `88c1da2b`, `4da3ea24`, `bda1350f` — all `succeeded`, all verified with 4 intact events. A gate run after this row is written cannot appear in it, so the final gate's own runs are named in the closure report | met, regress bounded |
| C-19 | Effective writer identity recorded from execution evidence | Observed, not requested | Coordinator | Model confirmed as `gpt-5.6-luna` from the execution log; agent deviates to `build`; effort not verifiable. See the deviation section below | met, with two deviations recorded |
| C-20 | Worktree clean, unrelated work untouched | No residue | Coordinator | `git status --short` empty after delivery. The main worktree still holds `.claude/skills/reingenieria-phase-task/SKILL.md` modified and `references/` untracked; neither was staged, stashed, moved, or committed | met |
| C-21 | Language scan | English artifacts, legacy Spanish preserved | Coordinator | Every added line across `docs`, `internal` and `.vas_sentinel` scanned for Spanish markers: no hits. The two touched assertion messages moved to English because their lines changed anyway; their untouched neighbours keep legacy Spanish, which is exactly what the policy prescribes | met |

## Findings and dispositions

Sentinel's review of `68a2910` returned `warn` with three findings. Each premise
was verified in the repository before disposition.

| Finding | Dimension | Premise verified | Disposition |
|---|---|---|---|
| Six versus seven runs over 600s across two records | spec | CONFIRMED. `f9-observabilidad.md:710` counted 7 over all 850 logical runs; `:778` counted 6 over the 325 measured runs; neither named its population | fixed in `5178e4f` |
| The record claimed the project config "currently" sets 600s, which this commit made false | design | CONFIRMED. `f9-observabilidad.md:705` | fixed in `5178e4f` |
| No test covers the checked-in project override | tests | Premise CONFIRMED — no such test exists. Remedy REJECTED | accepted with reason: every config test writes a temporary fixture, and no test in this repository reads the real `.vas_sentinel/vassentinel.yml`. Pinning an operational setting a user is expected to tune would turn a legitimate retune into a suite failure |

The two fixes landed in a documentation-only commit, which drew zero review
dimensions for the same FU-10 reason recorded against T9.4a. **Re-read
2026-09-02: FU-10 is resolved and that commit still derives `none` with the
planner reading content, so the empty scope reflects the change and not a
blind spot.** `registrarCorrecciones`
did not mark `FixedIn`, which is correct: it links only blocked fichas, and
`68a2910` is `warn`.

## Deviation from the F9 writer assignment

The plan assigns T9.4b to OpenCode `openai/gpt-5.6-luna` at Max effort. Recorded
from execution evidence rather than from the flags passed:

- Model: **confirmed**. `modelID=gpt-5.6-luna`, `providerID=openai`, session
  `ses_fa13ed13affedi9m6FO8n8IGHZ`.
- Agent: **deviates**. The writer ran as `build`, not the phase's
  `reingenieria-implementer`, because `opencode run` drives primary agents only
  and silently falls back when handed a subagent name.
- Reasoning effort: **not verifiable**. `--variant max` was passed, but no
  execution record confirms the effort actually applied. This is FU-9's gap
  appearing in the delegation path itself, and it is recorded rather than
  asserted.
