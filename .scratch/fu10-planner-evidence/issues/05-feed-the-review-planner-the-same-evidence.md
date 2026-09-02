# 05: Feed The Review Planner The Same Evidence

**What to build:** The review planner classifies a commit from the same
evidence `explain` uses, so the two surfaces stop disagreeing. A source change
that adds credential handling without touching an exported symbol now schedules
a security review instead of being invisible.

**Blocked by:** 04.

**Status:** ready-for-agent.

## Why this closes FU-10

This is the structural hole FU-10 names, and no commit in the phase exercised
it: the demonstrated instance was a false positive on the `explain` side, which
is evidence of the divergence, not evidence that the planner under-reviewed
that commit. The reverse case is the one that matters and it is unreviewed
today by construction, because the characteristic can never be present.

**Acceptance criteria:**

- [ ] The plan derivation takes its detector input from the shared constructor.
- [ ] All four consumers of the plan derivation are migrated.
- [ ] The three consumers that already hold the commit diff pass it through
      without adding any git subprocess; the fourth obtains it for its range.
- [ ] A focused RED test proves that a source change adding credential handling
      without touching an exported symbol schedules the security dimension, and
      fails before the production change.
- [ ] Tests prove `security_sensitive`, `concurrency`, and `behavior_change` can
      each be present in a review plan.
- [ ] A test pins that a documentation change with no risk characteristic still
      schedules no dimension, so the fix does not silently convert every commit
      into a full review.
- [ ] `go build ./...`, `go vet ./...`, focused tests, and the full suite pass,
      with the exact commands and outcomes recorded.
- [ ] `sentinel review` of each commit completes with no unresolved finding, and
      the final `sentinel gate` result is recorded.
