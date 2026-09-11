# Piece 4 — `pr review` authors the judgement and persists it

Implementation plan for item 10 of [`docs/issues/actionable.md`](../issues/actionable.md).
Governing text: [`docs/design/review-flow-ownership.md`](review-flow-ownership.md).
Depends on piece 1 ([`piece-1-slice-intent.md`](piece-1-slice-intent.md)) for the
intent and its provenance.

This document is the specification. Where it disagrees with item 10, this
document wins.

## 1. The question this command owns

**Do the pieces tell a coherent, complete story?**

It does not re-audit the individual commits: `sentinel review` owns that and is
the only writer of per-commit verdicts. `pr review` reads those verdicts and
looks at what is only visible with every piece together — whether something is
missing, whether one piece undoes another, whether the result does what the
intent said it would.

Two changes of substance, and everything else in this plan serves them:

1. It stops guessing the intent. It reads what piece 1 recorded.
2. It stops being a report printed to a terminal and becomes a **persisted
   entry** that `pr create` consumes. Today `pr create` re-derives its own view;
   after this piece it composes from what `pr review` wrote and authors nothing.

## 2. The PR body this produces

The reference is the body shape used by `kunchenguid/firstmate`, reviewed on
2026-09-09 across PRs 4073, 4075, 4077, 4083 and 4087. It is invariant across
all five, so it is hardcoded here, not made configurable.

Five sections, always present, always in this order:

```
## Intent
## What Changed
## Risk Assessment
## Testing
## Pipeline
```

`pr review` authors sections 1 to 4 and seals the Pipeline attestation.
`pr create` renders them and adds nothing (piece 5).

### 2.1 Intent

From piece 1, and **the provenance is rendered, never dropped**:

- **Deduplicate by the `(text, source)` pair, never by text alone.** Two
  commits carrying the same sentence, one `declared` and one `conversation`,
  are two different claims: one a human made and one a model inferred.
  Collapsing them to a single line destroys exactly the distinction this
  section exists to preserve. Render both, in commit order.
- Every commit carries the same pair ⇒ render that one line.
- The pairs differ across commits ⇒ render each distinct pair as a bullet, in
  commit order. This is the branch intent: the union of its commits' intents,
  as decided in piece 1.
- **Some commits carry a trailer and others do not** — the normal case, because
  piece 1 accepts commits made outside `slice`. Render the intents that exist,
  then one line naming the gap:
  `_<n> of <m> commits carry no recorded intent: <short shas>._`
  Never omit it silently. The Risk Assessment is measured against the intent,
  and a reader must know the intent covers only part of the branch.
- No commit carries a trailer ⇒ render exactly:
  `_No intent recorded. These commits were not created through `sentinel slice`._`
  Do not summarise the diff to fill the gap. An invented intent is worse than
  an absent one, because the Risk Assessment below is measured against it.

Each rendered line carries its source, in this shape:

```
- stop the gate and review from both claiming per-commit authority _(declared by the human)_
- recover the durable store wiring the gate needs _(derived from the working conversation)_
```

Mixed sources on one branch are normal and must render correctly.

`HonestNetIntention` (`internal/app/pr/review.go:20`) is deleted. It exists
because nothing recorded the intent; piece 1 records it. Remove the constant,
its alias `honestNetIntention` (`cmd/sentinel/pr_command.go:30`), and both call
sites (`internal/app/pr/review.go:148`, `internal/app/pr/create.go:248`).
`NetReviewOptions.Intention` now carries the real intent text, or the empty
string when none was recorded — and an empty `Intention` must reach the
reviewer prompt as an explicit "no intent was recorded", never as an absent
field the model fills in.

### 2.2 What Changed

Three to six bullets describing **behaviour**, not files. This is a new
semantic output: `internal/change` profiles a change structurally and does not
describe it. One agent call, on the branch's net diff, in the same pass that
already produces the overview.

