# FU-10: what feeding the review planner the same evidence costs

Ticket 03 of the FU-10 sequence, re-measured after ticket 03b. Reproduce with:

    go run ./tools/fu10divergence -ref fce2da5 -n 120

The tip is pinned so the artifact stays reproducible: the window moves with
every new commit, and an unpinned run would not reproduce these figures. The raw
per-commit record is `fu10-divergence.json`, which carries the resolved tip, the
measured window, and the failure count.

## What was measured

For each of the 120 most recent non-merge commits reachable from the tip, one
change profile is derived and shared by both arms. Only the detector input
varies:

- **today**: the production plan derivation, which supplies symbols and paths.
- **shared**: the input `sentinel explain` assembles, adding the added lines,
  `.gitattributes`, and the sensitive-path patterns.

The artifact records the resolved ref and its SHA, and how many of the requested
commits were actually measured. A commit that cannot be measured is counted and
the run exits non-zero: an evidence harness that skipped a commit and still
reported the requested window would publish a short measurement as a complete
one. Both recorded runs measured 120 of 120.

5 merge commits fall inside the window and are excluded: under `--no-ff` the
reviewed commits keep their SHAs, so non-merge commits are the population review
actually audits.

The cost unit is **agent invocations**, not distinct dimensions. Bundle
scheduling in `AuditarCommit` dedupes by bundle name, so at high risk with
`public_api` or `cross_module` present, `spec` is scheduled by both the
correctness and the contracts bundle, and `logic` by both correctness and
concurrency-data. Each is a separate agent run. The artifact records the
dimension lists with duplicates intact, so the doubling is visible per commit
rather than asserted. Whether the double scheduling is intended is not settled
here.

## Result

| Stratum | Commits | Risk level changes | Invocations today | Invocations shared | Delta |
|---|---|---|---|---|---|
| Source-bearing | 69 | 40 | 304 | 364 | +20% |
| Prose-only | 51 | 3 | 48 | 54 | +13% |

Characteristics unlocked by the shared evidence, across the whole window:
`behavior_change` 67, `security_sensitive` 30, `concurrency` 23. The strata
totals shift by a commit or two between pinned tips as the window slides; the
+20% headline does not.

`profile.Kind` is a poor stratifier here and is reported in the JSON only as a
secondary breakdown: a commit changing two Go files alongside several documents
classifies as `kind=documentation`. The headline uses whether any path
classifies as `ClaseSource`.

The three prose-only commits that still change level are test-only metrics
commits unlocked by `security_sensitive` in `_test.go` files. Test code is code,
so that is the detector working, not leaking.

## What the first measurement found

The pre-03b run put the prose-only stratum at 43 to 119 invocations, a 177%
jump. 17 of those 52 commits were unlocked by `concurrency` alone:
`detectarConcurrencia` read the added lines of every path with no class filter,
so every ficha in this repository mentioning `context.Background()` read as a
concurrent change. That was a defect, not a cost, and ticket 03b removed it
before the planner migrates. The stratum now moves 48 to 54 over a comparable
window.

## Verdict

**Feed the planner.** The cost on the population that matters is 20% more agent
invocations, and it buys the structural hole FU-10 names: today
`security_sensitive`, `concurrency` and `behavior_change` can never be present
in a review plan, so three risk rules never fire and a source change adding
credential handling without touching an exported symbol schedules no security
review at all. 40 of 69 source-bearing commits are currently classified at a
lower risk level by review than by `explain`.

**No risk-rule adjustment is required.** The rules behave as designed once fed
real evidence; what looked like a rule problem was a detector-input problem, and
the two detector narrowings (tickets 02 and 03b) resolved the implausible
strata. No population is left where the level is wrong for its content.

Tickets 04 and 05 proceed.
