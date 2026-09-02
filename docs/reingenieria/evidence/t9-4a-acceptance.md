# T9.4a — acceptance matrix

Completed 2026-09-01 against branch `f9-t9-4a`. Every row carries an exact
command or artifact reference; no row is pending, unknown, or asserted.

| ID | Criterion or obligation | Observable check | Owner | Exact evidence | Disposition |
|---|---|---|---|---|---|
| C-01 | Sufficiency thresholds frozen **before** collection | The freeze exists in history ahead of any evaluation | Coordinator | `a1a5803` commits [`t9-4a-contract.md`](t9-4a-contract.md); the evaluation lands later in `d3fd40a` | met |
| C-02 | Minimum duration frozen | S-2 in the contract | Coordinator | 7 consecutive days, derived from one calendar week | met |
| C-03 | Minimum logical-run volume frozen | S-2 in the contract | Coordinator | 500 logical runs; at the observed 61/day floor this needs 8 days, so duration stays binding | met |
| C-04 | Per-dimension coverage frozen | S-3 in the contract | Coordinator | Complete coverage plus at least 100 findings per dimension | met |
| C-05 | Effective-identity coverage frozen | S-4 in the contract | Coordinator | Complete identity coverage over measured runs and complete per-model attribution | met |
| C-06 | Usage/cost coverage frozen | S-1 and S-5 in the contract | Coordinator | Complete coverage plus a producer carrying an explicit `Provenance.Source` | met |
| C-07 | Included candidate classes frozen | S-6 in the contract | Coordinator | Every logical run in the local common-directory store, successes and failures alike | met |
| C-08 | Retry and failure handling frozen | S-6 in the contract | Coordinator | One logical run is one group key; `SuccessRate` denominators named; `failures[]` declared inadmissible | met |
| C-09 | The same store evaluated deterministically | One command against one named store state | Coordinator | `sentinel metrics --json` at 2026-09-01T19:49:35Z over 850 executions / 325 snapshots / 207 fichas, committed verbatim as [`t9-4a-metrics.json`](t9-4a-metrics.json) | met |
| C-10 | "No producer" is never recorded as "insufficient sample" | The verdict classifies before it measures | Coordinator | S-0 class A/B/C; the verdict table names FU-3, FU-6 and FU-8 as class A causes | met |
| C-11 | Insufficient evidence blocks calibration without moving a threshold | No threshold changed after evaluation | Coordinator | Contract frozen in `a1a5803`, unmodified through `8bf77ad`: `git diff a1a5803..HEAD -- docs/reingenieria/evidence/t9-4a-contract.md` is empty | met |
| C-12 | RED before the record existed | The reproducibility check fails without the verdict | Coordinator | `python3 docs/reingenieria/evidence/t9-4a-check.py` → exit 1, `FAIL: no T9.4a verdict section in the phase record` | met |
| C-13 | GREEN after the record existed | The same check passes | Coordinator | Same command → exit 0, `PASS: every cited figure is derivable from the artifact` | met |
| C-14 | Every task commit has a review record | One ficha per commit | Sentinel | `a1a5803`, `780c900`, `d3fd40a` → `ok`; `8bf77ad` → `ok`. All four recorded zero dimensions; see C-21 | met |
| C-15 | Build and vet | Repository checks | Coordinator | `go build ./...` exit 0; `go vet ./...` exit 0 | met |
| C-16 | Full test suite | Repository check | Coordinator | `go test -count=1 ./...` exit 0, zero `FAIL` lines | met |
| C-17 | Race scope | Touched Go packages | Coordinator | No Go package changed; the race scope is empty by construction, so no race command was run | met |
| C-18 | Staged volume enforced before each manual commit | The enforcing boundary | Sentinel | `sentinel check --staged` run immediately before all five commits; every one within budget, 0 authored lines | met |
| C-19 | Final gate | Pre-push gate | Sentinel | `sentinel gate --stage pre-push --timeout 1200` → `PASS`, exit 0 | met |
| C-20 | Durable runs settled and verified | Terminal state plus verification per run | Sentinel | Every run every gate emitted was settled and verified. First gate: `8c7e7102`, `b1d10a68`, `452462b8`, `5634bcbb`, `712e1cbc`. Second gate: `251ca163`, `baa64eec`, `2d08646c`, `49154b32`, `2ea85112`. All ten `succeeded`, all ten verified with 4 intact events | met |
| C-20b | The recording regress is bounded, not hidden | A gate run after this row is written cannot appear in it | Coordinator | Recording a gate's runs changes the tree that gate ran on. The rule in C-20 is what closes: every emitted run is settled and verified. The final gate's own five runs are named in the closure report rather than re-committed here | met |
| C-21 | Zero-dimension reviews explained, not waved through | The empty scope has a recorded cause | Coordinator | FU-10: the planner reads only symbols and paths, so a documentation candidate cannot exceed `NivelNone`. Disclosed in the T9.4a record itself. **Re-read 2026-09-02 after FU-10 was resolved: the outcome stands and was re-measured — every one of these commits still derives `NivelNone` under the full evidence — but the cause no longer holds, because the planner now reads content** | met; cause superseded |
| C-22 | Worktree clean after delivery | No residue | Coordinator | `git status --short` empty in `f9-t9-4a` | met |
| C-23 | Unrelated work untouched | Pre-existing changes preserved | Coordinator | The main worktree still holds `.claude/skills/reingenieria-phase-task/SKILL.md` modified and `references/` untracked; neither was staged, stashed, moved, or committed | met |
| C-24 | Language scan | English artifacts, legacy Spanish preserved | Coordinator | Every added line scanned; the only Spanish is the verbatim tool output `high por security_sensitive presente`, quoted as evidence. Legacy Spanish in `f9-observabilidad.md` and `f0-deuda.md` left untranslated | met |
| C-25 | FU-8 recorded | Follow-up with target and reason | Coordinator | `f0-deuda.md`, FU-8: failure classes double-counted | met |
| C-26 | FU-9 recorded | Follow-up with target and reason | Coordinator | `f0-deuda.md`, FU-9: observed identity unreadable by the producer | met |
| C-27 | FU-10 recorded | Follow-up with target and reason | Coordinator | `f0-deuda.md`, FU-10: the review planner never sees content | met |
| C-28 | At least one axis sufficient, or T9.4b explicitly blocked | The verdict names the outcome | Coordinator | Execution duration is sufficient at 325/325, so T9.4b is not blocked. Its right-censoring caveat travels with it | met |