Reuse the existing `OverviewResult` seam (`internal/review/branch.go:119`)
rather than adding a second agent round trip: extend it with the bullets.

```go
type OverviewResult struct {
    Coherent  bool     `json:"coherent"`
    Rationale string   `json:"rationale"`
    Changed   []string `json:"changed"`   // behaviour bullets, 3–6
    Risk      string   `json:"risk"`      // one justified sentence, see 2.3
}
```

Empty `Changed` renders `_Not summarised: the overview was unavailable._` and
does not block. The existing `OverviewError` path already covers why.

### 2.3 Risk Assessment

One line: a verdict, then the justification, then the blockers if any.

```
✅ Low — narrowly scoped, satisfies the recorded intent, and every prior block
was retired by a later commit that was re-audited.
```

The verdict comes from `BranchResult` and the existing blocker projection
(`BranchBlockers` with the standing human dispositions), which already applies
those dispositions.
The justification sentence is `OverviewResult.Risk`, authored in the same
call as 2.2, and it is untrusted model output like any other: pass it through
`sanitizeText` before interpolating it. It is rendered on the same line as the
computed verdict, so an embedded newline in it would forge the section. **The verdict is computed, the sentence is authored.** A model
sentence must never be able to change the verdict; assert this with a test
that pairs a `block` verdict with a reassuring `Risk` sentence and checks the
rendered verdict is still a block.

When there are blockers, list them under the line with the existing merged
finding rendering (`renderMergedFinding`), which already sanitises untrusted
model output for Markdown (`sanitizeEvidence`, `sanitizeText`).

### 2.4 Testing

Prose summary, then the deterministic evidence Sentinel actually has:
`lint_commands`, `test_commands`, `build_commands` with their real exit codes.

`TemplateVerification` (`internal/review/renderer.go:180`) already carries
exactly this and already refuses to invent a PASS. Keep that type. What
changes is only the rendering: today `Validation` and `Verification` are two
sections with near-identical names and identical formatting that a reader
cannot tell apart. Merge them into `## Testing` with their origin stated on
each group:

```
Before the review:
- ✅ `go vet ./...` (exit 0)
After the review:
- ✅ `go test ./...` (exit 0)
```

Do **not** copy the reference's live-vs-deterministic scenario table. It
presupposes a classification Sentinel does not have and would be a claim
without evidence behind it.

### 2.5 Pipeline

The audit trail: which steps ran, and what each found. Steps, fixed order:

| Step | Source of truth | Status when it did not run |
|---|---|---|
| `slice` | intent trailers present on the range | ⚪ not observed |
| `review` | the ledger records for each SHA | ⚪ no records |
| `gate` | the deterministic validation exit codes | ⚪ not run |
| `lint` | `lint_commands` exit codes | ⚪ not configured |
| `test` | `test_commands` exit codes | ⚪ not configured |
| `build` | `build_commands` exit codes | ⚪ not configured |
| `pr review` | this run | always ✅ by construction |
| `ci` | filled by `pr create` (piece 5) | ⚪ not observed by Sentinel |

Each step renders as a collapsed `<details>` whose summary line encodes the
outcome, so seven states read without expanding anything:

```
<details><summary>✅ <b>review</b> — 4 commits audited, no pending blocks</summary>
<details><summary>🔧 <b>review</b> — 2 blocks, both retired by later commits</summary>
<details><summary>⚠️ <b>lint</b> — exit 1</summary>
<details><summary>⚪ <b>ci</b> — not observed by Sentinel</summary>
```

The `review` step is where Sentinel is stronger than the reference and must
show it. The reference asserts in prose that it found problems and fixed them.
Sentinel has the ledger: the commit that blocked, the finding with its
`file:line`, the later commit that retired the block, and the re-audit that
cleared it. Render that chain, SHA by SHA. It is evidence, not a claim.

The existing commit × dimension matrix (`RenderMatrix`) belongs inside this
step's `<details>`, not at the top of the body. It is an audit artifact.

