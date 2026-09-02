# 03: Measure The Planner And Explain Risk Divergence

**What to build:** A recorded number that says what feeding the review planner
the same evidence as `explain` actually costs, so the branch decision rests on
measurement rather than on preference.

**Blocked by:** 02.

**Status:** complete.

## Why this gates the fix

The divergence itself is already proved by reading the code, not by sampling.
The review planner supplies only symbols and paths, so the three detectors that
read added lines are starved and always report absent, and the sensitive-path
patterns are never supplied either. Three risk rules — behaviour change with an
incomplete graph, security-sensitive present, and behaviour change without
confirmed test coverage — therefore never fire on the review side at all.

What is not known is the price. Feeding the planner content is a risk-policy
change with a cost multiplier in agent invocations, not a neutral bug fix. A
commit that draws zero dimensions today can draw three to five once
`security_sensitive` can be present.

This runs after 02 deliberately. Measuring first would price the change against
false positives that 02 deletes, and could reject the fix for a cost that does
not exist.

## Method constraints

These fix what the number means. Getting any of them wrong produces a figure
that looks like evidence and is not.

**Hold the change profile constant.** `explain` derives its profile from a
range and the review planner derives it from a commit. Using each function on
its own side would let a disagreement in kind or symbols land inside the delta,
measuring two variables at once. Compute one profile per commit and feed the
same value to both arms, varying only the characteristics input.

**Stratify by change kind, and headline the source stratum.** Recent history is
heavily documentation-bearing, and documentation is exactly the population where
the answer is unchanged before and after. Averaged over that tail the change
looks nearly free while the real cost lands on source-bearing commits. Report
the source stratum as the headline and the documentation stratum separately.
Widen the window if the source stratum is thin.

**Count agent invocations, not distinct dimensions.** Bundle scheduling dedupes
on bundle name, not on dimension. At high risk with `public_api` or
`cross_module` present, `spec` is scheduled by both the correctness and the
contracts bundle, and `logic` by both correctness and concurrency-data, so those
dimensions launch two separate agent invocations. That regime is exactly where
ticket 05 pushes commits, so the convention must be stated and the invocation
count reported. Whether the double scheduling is intended is a separate question
this ticket only has to record, not settle.

**State the merge-commit policy.** Reviewed commits keep their SHAs under
`--no-ff` merges, so non-merge commits are the population review actually
audits. Exclude merges explicitly rather than letting the profile derivation
pick a parent.

**Harness placement.** The harness must import the review and risk packages, so
it lives inside the module at `tools/`, following the existing `tools/release`
precedent, with the artifact under the reengineering evidence directory
alongside the T9.4a files. Both arms are computable today: the plan derivation
and the risk evaluation are already exported, so the harness assembles the
`explain`-shaped input itself. Ticket 04 is what makes that assembly shared;
this ticket does not depend on it.

**Acceptance criteria:**

- [x] For at least the last 50 commits on `main`, the artifact records per
      commit: the `explain`-side risk level, the planner-side risk level, the
      dimensions the planner schedules today, and the dimensions it would
      schedule with the shared evidence.
- [x] Both sides are computed through the real code path, not re-derived by
      hand or by eye, from a single change profile per commit.
- [x] Merge commits are excluded and the exclusion is stated.
- [x] The aggregate is stratified by change kind, with the source-bearing
      stratum as the headline.
- [x] The harness that produced the artifact is committed with it, following
      the precedent already set by the T9.4a evidence artifacts.
- [x] The artifact states the aggregate: how many commits change risk level,
      and the total delta in scheduled agent invocations, with the counting
      convention named.
- [x] The artifact records that bundle scheduling dedupes by bundle and not by
      dimension, and what that does to the count at high risk.
- [x] `go build ./...` and `go vet ./...` cover the harness.
- [x] The artifact states a verdict — feed the planner, or stop `explain`
      reporting a risk level review will not act on — with the reason.
- [x] If the verdict is to feed the planner, the artifact states explicitly
      whether the risk rules also need adjusting, and defers that to its own
      change rather than folding it in.
- [x] Tickets 04 through 06 are re-read against the verdict before 04 starts;
      they assume the planner is fed.

## Evidence

- Harness: `tools/fu10divergence`, run as `go run ./tools/fu10divergence -n 120`.
- Artifact: `docs/reingenieria/evidence/fu10-divergence.json`, with the verdict
  and method in `fu10-divergence.md` beside it.
- Window: 120 non-merge commits from `HEAD`; 5 merges inside the window were
  excluded.
- Headline, source-bearing commits: 68 commits, 39 change risk level, 300 to 358
  agent invocations, +19%.
- Prose-only commits: 52 commits, 17 change risk level, 43 to 119 invocations.
  That stratum is a defect, not a cost: 17 are unlocked by `concurrency` alone,
  which reads the added lines of every path with no class filter.
- `profile.Kind` proved a poor stratifier and is reported only as a secondary
  breakdown: `ab3acee` changes two Go files and classifies as
  `kind=documentation`. The headline splits on whether any path classifies as
  `ClaseSource`.
- Bundle scheduling dedupes by bundle name and not by dimension, so `spec` and
  `logic` can each launch two agent invocations at high risk. The count uses
  invocations and says so.
- `go build ./...` and `go vet ./...` cover the harness.
- Verdict: feed the planner, with ticket 03b as a blocking precondition and a
  re-measurement before ticket 05. No risk-rule adjustment required.

## Follow-ups

- Ticket 03b narrows the concurrency detector the same way ticket 02 narrowed
  the security one. It blocks ticket 04.
- The 358 figure must be recomputed after 03b lands.
