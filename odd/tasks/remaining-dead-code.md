# Reconfirm and Remove Remaining Dead Code

## Goal
Reconfirm each outstanding Staticcheck/deadcode candidate against current source and call paths. Remove a candidate only if it is still unused; run the smallest relevant Go package test before moving to the next candidate.

## Ordered Work Units
1. `internal/acpadapter/review.go`: `AcpxAdapter.outputOf`.
2. `internal/app/pr/publish.go`: `exitOnError`.
3. `internal/gate/gate_durable.go`: `settlementForState`.
4. `internal/git/agentplan.go`: `decisionRecorder.callback`.
5. `internal/git/agentplan.go`: `filesForPlan`.
6. `internal/git/agentplan.go`: `assignFileSelectors`.
7. `internal/git/plan.go`: `groupByLayers`.
8. `internal/git/plan.go`: `groupByClasses`.
9. `internal/ops/events.go`: `writeTemporaryLog`.
10. `internal/review/engine.go`: `restrictedToolReviewer`.
11. `internal/review/finding.go`: `extractJSONLBlock`.
12. `internal/git/draft.go`: `HashDraftState` (exported inside an `internal` package; verify local API intent before deletion).
13. `internal/git/staged.go`: `CheckStagedDiffLimits` (comment describes a short-form contract; verify intent before deletion).
14. `internal/review/dispositions.go`: `ResolveDispositionTargetWithDispositions` (verify supersession and local API intent).
15. `internal/planning/context.go`: `GitTreeReader.Show` and its helper `gitObjectPath` (check constructors); also update the matching curated `internal/adaptersites/inventory.go` entry if the source site is removed.
16. `internal/remediation/`: verify package importers and production reach. If its source is only self-referenced by tests, remove the dormant package as one unit; preserve it if any production consumer exists.
17. `internal/gate/gate_test.go`: orphan reviewer doubles/helpers.
18. `internal/review/engine_test.go`: `hasStrings`, `hasPrompt`, `hasResultBundle`.
19. `internal/tui/fakes_test.go`: `fakeHost.appliedCount`, `scriptedProvider.callCount`.
20. `internal/remediation/`: package-level production reach; remove the dormant package only if no production importer exists.
21. `internal/planning/`: package-level production reach and roadmap intent; if retired, reconcile architecture and design references.
22. `tools/tuipreview/main.go`: cover ANSI-to-HTML behavior and fix any confirmed conversion defect.

## Acceptance Criteria
- Reconfirm exact identifier references and dynamic/interface/build-tag considerations immediately before each deletion.
- Do not remove any symbol with a live caller or required interface contract.
- After each individual candidate or tightly coupled implementation island, run the owning package's Go tests before proceeding.
- Record candidates retained due to API intent, interface obligations, or uncertainty, with the evidence and decision needed.
- At closure run `go test ./...`, `go vet ./...`, `git diff --check`, and current `deadcode`/`staticcheck -checks=U1000` analysis.

## Constraints
- Read-only investigation unless a candidate is reconfirmed dead.
- Keep removals minimal; no broad refactors or unrelated test rewrites.
- Do not stage, commit, push, or open a PR without a fresh explicit request.
- Current branch: `refactor/remove-remaining-dead-code`, created from clean `main` at `621e1d7133f0ed94ac88d3b8889fa5606b948534`.