### 2.6 The machine attestation

One HTML comment, invisible to the reader, immediately before the Pipeline
section:

```
<!-- vas-sentinel-attestation:v1 {"head_sha":"...","branch":"...","verdict":"...","steps":[{"step":"review","status":"passed"},...]} -->
```

`v1` in the marker is the schema version and is mandatory: a later reader must
be able to refuse a shape it does not know. `pr create` reads it to fill in the
`ci` step without re-deriving anything else.

Add a parser beside the renderer: `ParseAttestation(body string) (Attestation,
error)`. It must reject a body carrying more than one attestation comment
rather than taking the first, and must return a typed "absent" for a body with
none.

### 2.7 Evidence files

Long evidence is committed to the repository and linked, not only pasted. The
body is truncated at `PRBodyLimit` (`internal/review/renderer.go:424`); a
committed log is not.

- Location: `.vas_sentinel/evidence/<branch-slug>-<branch-identity-hash>/<step>.log`,
  where the identity hash is derived from the exact branch name to distinguish
  branches with the same slug.
- Written by `pr review`, in the working tree, as ordinary files. It does not
  commit them: creating a commit is not this command's job.
- **Who commits them is part of the flow, not an afterthought.** An untracked
  log is a log the permalink cannot reach, so the evidence would degrade to an
  excerpt in exactly the path it was designed for. The required step is
  explicit: after `pr review` and before `pr create`, the operator commits the
  evidence, normally as `chore(evidence): record the pr review logs`.
  `pr review` prints that command verbatim. `pr create` refuses to publish when
  the entry references evidence files that are untracked at the head, with a
  message naming them and that commit (piece 5, §3).
- **The loop must terminate, and saying so is part of the spec.** Committing
  the evidence moves the head, which invalidates the entry, which forces a
  second `pr review`, which writes the evidence again. That converges in
  exactly two rounds only if the evidence is a deterministic function of the
  reviewed work: the per-commit ledger records, and the lint/test/build exit
  codes. Adding a commit that contains only evidence logs changes none of
  those, so the second run writes byte-identical files, the tracked-and-
  matching check passes, and no third commit is needed. Two things follow and
  must be enforced: the evidence rendering must not embed a timestamp or any
  other value that changes between runs, and a second run whose evidence bytes
  differ from the tracked ones is a bug in that determinism — report it as an
  error naming the file, do not ask for a third commit.
- Consequence to accept: these logs are inside the worktree, so `sentinel check`
  measures them. They are not authored code, so they must classify as such —
  verify with a test that a large evidence log does not push `check --staged`
  over the 400-line budget. If it does, the classification is the bug, not the
  budget.
- The body links them by permalink at the head SHA, and also embeds the first
  `evidenceEmbedMaxBytes` of the log inline, so a reader with no link still
  sees something.
- A link whose target is not committed would 404, and a link whose target
  changed since the commit would resolve to different bytes. `git ls-files
  --error-unmatch` proves neither: it succeeds for a staged-but-uncommitted
  file and for a tracked file whose working copy has since been edited. The
  check must be about the head blob, not the index:
  1. `git rev-parse --verify HEAD:<path>` — the path exists at the head.
  2. Compare that blob's OID against `git hash-object <path>` — the bytes on
     disk are the bytes the permalink will serve.
  Both must hold to render the link. Otherwise embed the excerpt with a note
  saying whether the log is untracked at the head or differs from it. Test
  both failure modes separately: they have different causes and different
  remedies.

### 2.8 Truncation order

When the rendered body exceeds `PRBodyLimit`:

1. `Intent`, `What Changed` and `Risk Assessment` are never truncated. If those
   three alone exceed the limit, that is a bug in the summariser — fail with an
   error rather than publish a body that lost its verdict.
