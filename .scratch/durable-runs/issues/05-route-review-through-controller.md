# 05: Route Review Execution Through The Controller

**What to build:** Make the review scheduler execute every dimension through
the durable controller while preserving existing review results, ledger
compatibility, configured parallelism and timeouts, and truthful effective-agent
authorship.

**Blocked by:** 04: Add the Execution Controller (complete; foundation merged to
`main` at b0b71cc).

**Status:** ready-for-agent

**Design contract (agreed analysis):**

- The seam sits where `auditarConAgente` invokes the agent. Implement an
  `execution.Adapter` whose `Execute` calls the existing restricted reviewer
  path (`EjecutarRevision`) including the effective-agent observation wrapper;
  the H4 lesson (record who answered after success) must survive the migration.
- One logical job per review dimension. Every transport retry
  (`ejecutarConReintento`) and every chain fallback (`CadenaAdaptador`)
  becomes a distinct physical invocation recorded within that logical job.
- The engine keeps bundle scheduling, budget gating, refuter pass,
  supersede/aggregation, and verdict computation unchanged. `AuditarCommit`
  keeps its signature; gate, branch analysis, and PR command callers are
  unaffected. The append-only review ledger and content-stable finding
  fingerprints stay exactly where they are.
- Scheduler parallelism stays as the admission gate *before* `Start` so the
  controller never launches more concurrent provider processes than the
  configured limit. Configured timeouts travel in the invocation envelope or
  run policy so a provider timeout surfaces as the controller timeout terminal
  class instead of an unclassified error.
- The clarification round (question verdict plus user answers) uses the
  controller's awaiting-decision state and linked respond action so response
  lineage stays inspectable.
- Translation rule: success parses output exactly as today; failure,
  cancellation, timeout, and unavailable terminal classes become dimension
  unavailable results whose reason preserves the concrete provider failure
  text from the adapter error. No provider failure may degrade into a generic
  message.
- Two adapters exist at the seam: the legacy direct call and the
  controller-backed job. Selection happens at construction time; that choice
  is the rollback mechanism.

- [ ] Focused tests prove one logical job per dimension with retries and fallbacks recorded as separate physical invocations.
- [ ] Migration tests compare legacy and controller-backed outcomes on identical fake adapters and require equal verdicts, findings, and reasons.
- [ ] Tests prove concrete provider failure text survives terminal-class translation into dimension results.
- [ ] Tests prove effective-agent recording still attributes each dimension to the adapter that actually answered.
- [ ] Tests prove parallelism and budget behavior are unchanged versus the legacy scheduler.
- [ ] The implementation records build, vet, full tests, guardian, independent `code-review`, Judgment Day, rollback boundary, and follow-ups here.

**Out of scope:** Operator commands (R5), evidence admission cutover (R6),
cancellation and process-tree ownership (R7), recovery (R8), the gate path
(R9), daemon/TUI work, ACP/acpx adapters, and any change to review prompts,
finding fingerprints, or the ledger format.

**Rollback boundary:** Remove only the controller-backed review execution
adapter and its tests; selection falls back to the legacy scheduler at
construction time. Durable runs already written remain readable, and the
ledger, store, and controller packages stay intact.

## Evidence — slice 1 (adapter seam)

- Commit: be4855f feat(reviewexec): route restricted reviewers through the execution controller.
- Scope delivered: `internal/reviewexec` package only — ReviewAdapter over a
  locally declared RestrictedReviewer (no import of internal/review),
  DefaultClassifier mapping context errors, concrete provider failure text
  preserved end-to-end through Completion and durable outcomes, effective-agent
  observation proven with answerer identity attribution.
- Verification: gofmt clean; go build OK; go vet OK; go test ./... all green;
  guardian 279 authored lines [PUNTO_OPTIMO]; pre-commit hook passed.
- Independent review (code-review skill, parallel Standards+Spec axes): both
  APPROVE-WITH-FINDINGS. Applied fixes: durable Inspect evidence for the
  unavailable path; answerer identity attribution in the observation test;
  extracted startAndWait scaffold; naming waiver comment for EjecutarRevision.
- Accepted follow-ups: continuation-response append kept as documented
  placeholder until engine-level question-round wiring; retries/fallbacks as
  separate physical invocations deferred to the engine-wiring slices;
  Judgment Day pending at milestone closure per the unit-skill matrix.

## Evidence — slice 2 (engine routing seam)

- Scope delivered: `DurableTransport` in reviewexec (one physical invocation
  per Run call over a shared store; TerminalError preserving concrete provider
  text and outcome class; nanosecond salt against candidate collision across
  repeated audits) plus the optional `ReviewTransport` field on
  OpcionesAuditoria routing both the first reviewer call and the clarification
  round through it. Nil field keeps the legacy path byte-equivalent; parsing
  stays shared after either path.
- Verification: gofmt clean; go build OK; go vet OK; go test ./... all green;
  guardian 201 authored lines [PUNTO_OPTIMO].
- Independent review (code-review skill, parallel axes): Spec
  APPROVE-WITH-FINDINGS (0 blockers); Standards APPROVE-WITH-FINDINGS.
  Applied fixes: new Spanish identifiers renamed to English (invokeReview,
  transport, calls).
- MAJOR accepted finding — staged durability model: the roadmap phrase "one
  logical job per review dimension with retries recorded within that job" is
  temporarily implemented as one durable RUN per invocation keyed by
  bundle/dimension/salt, because R3 offers no multi-invocation primitive short
  of awaiting_decision/respond, which is semantic question lineage and must
  not be abused for retries. Grouping invocations under one logical job
  requires an R3 contract extension (attempt-level events or retry decision
  support) and lands before R5 exposes operator commands. Recorded here as
  the binding follow-up.
- Accepted minor follow-ups: configured timeouts still ride inside
  CLIAdapter's per-call timeout (terminal classes already correct); engine-
  level migration-comparison, parallelism-parity, and durable-path attribution
  tests land with production wiring; identityKey becomes a typed key when
  cmd-side composition defines its final shape.
