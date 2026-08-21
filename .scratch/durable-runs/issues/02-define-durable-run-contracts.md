# 02: Define Durable Run Contracts

**What to build:** Establish provider-neutral immutable identities and lifecycle
contracts so a run can contain logical jobs and separately attributable physical
invocations.

**Blocked by:** 01: Expose Gate and Review Evidence.

**Status:** completed

- [x] Define immutable request, run, job, and invocation identities.
- [x] Define lineage for retries, fallbacks, child work, and responses.
- [x] Define lifecycle transitions, terminal classes, and execution decisions.
- [x] Define normalized events that retain run, job, invocation, and lineage identity.
- [x] Keep semantic review verdicts outside the operational lifecycle model.

**Evidence:** Completed on `r1-run-contracts-closure2` in commit `18e8f5a`.
The final candidate stayed within the review budget and passed build, vet, and
focused/full tests.

**Rollback boundary:** Revert the pure `internal/agentrun` contract unit and its
tests without removing R0 gate behavior.