1b. **The `ci` step's `<details>` block is never dropped either**, even though
   it is last in the step order. `pr create` replaces that block in place
   (piece 5, §4.4), and the replacement is larger than the `⚪ not observed`
   placeholder it overwrites: a run URL, and possibly failed job names. So the
   block must survive truncation AND carry slack. A fixed reserve is only
   sound if the replacement is bounded, so piece 5 must bound it and this
   plan states the bound it relies on:
   - the run URL is a GitHub Actions run URL, capped at 200 bytes;
   - the failed job names are capped at **5 names of 60 bytes each**, and a
     longer list renders `… and N more` instead of growing;
   - the surrounding markup is fixed.
   That yields `ciStepReserveBytes = 1024` as a bound, not a guess. The
   truncation budget is `PRBodyLimit - ciStepReserveBytes`, and the `ci` block
   is excluded from the drop order below. A body that cannot fit sections 1–3
   plus the reserved `ci` block is the §2.8.1 error case. Test the bound
   directly: a CI outcome with 40 failed jobs and a maximum-length URL must
   render inside the reserve.
2. Drop evidence `<details>` blocks from the **last** step backwards, replacing
   each with one line naming the step and its evidence file path.
3. Append a single line stating how many were omitted:
   `_N evidence blocks omitted for size; the full logs are under .vas_sentinel/evidence/._`

Never truncate mid-block and never rely on `TruncateBody`'s blind byte cut for
this section: that cut may land inside a `<details>` and break the Markdown of
everything after it. `TruncateBody` stays as the last-resort backstop only.

## 3. Persistence: the net entry

`pr review` writes one entry. `pr create` reads it and refuses to publish
without one.

- Location: `<git-common-dir>/vas-sentinel/pr-reviews/<key>.json`, through
  `internal/store`, alongside the review ledger. Anchoring on the common
  directory matters for the same reason it did for the ledger: a review run in
  a linked worktree must not die with `git worktree remove`.
- Key: `<branch-slug>-<branch-identity-hash>-<head-sha>`. The readable slug is
  intentionally not identity: the identity hash is derived from the exact branch
  name to distinguish branches such as `feature/review` and `feature-review`.
  The head SHA prevents a stale entry from being reused.
- Content: the rendered body, the attestation struct, the verdict, the head
  SHA, the branch, and the time.
- It is **replaceable, and single per branch**: re-running `pr review` on the
  same head overwrites its entry, and writing an entry for a branch **deletes
  every prior entry carrying that exact branch name**. Two runs over the same
  tree produce the same key, so a re-run corrects rather than accumulates; and
  a branch that moves forward leaves nothing behind. Without that deletion the
  common directory grows one file per head a branch ever had, forever.
- The deletion happens only when `pr review` writes, so a branch that moves
  forward **without** another `pr review` run leaves its old entry in place.
  That is the common case — you commit, then run `pr create`. So the entry at
  a different head IS observable, and piece 5 must handle it: look the entry up
  **by exact branch name**, then compare its `HeadSHA` against the current head.
  That distinguishes "you never reviewed this branch" from "you reviewed it,
  then changed it", which need different messages. Do not look up by exact key.
- Test the deletion directly: write an entry at head A, write one at head B,
  assert only B remains. And test the lookup: an entry at head A with the
  branch now at head B is found and reported as covering A.

```go
// Entry is what pr review authored for one branch at one head. pr create
// composes from it and authors nothing.
type Entry struct {
    Branch      string
    HeadSHA     string
    Title       string // the PR title pr create publishes, unchanged
    Verdict     string
    Body        string
    Attestation Attestation
    Evidence    []string // repo-relative paths of the evidence logs this body links
    At          time.Time
}
```

`pr create` matching rule, specified here because this piece owns the contract:
an entry whose `HeadSHA` differs from the current head is **stale**, and stale
means refuse. Do not publish a judgement about a different tree.

## 4. `--audit-pending`

