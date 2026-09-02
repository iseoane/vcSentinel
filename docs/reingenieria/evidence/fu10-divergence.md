# FU-10: what feeding the review planner the same evidence costs

Ticket 03 of the FU-10 sequence. Produced by `go run ./tools/fu10divergence
-n 120`; the raw per-commit record is `fu10-divergence.json`.

## What was measured

For each of the 120 most recent non-merge commits reachable from `HEAD`, one
change profile is derived and shared by both arms. Only the detector input
varies:

- **today**: the production plan derivation, which supplies symbols and paths.
- **shared**: the input `sentinel explain` assembles, adding the added lines,
  `.gitattributes`, and the sensitive-path patterns.

5 merge commits fall inside the window and are excluded: under `--no-ff` the
reviewed commits keep their SHAs, so non-merge commits are the population review
actually audits.

The cost unit is **agent invocations**, not distinct dimensions. Bundle
scheduling in `AuditarCommit` dedupes by bundle name, so at high risk with
`public_api` or `cross_module` present, `spec` is scheduled by both the
correctness and the contracts bundle, and `logic` by both correctness and
concurrency-data. Each is a separate agent run. Whether that double scheduling
is intended is not settled here; it is recorded so the count is readable.

## Result

| Stratum | Commits | Risk level changes | Invocations today | Invocations shared | Delta |
|---|---|---|---|---|---|
| Source-bearing | 68 | 39 | 300 | 358 | +19% |
| Prose-only | 52 | 17 | 43 | 119 | +177% |

`profile.Kind` is a poor stratifier here and is reported in the JSON only as a
secondary breakdown. Commit `ab3acee`, which changes two Go files, classifies as
`kind=documentation` because most of its files are not source. The headline uses
whether any path classifies as `ClaseSource`, which separates the populations
correctly.

Characteristics unlocked by the shared evidence, across the whole window:
`behavior_change` 67, `concurrency` 37, `security_sensitive` 30.

## Verdict

**Feed the planner.** The cost on the population that matters is 19% more agent
invocations, and it buys the structural hole FU-10 names: today
`security_sensitive`, `concurrency` and `behavior_change` can never be present
in a review plan, so three risk rules never fire and a source change adding
credential handling without touching an exported symbol schedules no security
review at all. 39 of 68 source-bearing commits are currently classified at a
lower risk level by review than by `explain`.

**One precondition, and it is not optional.** The prose-only stratum nearly
triples, and that is a defect rather than a cost. 17 of those commits are
unlocked by `concurrency` alone: `detectarConcurrencia` matches `context.`,
`chan `, `sync.` and `go ` in the added lines of **every** path, with no path
class filter — the exact defect ticket 02 removed from the security detector,
still present in the concurrency one. Migrating the planner before narrowing it
would import a prose false positive wholesale and make every documentation
commit draw a full review.

Recorded as ticket 03b, which blocks ticket 04.

**No risk-rule adjustment is required.** The rules behave as designed once fed
real evidence; what looked like a rule problem is a detector-input problem. The
`security_sensitive` and `database` rules that jump a change straight to high
were already reachable through paths, and the measurement shows no stratum where
the level is implausible for its content.

## Re-measurement

The 358 figure includes the concurrency false positives on source-bearing
commits that also touch documentation. Re-run this harness after ticket 03b and
record the corrected source-bearing delta before ticket 05 starts.
