# 07: Cut Over Evidence Admission

**What to build:** Agent output affects review or gate results only when its
durable invocation provenance and evidence bindings pass Sentinel admission.
Stale snapshots and mismatched lineage are rejected; admission failures are
recorded as first-class evidence instead of generic unavailable results;
rebase reuse keeps working only through existing blob identity rules.

**Blocked by:** 05 (complete, merged at d0f9a3f via R4) and 06 (complete,
merged at d0f9a3f).

**Status:** ready-for-agent

**Design contract (agreed analysis):**

- The durable transport returns raw output today whenever the projection says
  succeeded. Admission adds one verification before that output may influence
  semantics: hash(returned output) MUST equal the AttemptOutcome.OutputHash
  recorded for the terminal event of that exact run. Mismatch is an admission
  failure, never a silent accept.
- A new Evidence struct travels with accepted completions: run id, job id,
  invocation id, lineage id, outcome class, output hash. The engine's
  unavailable-reason text for any admission failure starts with the literal
  prefix `admission:` plus the concrete mismatch detail, so failures are
  evidence with provenance, not generic unavailability. Existing failure-text
  preservation rules stay untouched for non-admission outcomes.
- Snapshot freshness binds three identities at consumption time: the stored
  immutable request candidate embeds the audited SHA, the adapter was
  constructed with the same SHA, and the prompt hash matches the admitted
  request prompt. Any divergence rejects the output with an `admission:`
  reason naming which identity diverged.
- Findings produced by a dimension audit record their producing invocation
  identity alongside the existing content-stable fingerprint. Fingerprints,
  blob indexes, and append-only revision rules stay byte-compatible; the
  binding is additive metadata, not a format break. Rebase reuse keeps
  working exactly through the existing blob identity rules — reviewed
  content re-encountered under a different SHA is admitted through those
  rules without rerunning the provider.
- Strictness is construction-time and reversible: `review.evidence_admission`
  (default true) turns verification on; setting it false restores today's
  observe-but-admit behavior while controller-backed execution remains fully
  observable through `sentinel runs`. Historical records are never rewritten.
- Refutation, supersede/aggregation, effective-agent authorship recording,
  and the legacy append-only ledger keep their current behavior. Admission
  sits where transport output enters the engine; it never re-decides verdicts.

**Acceptance criteria:**

- [ ] Focused tests prove output whose returned bytes do not match the
      recorded OutputHash is rejected with an `admission:` reason before any
      verdict computation sees it.
- [ ] Focused tests prove stale snapshot rejection: a run whose stored
      candidate/SHA or prompt differs from the live audit context is refused
      with the diverging identity named.
- [ ] Focused tests prove findings carry the producing invocation identity
      additively while fingerprints stay identical for identical content.
- [ ] Tests prove rebase reuse: content already reviewed under another SHA is
      admitted through existing blob identity rules without provider calls.
- [ ] Tests prove `review.evidence_admission=false` restores lenient
      acceptance byte-for-byte while every run stays inspectable via
      `sentinel runs`.
- [ ] Tests prove non-admission failure text preservation is unchanged.
- [ ] Exit-code/error mapping in gate, review, and pr paths surfaces
      admission failures distinctly from infrastructure failures.
- [ ] The implementation records build, vet, tests, guardian, independent
      `code-review`, Judgment Day, rollback boundary, and follow-ups here.

**Out of scope:** Process-tree cancellation ownership (R7), startup scanning
and interrupted-run classification (R8), daemon/remote surfaces (D units),
rewriting historical ledger rows.

**Suggested slices:**

1. Evidence-bound completions inside the durable transport with hash
   verification and the Evidence struct.
2. Snapshot, lineage, and prompt binding validation with typed admission
   failures surfaced through engine reasons; additive finding binding.
3. Cutover wiring behind `review.evidence_admission`, migration parity
   extension, documentation, then Judgment Day over the whole unit.

## Evidence — slice 1 (evidence-bound completions)

- Commits: feat(reviewexec) verify durable evidence (+104), test(reviewexec)
  admission branches and parity (+206). Both staged candidates passed the
  pre-commit budget.
- Surface: execution.HashAdapterOutput exported as the single output-hashing
  authority (wraps the unchanged controller hash); reviewexec.Evidence
  {RunID, JobID, InvocationID, LineageID, Class, OutputHash} built from the
  verified durable record via shared evidenceFromOutcome; AdmissionError with
  literal "admission: " prefix; DurableTransport.Run now returns
  (output, Evidence, error) and refuses success unless ReadAttemptOutcomes
  yields a matching-invocation, success-class outcome whose OutputHash equals
  HashAdapterOutput(returned bytes).
- Independent code review (dual axis): spec PASS on all seven binding
  requirements; standards found triplicated Evidence-copy literals and a
  duplicated invocation lookup (fixed via evidenceFromOutcome + reuse in
  tests), a misleading variable name (fixed). Spec design notes applied:
  unreadable-outcomes branch demoted from AdmissionError to a wrapped
  infrastructure error so store outages cannot wear the admission label
  slice 2 will surface distinctly; empty-output admission pinned by an
  explicit test using a silent reviewer double.
- Deliberate omissions: snapshot/prompt binding, engine-reason surfacing,
  config flag, CLI (slices 2-3); divergence forced at the verifier boundary
  because ReadAttemptOutcomes prefers embedded terminal-frame evidence over
  the outcomes directory, making on-disk byte tampering inert.
- Verification: gofmt clean, build/vet OK, full suite green across 22
  packages.

## Evidence — slice 2a (snapshot/prompt binding + surfacing)

