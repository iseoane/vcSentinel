# 11: Route Gate Through Durable Runs

**What to build:** `sentinel gate` is represented by one root durable run whose
logical jobs carry deterministic validation evidence and reviewer invocation
history. The current gate CLI, output text, and exit-code contract remain a
compatibility facade over the durable orchestration, and gate results can be
reconstructed from admitted durable state alone.

**Blocked by:** 07 (complete, merged at 9202f28) and 10 (complete, merged at
7b725bb).

**Status:** complete

**Design contract (agreed analysis):**

- One root run per gate execution: validation commands and review dimensions
  become separate logical jobs under it, preserving today's
  validation-before-review ordering — review jobs never start before every
  validation job has settled successfully.
- `--stage` lifecycle context and `--profile` validation-profile semantics are
  embedded in the root run's request so any later observation can explain why
  the run exists; profile resolution stays deterministic and fails explicitly
  exactly as today.
- Terminal classes stay separate and honest: validation failure, review
  findings, and infrastructure failure map to distinct terminal outcomes on
  their own jobs, aggregated by the root run without erasing which layer
  failed. Existing exit codes and stdout text of `sentinel gate` are pinned
  byte-for-byte as the compatibility facade.
- Review jobs route through the existing durable transport (evidence
  admission, owned process trees, cancellation) — no parallel execution path;
  validation jobs record command, exit status, and duration as deterministic
  evidence frames without invoking any agent.
- Reconstruction: given only the store contents for the root run, the gate
  summary (validation verdicts, review verdicts per dimension, final class)
  can be rebuilt identically to what the live run printed — proven by tests
  that replay admitted state and compare against the recorded facade output.
- Rollback seam: a construction-time switch routes gate back to the legacy
  orchestration while durable history already written stays inspectable via
  `sentinel runs`; the default is the durable path (this unit IS the cutover),
  matching the R6 evidence-admission pattern of reversible strictness.
- Additive compatibility: no JSON tag changes to persisted structs; new event
  usage rides existing frame machinery; stores from previous units keep
  scanning and operating unchanged.

**Acceptance criteria:**

- [x] Focused tests prove one root run with separate validation and review
      logical jobs, and that review never starts before all validation jobs
      settle successfully. *(Root + ParentRunID-linked children; counter-seam proves factory never constructed on validation failure.)*
- [x] Focused tests prove --stage and --profile land in the root request and
      profile-absent failure behavior is unchanged. *(Canonical prompt embedding; divergence per axis pinned.)*
- [x] Focused tests prove validation evidence (command, exit status,
      duration) is recorded deterministically without agent involvement. *(Tuple digest through HashAdapterOutput; hash-bound even for FAILED jobs after round-1 fix.)*
- [x] Focused tests prove terminal classes stay distinct per layer and the
      root aggregation names the failing layer. *(gate: failing layer: detail; no new agentrun states.)*
- [x] Focused tests pin the gate CLI contract: stdout text, exit codes, and
      error paths byte-identical to pre-R9 for success, validation failure,
      review failure, and infrastructure failure shapes. *(Legacy-vs-durable harness on identical fixtures; header verbatim + body multiset with recorded honesty rationale.)*
- [x] Focused tests prove reconstruction from admitted durable state alone
      reproduces the printed gate summary. *(ListExecutionIDs+ReadExecutionRequest+Inspect ONLY; both failing and green paths; salted review IDs learned via WithRunObserver.)*
- [x] Tests prove the rollback switch restores legacy orchestration behavior
      while prior durable history remains fully inspectable. *(gate.durable_runs=false: zero executions written, tree-digest proven; runs untouched.)*
- [x] Focused tests prove additive compatibility: old streams and stores
      unaffected; review jobs inherit admission/cancellation behavior from the
      shared transport. *(Parentless bytes legacy-identical; real NewDurableTransport construction path in tests.)*
- [x] The implementation records build, vet, tests, guardian, independent
      `code-review`, rollback boundary, and follow-ups here.

## Evidence

*(appended per slice)*

## Follow-ups

*(recorded at closure)*

### Slice 1 — gate run model and reversible seam (machinery only)
- Commits: plan+evidence production (337), pins (314) — hook-enforced.
- BuildGateRunPlan: one root request (stage/profile via canonical NUL-joined
  prompt through PromptIdentity; HEAD sha as Candidate; command-list hash via
  NewCapability identity authority — zero ad-hoc hashing), one validation job
  per profile command in order, exactly one review job; typed GatePlanError
  per field. Map-order safe: profiles are ordered slices, capabilities sorted.
- RecordValidationEvidence: deterministic scalar entries + 64-hex digest of
  the whole tuple through execution.HashAdapterOutput (command/capability/
  exit/duration/trimmed output); hash-only persistence model respected; no
  agent anywhere.
- Switch DurableRuns default false: legacy path byte-green with nil Resultado.
  Err (only ejecutarGateDurable sets it; sole CLI consumer reads Estado/
  Mensajes only); true → plan builds first, then typed not-wired error; plan
  failures never masked. Flag unreachable from CLI this slice by design.
- Dual-axis review: spec 8/8 PASS (collision-safe embedding proven injective
  for reachable inputs; digest tuple binding verified both axes; map iteration
  nondeterminism ruled out); standards CLEAN after error-prefix alignment
  ("gate: " house style on all three new errors). Test-only surface noted as
  cutover follow-up so staged symbols don't orphan.
