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