- Commits: feat(store) expose durable request records (+140 incl. focused
  execution_request_test.go split), feat(reviewexec) validate snapshot and
  prompt binding (+77), test(reviewexec) binding rejection and engine
  surfacing (+316). Every staged candidate under budget.
- Surface: store.ExecutionRequest exported rename (json tags byte-identical),
  ErrRequestCorrupt, Store.ReadExecutionRequest strict reader;
  DurableTransport.validateSnapshotBinding fail-fast after Start verifies
  lineage ownership, single audited-sha segment in the admitted candidate,
  candidate identity tie-back against the durable record, and prompt identity
  equality via the same PromptIdentity derivation the store persists.
- Review-driven fixes: misleading "candidate sha segment" reason text now
  names the identity tie-back honestly; throwaway request rebuild replaced by
  direct agentrun.PromptIdentity comparison; bindSnapshot renamed to
  validateSnapshotBinding (it mutates nothing); oversized execution_test.go
  no longer worsened — new reader tests live in their own file; mutex-copying
  silentReviewer double rewritten mutex-free.
- Deficiency surfaced by the binding-rejection test and fixed: a rejected
  binding used to leave the detached worker executing an untrusted provider
  call. Run now aborts the admitted run cooperatively on binding rejection so
  the durable record settles canceled with the rejection on record; the
  TempDir cleanup race disappeared with it (3x repeat green).
- Honest limits recorded: intra-call staleness is structurally unreachable
  because Run authors its own record microseconds earlier; the load-bearing
  checks target external tampering, damage, and transport misconfiguration.
  End-to-end sha-A-vs-sha-B divergence is unit-level only (Run mints fresh
  records); integration uses a malformed transport sha.
- Verification: gofmt clean, build/vet OK, full suite green across 22
  packages including 3x repeat of the previously racy rejection test.

## Evidence — slice 2b (additive finding binding + rebase reuse)

- Commits: feat(review) bind findings to invocation identity (+180 incl.
  focused invocation_binding_test.go), test(review) rebase reuse proof
  (+213, English-only after review fixes). Both staged candidates under
  budget.
- Surface: ReviewTransport contract now returns the producing invocation
  identity (legacy nil path returns empty); DimensionResult.InvocationID and
  Hallazgo.InvocationID are additive omitempty fields; stamparInvocacion
  stamps findings only when an identity exists, so legacy records keep their
  exact shape. Fingerprint(h) does not read the new field — pinned by
  TestFingerprintIgnoresInvocationID proving byte-identical fingerprints for
  findings differing only in provenance.
- Rebase-reuse proof is genuine: real git rebase rewriting three SHAs with
  unchanged content; second AnalizarRama pass reports zero pending commits,
  zero transport calls, zero direct reviewer calls (blob identity rules admit
  the reuse), and the adopted record still carries the ORIGINAL InvocationID.
  Persistence mechanism: ledger adoption copies the full record verbatim and
  fingerprint lookup ignores provenance metadata.
- Independent code review (dual axis): spec PASS on all five requirements
  with no leak paths into aggregation/supersede/refutation/renderer; JSON
  impact additive-only via omitempty. Standards found two hard violations in
  the new rebase test file — Spanish artifacts and a "/" path concatenation —
  both fixed (full English translation preserving legacy helper reuse;
  filepath.Join). Judgement calls recorded as follow-ups: transport closure
  body duplicated between production and migration-test double (hand-synced),
  ReviewTransport's adjacent unnamed string returns invite a named result
  struct at the next signature evolution.
- Deliberate omissions: aggregated findings do not merge invocation identities
  across dimensions (per-dimension binding is what persists per ticket scope);
  engine.go/finding.go remain pre-existing >500-line files with proportional
  growth only (+28/+11).
- Verification: gofmt clean, build/vet OK, full suite green across 22
  packages.

## Evidence — slice 3 (cutover wiring + surfacing)

- Commits: feat(config) evidence_admission default-on (+45/-5), feat(reviewexec)
  lenient seam behind WithEvidenceAdmission (+285 incl. cutover tests and
  parity extension), feat(gate) admission-vs-infrastructure surfacing
  (+92/-15 across gate/pr/docs), test(gate) surfacing contracts (+203).
  Every staged candidate under budget.
- Surface: ReviewConfig.EvidenceAdmission yaml `evidence_admission` defaulting
  true; WithEvidenceAdmission construction-time option (zero value strict, so
  accidental non-wiring fails closed); lenient mode skips binding+evidence
  verification and returns zero Evidence — byte-identical to pre-R6 behavior,
  verified line-by-line by the spec axis against HEAD. Gate unavailable
  dimensions gain `class=admission|infrastructure` labels via typed-error-first
  failureClass; pr review gains review_admission_failures /
  review_infrastructure_failures JSON keys plus a text notice. Documented
  exit-code contracts untouched and pinned.
- Independent code review (dual axis): spec PASS on all six requirements with
  byte-parity confirmed against the pre-R6 baseline; standards zero hard
  violations. Judgement calls recorded as follow-ups: third hand-synced copy
  of the transport closure in migration tests (extract a shared builder next
  time the signature evolves), IsAdmissionReason stays a documented stringly
  seam because append-only ledger rows carry no typed errors, comandos_pr.go
  keeps accreting (pre-existing oversize). Drive-by acknowledged: the
  retry-live CLI test now drains the relaunched attempt before TempDir removal,
  fixing a cleanup race the new parity run exposed.
- Verification: gofmt clean, build/vet OK, full suite green across 22
  packages including 3x repeat of the drained race test.
