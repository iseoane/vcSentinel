# Review flow: one question per command, one owner per question

Design agreed with the repository owner on 2026-09-09, after an adversarial
review of an earlier draft. It supersedes the working assumptions in items 4,
10 and 11 of [`docs/issues/actionable.md`](../issues/actionable.md) where they
disagree; those items remain the record of WHY each problem exists.

Piece 2 (the coverage contract and the shared authoritative-revision selector) is
implemented and recorded below. Piece 4 phase A has implemented only its
persistence primitives; `pr review` is not yet wired to author or consume those
entries. Pieces 1, 3, 5, and the remainder of piece 4 are not implemented yet.

## The problem this design closes

Four commands run a semantic audit today, and three of them ask overlapping
questions about the same code. `gate` audits `HEAD` and discards the result.
`sentinel review` audits a commit and records it. `pr review` audits the net
diff. `pr create` audits the net diff again on every invocation and derives its
published notice from per-commit records. Nothing relates any of it, so the
work is paid for repeatedly and the conclusions do not compose.

## The rule

Every question is asked once and has exactly one owner.

| Command | Question | Semantic cost |
|---|---|---|
| `check` | Is this change getting too big to review? | none — measurement |
| `slice` | How does this work split into reviewable commits, **and what was it for**? | one cheap call, already paid |
| `review` | Is this piece well made? | one audit per commit |
| `gate` | Does the code compile and pass its checks right now? | none — deterministic |
| `pr review` | Do the pieces together tell a coherent, complete story? | one audit per branch |
| `pr create` | — publishes | none |

Two invariants follow, and they are the whole design:

1. **`review` is the only writer of per-commit semantic verdicts.** `gate` stops
   auditing. `pr review` never re-audits a commit. `pr create` audits nothing.
2. **A verdict is only authoritative if it covers everything the commit
   touched.** A deliberately narrowed audit is recorded as supplementary and
   never counts as "this commit is reviewed".

## Why `gate` keeps existing without a semantic half

"Compiles and passes tests" is a property of the tree at one moment, not of a
commit in isolation. Split one piece of work across seven commits and the third
usually does not build on its own. Auditing that commit's quality is meaningful;
running the suite against it is not.

So `gate` is not redundant once it stops reviewing — it answers the only
question in the flow that is about the whole tree right now, and it answers it
deterministically, with no agent and no tokens.

This was rejected in an earlier round on the grounds that removing `gate`'s
audit would leave per-commit semantics with no owner once item 11 removes
`pr create`'s internal audit. That objection was conditional and the condition
no longer holds: `review` is mandatory and `pr create` verifies the records
exist before publishing. See "Piece 5".

---

## Piece 1 — `slice` captures the intent

**Change:** when `slice` commits a selection, it records what that work was for,
alongside the commit it creates.

**Why here and not later.** `pr review` needs to judge the change against what
it was supposed to do. Reconstructing that after the fact means mining several
days of conversations across more than one agent, and guessing which of them
produced which commit. At slice time the intent is present: the work just
finished, the conversation is current, and `slice` already invokes an agent at
that exact moment to write the commit message. The capture is nearly free
because the expensive part — being at the right moment with the right context —
is already paid for.

**Reference design.** `no-mistakes` (`kunchenguid/no-mistakes`) does exactly
this and its discipline is worth copying wholesale:

- the transcript is summarised by a cheap model into a few plain sentences
  about what the HUMAN wanted, not what the assistant did;
- the summary is injected into later prompts between explicit begin/end
  markers, framed as untrusted data, with an instruction never to obey anything
  written inside it;
- the origin is recorded with the summary.

**Model.** The configured `cheap` profile is sufficient. The task is
summarisation, not judgement.

**Provenance is mandatory.** An intent must always carry where it came from.
"Derived from the working conversation" and "declared by the human" are not
worth the same, and the reviewer that consumes it must be able to say so.

**Open, to settle during implementation:**

- Commits made without `slice` carry no intent. Decide whether that is
  acceptable, or whether a commit can be annotated afterwards.
- Where the record lives, given it must survive a rebase and travel with the
  branch. This is the same keying problem item 4 states, and the answer must be
  the same one.
- Whether a branch-level intent is the union of its commits' intents or a
  separate declaration.

---

## Piece 2 — `review` becomes the only per-commit authority