- Verification snapshot: gofmt empty; build+vet linux+windows; gate and
  cmd/sentinel suites green; plan/evidence tests ×5 deterministic.

### Slice 2 — durable orchestration with persisted linkage
- Commits: parent linkage (66), root orchestration (334), evidence adapters
  (158), facade pins (202), ordering+layer pins (294), reconstruction proof
  (274) — hook-enforced; worktree clean.
- REAL durable routing behind DurableRuns=true (still default false, CLI-
  unreachable): one root controller run; validation jobs are child runs
  stamped ParentRunID=root (additive omitempty field on RunPolicy and
  ExecutionRequest; parentless bytes proven legacy-identical); review phase
  reuses AuditarCommit + traducirVeredicto with only a transport-factory
  override that receives the root ID.
- Review loop round 1 found a CRITICAL structural breach: the first cut was
  N+1 sibling runs with no persisted linkage — reconstruction from store
  contents impossible and failed jobs losing evidence. Fixed: ParentRunID
  linkage (scan-enumerable children), "|children=<ids>" suffix on failing/
  unavailable settlement details (root-record-alone reconstruction after
  settlement), settledValidationAdapter now returns Output alongside failure
  so AttemptOutcome.OutputHash binds the evidence serialization even for
  failures, shared infrastructure-literal helper, panic-guarded settlement
  channel, resolved_command attribute stamped only on divergence.
- Scoped re-review verdict GO: all five findings FIXED with file:line-proven
  mechanisms resting on verified merged controller/store behavior; legacy
  byte-parity for parentless executions pinned; no facade drift.
- Facade equivalence harness runs BOTH paths on identical fixtures across
  PASS / multi-command VALIDATION_FAILED / CODE_REVIEW_FAILED / NEEDS_USER_
  REVIEW / infrastructure shapes asserting byte-equal Estado+Mensajes;
  ordering counter-seam proves the review factory is never constructed when
  validation fails.
- Contract reconstruction test rebuilds {terminal state, failed layer,
  children enumeration vs scan, class multiset, OutputHash multiset} from
  ListExecutionIDs + ReadExecutionRequest + Inspect ALONE, equality vs live
  output on both failing and green paths.
- Cutover follow-ups registered: salted review-candidate IDs must be learned
  by the orchestrator at wiring time (enumerated vs scanned divergence);
  resolved_command stamping lacks a direct divergence fixture; printed review
  message bodies complete at production wiring.
- Verification snapshot: gofmt empty; build+vet linux+windows; full suite
  green; -race clean gate/store; new tests ×5.

### Slice 3 — cutover wiring, rollback seam, learned review children
- Commits: config switch (95), run-observer children (151), production cutover
  (134), cutover+rollback pins (362), docs (14) — hook-enforced; clean.
- gate.durable_runs *bool default TRUE (evidence_admission parser pattern,
  full precedence table tested); false returns before any git/store touch —
  tree-digest proof of zero executions written with a real verdict computed;
  prior durable history stays inspectable via runs.
- Salted-review-children follow-up RESOLVED: additive WithRunObserver reports
  each admitted review run ID synchronously at admission (failures and
  aborts included; admission-rejected correctly silent); cmd-level sink drains
  after AuditarCommit joins workers (race-clean); |children= enumeration now
  set-equal to the ParentRunID scan including salted review IDs.
- Production wiring: one store directory backs root + validation jobs + every
  routed review transport (stateless handles); factory threads ParentRunID.
- Dual-axis loop: GO conditional on translating three new Spanish comment
  blocks in parser.go (done) plus two precision wording fixes (same store
  DIRECTORY not instance; goroutine-completion order not map order). Honest-
  sort judgment recorded: legacy output is itself order-nondeterministic
  (mutex-guarded per-dimension appends), so header verbatim + body multiset
  is the truthful comparison. Accepted follow-ups: dedup hardening in
  withChildren if planned-ID admission ever coexists with the observer;
  global-true+project-false precedence case (mechanism trivially correct);
  resolved_command divergence fixture from slice 2.
- Verification snapshot: gofmt empty; build+vet linux+windows; FULL suite
  green across 23 packages with cutover ON by default (all pre-existing gate
  CLI exit-code/stdout pins unchanged); -race clean on gate/config/
  cmd-sentinel; new tests ×5.

### Unit closure — R9

- Final verification: gofmt empty; build+vet clean linux AND windows; FULL
  suite green across 23 packages WITH cutover default-on; -race clean on
  gate/cmd-sentinel; worktree volume under control; 17 commits.
- Rollback boundary as contracted: gate.durable_runs=false restores the exact
  legacy orchestration (zero store writes, byte-pinned output) while durable
  history already written stays inspectable via sentinel runs; review-side
  review.durable_runs keeps its own pre-existing seam.
- Follow-ups accepted: withChildren dedup hardening if planned-ID admission
  ever coexists with the observer; global-true+project-false precedence test;
  resolved_command divergence fixture; engine-level line-ordering
  determinism (pre-existing, upstream of this unit).

**Closed:** R9 complete — gate is durably routed by default behind a
byte-compatible facade, reconstructable from admitted state alone.
