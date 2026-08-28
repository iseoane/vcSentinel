# 18: Harden Review Run Ownership

**What to build:** Prevent repository-daemon shutdown and controller completion
from racing over review runs, and retry one malformed semantic review response
before classifying its dimension as unavailable.

**Blocked by:** 16 (complete).

**Status:** ready-for-agent.

**Design contract:**

- A daemon shutdown must not orphan a run supervised by another live process.
- A controller that loses a terminal append race must reconcile the durable
  terminal event instead of reporting an infrastructure error.
- Semantic-output format failures get one corrective retry; provider execution
  failures and valid semantic blockers do not retry.
- Persisted event and outcome formats remain additive and unchanged.

**Acceptance criteria:**

- [ ] Focused tests reproduce a daemon shutdown racing a controller terminal
      completion and prove the review result is terminal rather than unavailable.
- [x] Focused tests prove malformed semantic payloads get one corrective retry.
      TestShouldRetryFormatCubreTodoFalloDeFormato covers every class;
      TestReviewTransportRetriesEvidencePolicyFailureOnce and
      TestAuditarCommitRetriesFormatFailuresButNotToolDenial prove it end to end.
- [ ] Focused tests prove no daemon shutdown or reconciliation path fabricates a
      provider completion.
- [ ] Build, vet, focused tests, full tests, guardian, and independent review
      evidence are recorded before closure.

## Evidence

*(appended per slice)*

## Follow-ups

*(recorded at closure)*