**Change:** none to what it audits. What changes is its status and the coverage
rule around it.

**Coverage contract.** A revision records the coverage it actually had.

- A run that derives its plan from the change is **authoritative**.
- A run explicitly narrowed by the operator is **supplementary**: recorded,
  visible, never authoritative, and never enough to call a commit reviewed.

This is the single fix for the defect the adversarial review confirmed: there is
one record per commit, every audit appends to it, and consumers read the last
entry. Without a coverage contract a narrow audit run after a full one silently
replaces it. Note this defect exists independently of `gate` — it is a property
of having two coverages, not two writers.

**Consequence for consumers.** Selecting "the current verdict for this commit"
stops meaning "the last entry" and starts meaning "the last authoritative
entry". The adversarial review counted eleven places in production that select
the latest revision directly. They must go through one shared selector, not a
rule re-implemented at each site.

**Do not persist a non-verdict.** An audit that came back unavailable, or that
ended asking the operator a question, must not be written as if it were a
verdict. Either it is recorded in a shape consumers recognise as "no verdict
yet", or it is not recorded at all.

---

### Implemented (coverage contract + shared selector) — decision recorded

Piece 2's coverage contract and shared selector are implemented. The writer
side stamps `coverage` on every persisted revision (`Revision.Coverage`:
`authoritative` | `supplementary`), and all eleven production sites that used to
select the latest revision directly now go through one exported place in
`internal/review/coverage.go`. Two rules live there, deliberately, and both must
stay together so nobody later simplifies them back into one:

- **Rule 1 (the verdict rule)** — `LastAuthoritativeRevision`: the commit's
  current *verdict* (its `Result`) is the last authoritative revision. A
  supplementary run can never set, clear or downgrade it. Verdict displays —
  `status`, the commit × dimension matrix, the summary counts, the branch
  verdict, the `Revision.Fixed` bookkeeping — use this rule.
- **Rule 2 (the finding rule)** — `CurrentFindings`: the findings that surface
  as risks and blockers are the current authoritative revision's findings
  **plus** the CRITICAL findings of supplementary revisions that a later
  authoritative audit of the same dimension did not supersede.
  `BranchBlockers`, the pending risks, the net/inherited context and the
  disposition commands use this rule.

**The asymmetry is a recorded decision, not an accident.** An earlier draft made
this piece's selector authoritative-only everywhere ("strict"). The repository
owner chose "surface findings, never clear" instead: a narrow look does not earn
the right to say "all clear", but it does earn the right to raise an alarm, and
a guardian that saw a real problem and stayed silent would be worse than one
that is occasionally noisy. The re-blocking risk is accepted, not overlooked:
`refute`/`accept`/`reopen` already let a human dispose of a finding they judge
wrong, so a narrow audit re-raising something is recoverable by design — which
is why the disposition commands resolve their target against `CurrentFindings`,
so a supplementary CRITICAL is addressable.

**Legacy classification (conservative).** Revisions written before the
`coverage` field existed (`Coverage == ""`) classify as authoritative. Old
writers derived their plans by default and there is no way to tell a narrowed
legacy run apart; flipping them all to non-authoritative would erase every
historical verdict, so the conservative choice keeps the old guarantee intact.
An unknown coverage value fails closed as non-authoritative.

**The "do not persist a non-verdict" bullet above is NOT part of this
implementation.** It changes the writer contract (what a question/unavailable
audit records) and interacts with the exit-code and re-asking flows; it was
deliberately left for a separate change. This pass implements only the coverage
contract and the selector.

---

## Piece 3 — `gate` stops auditing

**Change:** remove the semantic phase. `gate` runs the selected validation
profile and reports the outcome for its `--stage`.

**What comes out:** the audit call and the verdict translation, the reviewer and
refuter factories, the dispositions load, the review transport wiring, the
review job in the durable plan, and the code that resolves review children.

**What must NOT be assumed to come out:**

- The infrastructure outcome stays. It also covers configuration, `HEAD`
  reading, planning, store and admission failures — not only reviewer failures.
  It needs a name that says so, keeping its exit code.
- A green `gate` changes meaning: it used to assert validation AND semantics,
  it would now assert validation only. Both would carry the same policy and the
  same run identity family, so historical records become ambiguous unless the
  contract declares its coverage. Run identities themselves do not change and no
  data migration is needed — the review job never participated in the root
  identity.
