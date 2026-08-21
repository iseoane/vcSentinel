# 01: Expose Gate and Review Evidence

**What to build:** Make ordinary gate output explain the confirmed blocker and
the exact semantic-review infrastructure failures that stopped a run.

**Blocked by:** None (can start immediately).

**Status:** completed

- [x] Render every effective confirmed CRITICAL finding without collapsing distinct evidence.
- [x] Render every unavailable semantic-review dimension and its reason.
- [x] Preserve existing verdict precedence and exit-code behavior.
- [x] Cover mixed legacy and current review evidence with focused tests.

**Evidence:** R0 landed in the gate evidence commits before R1 was started. The
final R0 review accepted the behavior and the full build/test checks passed.

**Rollback boundary:** Revert the R0 gate evidence changes without touching the
durable execution contracts or store.