## Status
- Step 1: reconfirmed `AcpxAdapter.outputOf` had no caller (CodeGraph showed only its declaration; active methods return `res.Output` directly), removed it, and `go test ./internal/acpadapter` passed.
- Step 2: exact search found `exitOnError` only at its declaration/comment, removed it, and `go test ./internal/app/pr` passed.
- Step 3: CodeGraph found no caller of `settlementForState`; the live gate constructs `rootSettlement` inline. Removed it; `go test ./internal/gate` passed.
- Step 4: exact search found only the declaration of `decisionRecorder.callback`; removed it. The first `go test ./internal/git` exposed the now-unused `fmt` import; removed that import and reran `go test ./internal/git`, which passed.
- Step 5: CodeGraph and exact search found only the declaration of `filesForPlan`; removed it, and `go test ./internal/git` passed.
- Step 6: CodeGraph and exact search found only the declaration of `assignFileSelectors`; removed it, and `go test ./internal/git` passed.
- Step 7: CodeGraph and exact source search found no callers of `groupByLayers`; one old test comment mentions it. Removed it; `go test ./internal/git` passed.
- Step 8: exact search found only the `groupByClasses` declaration; worker reconfirmed no interface or dynamic use, removed it, and reported `go test ./internal/git` passed.
- Step 9: CodeGraph showed `writeTemporaryLog` only wrapping the live terminator writer and exact source search found no callers. Removed the wrapper; `go test ./internal/ops` passed. Preserved `writeRawTemporaryLog` and the testable rename variant.
- Step 10: exact search found only local `restrictedToolReviewer`; the similarly named `reviewexec.RestrictedReviewer` is distinct and live. Removed the unused interface; `go test ./internal/review` passed.
- Step 11: CodeGraph showed the parser calls only `extractJSONLBlockWithSchema`; exact search found the old wrapper's declaration only. Removed it; `go test ./internal/review` passed.
- Step 12: CodeGraph and exact source/doc search found only `HashDraftState`'s declaration; no consumers or compatibility contract beyond its doc comment. Removed it; `go test ./internal/git` passed.
- Step 13: exact search found only the declaration of `CheckStagedDiffLimits`; it only wrapped `MeasureStagedVolume`, with no consumers. Removed it; `go test ./internal/git` passed. Its doc's claimed short-form contract was not exercised in-repo; package is internal.
- Step 14: exact search found no consumers of `ResolveDispositionTargetWithDispositions`; active commands call the record-level resolver. Removed the revision wrapper; `go test ./internal/review` passed.
- Step 15: exact Go search found no `GitTreeReader` construction and no production caller of `BuildContext`; removed the dormant concrete `GitTreeReader`/`gitObjectPath` island while preserving the injectable `TreeReader` seam. The planning package test exposed now-unused `strings`; after removing it, `go test ./internal/planning` passed. Full-suite verification exposed a stale curated adaptersites entry for the removed command; removed only that inventory entry, `go test ./internal/adaptersites` passed, and rerun `go test ./...` passed all packages.
- Step 16: `GuardedEditor.Read` is required by the `Editor` interface; `GuardedEditor` implements it and its `Edit` reads through the wrapped editor. Construction is test-only/no production caller, so the abstraction appears dormant, but deleting `Read` alone would break its documented interface contract. Kept the method; `go test ./internal/remediation` passed.
- Step 17: exact search showed the gate-local reviewer doubles/helpers had no test or runtime callers; their only intra-cluster calls were to each other. Removed the cluster and now-unused imports; `go test ./internal/gate` passed. A separate `completeTestContract` helper in `internal/review` remains live and was not changed.
- Step 18a: exact Go search found `hasStrings` only at its declaration; removed it and `go test ./internal/review` passed.
- Step 18b: exact Go search found `hasPrompt` only at its declaration; removed it and `go test ./internal/review` passed.
- Step 18c: exact Go search found `hasResultBundle` only at its declaration; removed it and `go test ./internal/review` passed (27.121s).
- Step 19a: exact search found `fakeHost.appliedCount` only at its declaration; removed it, preserving `appliedActions`; `go test ./internal/tui` passed.
- Step 19b: exact search found `scriptedProvider.callCount` only at its declaration; removed it, preserving the `calls` sequence state; `go test ./internal/tui` passed.
- Step 20: exact package-import and `go list -deps` checks found no production references to `internal/remediation`; only package-local tests/path literals used it. The user removed all eight package files; repository-wide Go search found no remaining package/type references.
- Step 21: exact Go-source search found no production importers or callers of `internal/planning`; its APIs are used only in package-local tests. The user removed the four package files. Updated `AGENTS.md` and F5 in `docs/design/replanteamiento-objetivo.md` to retain the planner as a future goal without keeping an orphan implementation, and changed the stale agentadapter fixture path. Focused `go test ./internal/agentadapter` passed.
- Step 22: `tools/tuipreview` had no test files. Added table-driven tests for HTML escaping, xterm color cube, and grayscale conversion. They exposed `escapeANSI` slicing the CSI sequence from `i+1` (retaining `[`), so the color prefix was never recognized. Changed the slice start to `i+2`; focused tests passed.
- Final verification: `go test ./tools/tuipreview`, `go test ./...`, `go vet ./...`, `git diff --check`, Staticcheck U1000, and `deadcode -test ./...` all passed.
- Follow-up candidates not flagged by analyzers: `SerializePlan` is called only by `internal/git/semantic_test.go`; `BuildFragmentationPlan` is called only by `internal/git/plan_test.go`, while production calls `BuildFragmentationPlanWithReader`. Both are exported internal/test-facing seams; do not remove automatically without separate intent review.
