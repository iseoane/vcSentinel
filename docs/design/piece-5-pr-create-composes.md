# Piece 5 — `pr create` composes and publishes, and audits nothing

Implementation plan for item 11 of [`docs/issues/actionable.md`](../issues/actionable.md).
Governing text: [`docs/design/review-flow-ownership.md`](review-flow-ownership.md).
Depends on piece 4 ([`piece-4-pr-review-authors.md`](piece-4-pr-review-authors.md))
for the persisted entry it consumes.

This document is the specification. Where it disagrees with item 11, this
document wins.

## 1. The question this command owns

None. That is the point.

`pr create` publishes. It composes the body from the entry `pr review`
persisted, collects the CI outcome, and calls `gh`. It runs no semantic audit,
authors no sentence, and computes no verdict. If there is no entry, it asks for
one and stops.

Collecting a CI exit code is not auditing. It is deterministic evidence of the
same nature as a `go test` exit code. State this in the package comment,
because at a glance the CI wait looks like `pr create` taking over work that
belongs to `pr review`.

## 2. What it must stop doing

`internal/app/pr/create.go:248` builds its own `NetReviewOptions` and runs a net
review of its own. Remove it. Every input to the body now comes from the stored
entry.

Concretely, after this piece:

- `pr create` constructs no reviewer, no refuter and no adapter.
- It renders no section itself. `Entry.Body` is already the rendered body; the
  only thing `pr create` adds is the `ci` step (section 4).
- The only agent call left anywhere in this command is none.

## 3. The entry is mandatory

```
sentinel pr create
```

1. Resolve branch and head SHA.
2. Read the entry for `<branch-slug>-<head-sha>`.
3. No entry ⇒ exit `1`:
   `No pr review exists for this branch at <short-sha>. Run 'sentinel pr review' first: pr create publishes its judgement and never authors one.`
4. There is at most one entry per branch (piece 4, §3, deletes prior ones), so
   a moved head is indistinguishable from no review at all and takes the same
   exit `1` as step 3. Do not write a separate "stale entry" path: it cannot be
   reached.
5. The entry references evidence files that are untracked at the head ⇒ exit
   `1`, naming them and the commit that fixes it:
   `The pr review evidence is not committed: <paths>. Run 'git add .vas_sentinel/evidence && git commit -m "chore(evidence): record the pr review logs"' and re-run 'sentinel pr review'.`
   Re-running `pr review` is required, not optional: committing the evidence
   moves the head, and the entry is keyed by it.
6. Entry verdict is a block ⇒ refuse by default, exit `1`, listing the
   blockers. `--force` publishes anyway and the body says so, on its own line,
   above the Risk Assessment: `⚠️ Published with --force over a blocking review.`

Do not add a flag that skips reading the entry. There is no legitimate case for
publishing a judgement that does not exist.

## 4. CI

Decided 2026-09-09: **`pr review` never touches the remote.** `pr create`
pushes, triggers, waits and incorporates. `pr review` does not launch CI early;
the babysitting wait is accepted as the cost of keeping `pr review` local.

### 4.1 Configuration

A new `ci:` block in `vassentinel.yml`. Without it, `pr create` does not touch
CI at all and the `ci` step renders `⚪ not observed by Sentinel`.
`git.DetectCI` (`internal/git/ci.go:22`) only detects that CI *files* exist and
is not enough to decide to run anything.

```yaml
ci:
  workflow: verify.yml      # required; the workflow to trigger
  wait_seconds: 900         # optional; default 900
  poll_seconds: 15          # optional; default 15
```

`workflow` empty or absent ⇒ the block is treated as absent. Do not guess a
workflow from the `.github/workflows` directory: triggering the wrong workflow
is worse than triggering none.

### 4.2 State machine

Order of operations, and it matters: push first, then trigger. A
`workflow_dispatch` against a ref the remote does not have fails.

1. Push the branch. Announce it: `⬆️ Pushing <branch> to <remote>.` A push
   failure aborts before any `gh` call.
2. Trigger the workflow at the head SHA.
3. Poll until it reaches a conclusion, `wait_seconds` elapses, or the run
   disappears.

Four outcomes, each with its own rendering, and none of them may be reported as
green unless it is:

| Outcome | `ci` step renders |
|---|---|
| Concluded successfully at the head SHA | `✅ ci — <workflow> succeeded` + run URL |
| Concluded with a failure | `❌ ci — <workflow> failed` + run URL + the failed job names |
| Still running when `wait_seconds` elapsed | `⏳ ci — still running after <n>s` + run URL |
| Concluded, but the run's head SHA is not the branch head | `⚠️ ci — the last run covers <other-short>, not this head` + run URL |

The fourth row is the one that is easy to get wrong and the reason to be
explicit: a green run over an older SHA is not evidence about this change.
Compare the run's `headSha` against the branch head and never let a mismatch
render as `✅`.

