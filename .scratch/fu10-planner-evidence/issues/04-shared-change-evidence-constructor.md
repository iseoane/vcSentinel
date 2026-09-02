# 04: One Shared Change-Evidence Constructor

**What to build:** A single place that builds the detector input for a change,
used by `explain`. After this ticket `explain` behaves exactly as before, but
the evidence it feeds the detectors is assembled by shared code rather than
inline in a CLI command.

**Blocked by:** 03, 03b.

**Status:** complete.

## Why this shape

FU-10 says the defect is that one derivation runs on two different inputs, and
that it must not be fixed by widening a single detector. Adding one missing
field to the planner would recreate the same defect on the next field: the two
call sites already disagree on added lines, on `.gitattributes`, and on the
sensitive-path patterns, and neither supplies the data patterns, the test map,
or the file contents.

The sensitive-path patterns are today a literal inside a CLI command file. They
are classification policy and belong with the detectors.

This ticket expands only. The planner is not migrated here, so `explain` proves
the constructor before anything changes behaviour.

The real work is an in-memory unified-diff parser. Three of the four planner
consumers already hold the commit diff, and one of them is the pre-commit
enforcement boundary, so reading added lines with one `git diff` per path is not
acceptable there.

## What the parser must survive

Learned while building the ticket 03 harness, which sidestepped most of this by
reading paths NUL-delimited. A unified-diff parser cannot: it reads the path out
of the `+++ b/<path>` header, where Git's own quoting applies.

- **Quoted paths.** Git quotes and escapes a path holding a space or a non-ASCII
  byte in diff headers under `core.quotePath`. A parser that takes the header
  bytes literally will key its map by a path that never matches the changed-path
  list, and the added lines silently vanish for exactly the files most likely to
  be interesting.
- **Root commits and pure additions**, where the old side is `/dev/null`.
- **Renames and mode-only changes**, which produce a header with no hunk at all.
- **Binary files**, whose body is not parseable and must contribute no lines
  rather than garbage.
- **`\ No newline at end of file`**, which is neither an added nor a removed
  line and must not be counted as one.
- **A `+++` or `@@` sequence inside an added line**, which a naive
  line-prefix scanner reads as a new file header.

A silently empty result is the failure mode that matters here: it understates
every content-reading detector and biases the whole thing back toward today's
starved planner, which is the defect this sequence exists to remove.

**Acceptance criteria:**

- [x] One exported constructor in the change package builds the detector input
      from a change profile, the changed paths, unified diff text, and
      `.gitattributes` content.
- [x] Added lines are parsed from the diff text in memory, with no subprocess
      per path.
- [x] Focused RED tests for the parser exist before the production code and
      cover renames, added and deleted files, binary files, and colourless
      output.
- [x] The sensitive-path patterns move out of the CLI command into the
      constructor.
- [x] `explain` uses the constructor and produces identical output for the same
      range as before the change, proved on at least three commits of differing
      kind.
- [x] The review planner is untouched by this ticket.
- [x] `go build ./...`, `go vet ./...`, focused tests, and the full suite pass,
      with the exact commands and outcomes recorded.
- [x] Each of the parser cases above has a focused test.
- [x] Any new file that shells out is registered in the adapter-site inventory.
      `go build` and `go vet` do not catch an unregistered one; the inventory
      test does, and it fails the whole suite.
- [x] `sentinel review` of the commit completes with every finding either
      resolved or dispositioned with a verified premise and a stated reason for
      not acting.

## Acceptance matrix

Completed before delegation. No row is unowned or unmeasurable.