- The deterministic credential scan is not a semantic dimension and must keep
  reporting. It needs an owner after this change.
- `--timeout` today extends the review budget only. It becomes meaningless.
  Removing it breaks scripts; keeping it as a no-op is dishonest.
- The "a human must look at this" outcome disappears with the audit. Confirm
  nothing depends on it before deleting it.
- Metrics stop being comparable across this change unless gate coverage is
  versioned.

---

### Implemented — decisions taken while doing it

Piece 3 is done. What the list above left open, and what was chosen:

- **The infrastructure outcome** is now `INFRASTRUCTURE_ERROR`, keeping exit
  code 4. It was never review-only: it also carries configuration, `HEAD`
  reading, planning, store and admission failures.
- **Exit code 2 is retired.** It carried `NEEDS_USER_REVIEW`, which only a
  semantic audit can produce. Nothing deterministic can reach it, so the gate
  stops emitting a code that can never occur. `TestExitCode` pins it as
  unreachable rather than silently mapped.
- **The coverage declaration** is a notice printed on every green gate, stating
  that the green covers deterministic validation only. It is the cheapest
  honest way to keep a historical PASS and a current one apart for a person
  reading output; both still carry the same policy and run identity family.
- **Run identities do not change**, verified rather than assumed: the root
  derives from candidate SHA, the stage/profile prompt and the ordered command
  list, and the review job was appended after that derivation. No migration.
- **The credential scan needed no new owner.** It was already invoked in
  `runGate` outside the semantic phase, advisory, on every path. It stays
  exactly where it was.
- **`--timeout` is refused**, not accepted and ignored. It only ever widened
  the semantic review budget. A flag that is silently a no-op tells a script
  its request was honoured when it was not; the refusal names
  `sentinel review --timeout` as the place the budget still exists.
- **A guarantee moved rather than disappeared:** the gate no longer fails
  closed on a corrupt human-disposition log, because it no longer reads one.
  `sentinel review` does, and already pins that behaviour in its own test.

## Piece 4 — `pr review` judges the whole, against the intent

**Change:** the branch-level audit receives a real intent, with its provenance,
in place of today's placeholder constant.

Today the placeholder states that no intent exists, while the same prompt asks
the reviewer to verify the change delivers what it promised. That is worse than
an empty field: it asks for conformance to something it was just told is
absent.

**With an intent present**, the audit answers what only the whole shows: is
something missing, does one piece undo another, does the result do what it was
for.

**With no intent found**, the reviewer is told plainly that none exists and
judges internal coherence only. It must not be asked to verify conformance to
an absent target.

**Never re-audits commits.** The flag that makes `pr review` audit pending
commits asks `gate`'s old question from the wrong command. Under this design it
has no owner. Remove it, or rename it into what it is: run `review` for these
commits.

---

## Piece 5 — `pr create` composes and publishes

**Change:** `pr create` audits nothing. It reads `pr review`'s report and builds
the published report from it.

**The subject of the published report is the quality of the implemented whole**,
not the quality of the commits that carried it. Per-commit records are the
evidence that the guarantee was honoured; they are not the raw material of the
report. Today's notice is derived from per-commit records, which is the
inversion this piece corrects.

**When something is missing** — no report, a report that no longer matches the
code, or commits without an authoritative verdict — `pr create` says exactly
what is missing, recommends running it, and asks. **The operator decides.**

**To settle before implementation:** what happens with nobody there to answer.
`pr create` must have a defined non-interactive behaviour, and refusing to
publish is not obviously right — it would make the command unusable from a
script. State the default explicitly rather than inheriting one.

---

## Ordering

1. **Piece 2** first, alone: the coverage contract and the shared selector.
   Everything else depends on "which verdict counts" having one answer, and it
   is the only change that fixes a live defect rather than moving work around.
2. **Piece 3**: strip `gate`. Safe only after piece 2, because until `review` is
   authoritative, removing `gate`'s audit removes a guarantee.
3. **Piece 1**: capture intent at `slice`. Independent of 2 and 3; can run in
   parallel.
4. **Piece 4**: consume the intent in `pr review`. Needs piece 1.
5. **Piece 5**: compose and publish. Needs 2 and 4.