Retire it. Its behaviour — `pr review` auditing commits that carry no record —
is exactly the boundary this design moved to `sentinel review`. Follow the
precedent already set for `--only-unaudited` (`cmd/sentinel/pr_command.go:79`):
refuse the flag with a message naming the replacement, rather than removing it
silently.

```
--audit-pending was retired: pr review no longer audits commits, and never
audits them on your behalf. Run `sentinel review <sha>` for each unaudited
commit; `sentinel pr review` reports which ones they are.
```

`BranchResult.Unaudited` keeps its meaning and is what the `review` Pipeline
step reports as `⚪`. Unaudited commits inform, they do not gate — that is
already the recorded decision in `docs/issues/decisions.md` and this piece does
not change it.

## 5. Out of scope

- Everything `pr create` does: pushing, `gh`, CI, publication. That is piece 5.
- Any change to `sentinel review` or the per-commit verdict.
- Re-auditing commits under any flag.
- The live-vs-deterministic test classification of the reference.
- Pruning the evidence directory. Note it as follow-up work; do not build it.

## 6. Tests required

- Intent rendering: single shared intent; differing intents deduplicated in
  commit order; no intent at all; mixed `declared` and `conversation` sources.
- A `block` verdict with a reassuring authored `Risk` sentence still renders a
  block.
- `ParseAttestation`: valid, absent, malformed JSON, unknown schema version,
  two attestation comments in one body.
- Truncation: a body over the limit keeps sections 1–3 intact, drops evidence
  blocks from the last step backwards, and states the omitted count; no
  `<details>` is ever cut mid-block.
- The entry key is identical for two runs over the same branch and head, and
  different for distinct heads or branches whose readable slugs collide.
- An entry is refused as stale when the head moved.
- `--audit-pending` exits `1` with the retirement message and audits nothing.
- Evidence linking: a file present at the head with matching bytes renders a
  permalink; one absent at the head renders the excerpt with the untracked
  note; one present but with different bytes renders the excerpt with the
  differs-from-head note. Three cases, three tests.
- Evidence determinism: two consecutive `pr review` runs over the same records
  and exit codes write byte-identical evidence files.
- `What Changed`: bullets render in order; an empty `Changed` renders the
  fallback and does not block.
- `Testing`: the two origins render under their own headings with their real
  exit codes, and a non-zero exit renders `❌`, never `✅`.
- `Pipeline`: the eight steps render in the fixed order; a step with no data
  renders `⚪` with its stated reason; the commit × dimension matrix appears
  inside the `review` step's `<details>` and nowhere else; and a ledger chain
  of block → fix → cleared renders all three SHAs.

Verify every new test by mutation: break the rule it covers, confirm it fails,
restore, confirm it passes. A test that re-evaluates the production expression
locally instead of calling the function under test is not a test.

## 7. Constraints

All of `AGENTS.md` applies: English artifacts, Conventional Commits, no agent
attribution trailers, `filepath.Join` for paths and `filepath.ToSlash` for git,
`sentinel check` before proposing, and `sentinel review` on every commit you
create.

## 8. Verification

```
go build ./...
go vet ./...
go test ./internal/review ./internal/app/pr ./cmd/sentinel
go test ./...
```

Report the observed result of each command.

## 9. Suggested commit order

1. `feat(review): read the branch intent from the commit trailers` — intent
   rendering and the `HonestNetIntention` removal.
2. `feat(review): author the change summary and the risk sentence` —
   `OverviewResult` extension and the prompt.
3. `feat(review): render the pipeline audit trail` — the step table, the
   `<details>` shapes, the matrix moved inside the review step.
4. `feat(review): seal a machine attestation into the body` — the comment, the
   parser, its tests.
5. `feat(review): write the evidence files and link them` — evidence writing,
   tracked/untracked link rule.
6. `feat(review): persist the net entry pr create consumes` — the store entry,
   the key, the staleness rule.
7. `feat(pr): retire --audit-pending` — the refusal and its message.

Each one must build, pass its tests, and pass `sentinel review` on its own.