| ID | Criterion or obligation | Observable check | Owner | Evidence | Disposition |
|---|---|---|---|---|---|
| C-01 | One exported constructor in the change package builds the detector input | A single function takes profile, paths, diff text and `.gitattributes` content and returns the detector input | Writer | Focused test naming the constructor | met |
| C-02 | Added lines parsed in memory | No subprocess per path anywhere in the constructor path | Writer | Focused parser test plus absence of any exec call in the new code | met |
| C-03 | Focused RED before production code | The parser test fails for the requested behaviour, not for a missing symbol | Writer | Exact command, exit status, observed failure | met |
| C-04 | GREEN after production code | Same focused checks pass | Writer | Exact commands and outcomes | met |
| C-05 | Quoted paths survive | A path with a space or a non-ASCII byte keys the map by its real name | Writer | Focused test | met |
| C-06 | Root commits and pure additions | `/dev/null` on the old side yields the whole file as added | Writer | Focused test | met |
| C-07 | Renames and mode-only changes | A header with no hunk contributes no lines and no error | Writer | Focused test | met |
| C-08 | Binary files | A binary body contributes no lines rather than garbage | Writer | Focused test | met |
| C-09 | No-newline marker | `\ No newline at end of file` is counted as neither added nor removed | Writer | Focused test | met |
| C-10 | Header sequences inside added lines | An added line beginning `+++` or containing `@@` is not read as a new file header | Writer | Focused test | met |
| C-11 | Sensitive-path patterns leave the CLI command | The literal lives with the detectors | Writer | Grep of the command file plus a constructor test | met |
| C-12 | `explain` output identical | Same range, same bytes, before and after | Coordinator | `sentinel explain` on at least three commits of differing kind, diffed | met |
| C-13 | Review planner untouched | The plan derivation and its four consumers are unchanged | Coordinator | Diff inspection of the task-only change | met |
| C-14 | Adapter-site inventory | Any new file that shells out is registered | Writer | `go test ./internal/adaptersites` | met |
| C-15 | Build, vet, full suite | All pass | Writer, confirmed by coordinator | Exact commands and outcomes | met |
| C-16 | Staged volume within budget | Each commit under the 400-line review budget | Coordinator | `sentinel check --staged` output per commit | met |
| C-17 | Sentinel review of every task commit | A review record exists per commit | Sentinel | Review verdict and findings per SHA | met |
| C-18 | Findings resolved or dispositioned | No block carried forward; every warning has a verified premise and a stated reason | Coordinator, verdict by Sentinel | Per-finding disposition | met |
| C-19 | Language scan | Every new or changed artifact is English; legacy Spanish preserved | Coordinator | Scan scope and outcome | met |
| C-20 | Worktree clean, unrelated paths untouched | Task paths only | Coordinator | `git status` and the named untouched paths | met |
| C-21 | Final gate | Recorded with its exact command and outcome | Sentinel | `sentinel gate` result | met |

**Owned paths:** the change package, the explain command, and their tests. Every
other path is out of scope, the review planner explicitly so.

**Worktree:** `/home/iseoane/0-workspace/vas.sentinel-worktrees/fu10-ticket-04`
on branch `fu10/ticket-04`, branched from `f9a913e`.

## Evidence

- Delegated to a writer in the dedicated worktree `fu10-ticket-04` on branch
  `fu10/ticket-04`. Effective identity: opencode `build` agent, model
  `openai/gpt-5.6-luna`, variant `max`.
- The first delegation attempt stalled for 42 minutes without leaving `init` and
  exited 0 having written nothing. A minimal probe with the same flags and model
  succeeded in one minute, so the cause was the ~4.5 KB prompt passed as one
  shell argument, not the writer. The retry pointed at this ticket file instead
  and started immediately.
- RED first, by the writer:
  `go test ./cmd/sentinel -run TestLineasAnadidasExplainReadsUnifiedDiffOnce -count=1`
  failed on behaviour before the production code existed.
- Verified by the coordinator rather than accepted from the handoff:
  `go build ./...`, `go vet ./...`, `gofmt -l .`, and `go test ./...` all clean.
- C-12: `explain` output byte-identical across 120 non-merge commits plus
  `5dccc8e`, which carries a rename, comparing binaries built from `f9a913e` and
  from the branch tip. Re-run after each correction.
- C-13: the plan derivation and its four consumers are untouched, confirmed by
  diff inspection.
- A suspected regression was checked rather than reported: the writer added `-M`
  without flagging it, which looked like it would change rename handling. It
  does not, because `diff.renames` is already on by default, and the rename
  commit compares identical.
- Commits `b7ac0cf`, `0269350`, `78f52f4`, `df034c1`, `1bf5e1e`, merged with
  `--no-ff`. Sentinel review ran on each.
- Three correction rounds, all on one recurring defect: a comment or test
  claiming more than the evidence supported. The last was closed by making the
  test run the unforced invocation too and assert the difference, rather than by
  softening the comment.

## Follow-ups

- FU-12: reviews run in a linked worktree write to `--git-dir` rather than
  `--git-common-dir`, so these commits' receipts never reached the repository
  ledger.
- The attribution-trailer cleanup rewrote this session's commits, so 24 review
  receipts now sit outside the current history and none of the rewritten SHAs
  carries one. Re-review is required before any gate that must validate a
  receipt for them.
