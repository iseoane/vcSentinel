# 03: Measure The Planner And Explain Risk Divergence

**What to build:** A recorded number that says what feeding the review planner
the same evidence as `explain` actually costs, so the branch decision rests on
measurement rather than on preference.

**Blocked by:** 02.

**Status:** ready-for-agent.

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

**Acceptance criteria:**

- [ ] For at least the last 50 commits on `main`, the artifact records per
      commit: the `explain`-side risk level, the planner-side risk level, the
      dimensions the planner schedules today, and the dimensions it would
      schedule with the shared evidence.
- [ ] Both sides are computed through the real code path, not re-derived by
      hand or by eye.
- [ ] The harness that produced the artifact is committed with it, following
      the precedent already set by the T9.4a evidence artifacts.
- [ ] The artifact states the aggregate: how many commits change risk level,
      and the total delta in scheduled dimensions.
- [ ] The artifact states a verdict — feed the planner, or stop `explain`
      reporting a risk level review will not act on — with the reason.
- [ ] If the verdict is to feed the planner, the artifact states explicitly
      whether the risk rules also need adjusting, and defers that to its own
      change rather than folding it in.
- [ ] Tickets 04 through 06 are re-read against the verdict before 04 starts;
      they assume the planner is fed.