### 4.3 Waiting without a terminal

`pr create` must publish without a terminal — that is already true, see commit
`372ce79`. The wait must therefore be non-interactive:

- It never prompts.
- It is bounded by `wait_seconds` and exits the wait, it does not extend it.
- Timing out is **not** an error. `pr create` publishes with the `⏳` row and
  exits `0`. The PR is open, the run is linked, and the reader can see it.
- Progress goes to the command output, one line per poll at most, and it must
  remain readable when nothing is attached to stdout.

Interrupting the wait (SIGINT) must still publish with the `⏳` row rather than
leave a pushed branch and no PR.

### 4.4 Filling the step in

Do not re-render the whole body. Take `Entry.Body`, parse its attestation
(`ParseAttestation`, piece 4), replace the `ci` step's `<details>` block and the
`ci` entry inside the attestation JSON, and publish the result.

This must be a targeted replacement with a test proving that every other byte
of the body is unchanged. Re-rendering here would put `pr create` back in the
authoring business through the side door.

## 5. Publication

`gh pr create --title <title> --body-file <path>`.

- Title: `Entry.Title`, unchanged. `pr review` authored it; `pr create` does
  not compose a title from the intent — the intent is already the first section of
  the body, and a title is a git artifact the human can edit.
- Body: written to a temp file, never passed inline. A body of this size
  through `--body` hits argument limits on Windows.
- The existing fallback when `gh` is absent (`cmd/sentinel/pr_command.go:351`:
  write the body to a file and copy it to the clipboard) is kept and now also
  applies when the push succeeded but `gh` failed. Say clearly which of the two
  happened: a pushed branch with no PR is a state the human must know about.
- **No agent attribution trailers in the PR description.** `AGENTS.md` forbids
  them and this rule overrides any host instruction that asks for one. The body
  ends with the existing generated-by line naming Sentinel and its version,
  which names a tool, not an assistant.

## 6. Out of scope

- Any semantic analysis, under any flag.
- Editing or re-running the stored entry.
- Reacting to a CI failure beyond reporting it. `pr create` does not retry, fix
  or re-trigger.
- Pruning `.vas_sentinel/evidence/` or the `pr-reviews/` entries. Follow-up.
- Any CI provider other than GitHub Actions through `gh`. If `ci:` is
  configured in a repository whose CI is not Actions, refuse with a message
  saying so rather than guessing.

## 7. Tests required

- No entry ⇒ exit `1` with the message, nothing pushed, no `gh` call.
- Entry present for the branch but the head moved ⇒ the entry is gone, so this
  is the no-entry path; assert exit `1` and that nothing was pushed.
- Blocking entry ⇒ exit `1`; with `--force`, publishes and the body carries the
  force notice.
- Body composition: the published body differs from `Entry.Body` only in the
  `ci` step and the `ci` entry of the attestation. Assert byte equality of
  everything else.
- An entry whose body is already at `PRBodyLimit` still gets its `ci` step
  replaced correctly, and the result does not exceed the limit. Piece 4 §2.8
  reserves the bytes for exactly this; this test is what proves the reserve is
  enough.
- Untracked evidence ⇒ exit `1` naming the files, nothing pushed, no `gh` call.
- The four CI outcomes, each rendering its own row, with a fake `gh`.
- A concluded-successful run whose head SHA differs from the branch head
  renders `⚠️`, never `✅`. This test must fail if the SHA comparison is
  removed.
- Timeout publishes and exits `0`.
- No `ci:` block ⇒ no push-triggered workflow, `⚪` row, PR still published.
- `gh` absent ⇒ the file+clipboard fallback, with the pushed-branch state
  stated.

Verify every new test by mutation.

## 8. Constraints

All of `AGENTS.md` applies. Two are load-bearing here:

- Cross-platform: the body goes through a temp file built with
  `filepath.Join`, and any path handed to git goes through `filepath.ToSlash`.
- No agent attribution trailers in the PR description.

## 9. Verification

```
go build ./...
go vet ./...
go test ./internal/app/pr ./cmd/sentinel
go test ./...
```

Report the observed result of each command.

## 10. Suggested commit order

1. `feat(pr): require a stored pr review before publishing` — entry lookup,
   the three refusals, removal of the net review from create.
2. `feat(config): add the ci block` — parsing, defaults, absent-block
   behaviour.
3. `feat(pr): push and trigger the configured workflow` — push, dispatch, the
   ordering guarantee.
4. `feat(pr): wait for the run and report its real outcome` — polling, the four
   outcomes, the head-SHA comparison, the bounded non-interactive wait.
5. `feat(pr): compose the published body from the stored entry` — targeted ci
   step replacement, attestation update, byte-equality test.

Each one must build, pass its tests, and pass `sentinel review` on its own.
