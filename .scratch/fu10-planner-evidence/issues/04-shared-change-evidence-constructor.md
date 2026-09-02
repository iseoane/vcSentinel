# 04: One Shared Change-Evidence Constructor

**What to build:** A single place that builds the detector input for a change,
used by `explain`. After this ticket `explain` behaves exactly as before, but
the evidence it feeds the detectors is assembled by shared code rather than
inline in a CLI command.

**Blocked by:** 03, 03b.

**Status:** ready-for-agent.

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

- [ ] One exported constructor in the change package builds the detector input
      from a change profile, the changed paths, unified diff text, and
      `.gitattributes` content.
- [ ] Added lines are parsed from the diff text in memory, with no subprocess
      per path.
- [ ] Focused RED tests for the parser exist before the production code and
      cover renames, added and deleted files, binary files, and colourless
      output.
- [ ] The sensitive-path patterns move out of the CLI command into the
      constructor.
- [ ] `explain` uses the constructor and produces identical output for the same
      range as before the change, proved on at least three commits of differing
      kind.
- [ ] The review planner is untouched by this ticket.
- [ ] `go build ./...`, `go vet ./...`, focused tests, and the full suite pass,
      with the exact commands and outcomes recorded.
- [ ] Each of the parser cases above has a focused test.
- [ ] Any new file that shells out is registered in the adapter-site inventory.
      `go build` and `go vet` do not catch an unregistered one; the inventory
      test does, and it fails the whole suite.
- [ ] `sentinel review` of the commit completes with every finding either
      resolved or dispositioned with a verified premise and a stated reason for
      not acting.

## Acceptance matrix

Completed before delegation. No row is unowned or unmeasurable.

| ID | Criterion or obligation | Observable check | Owner | Evidence | Disposition |
|---|---|---|---|---|---|
| C-01 | One exported constructor in the change package builds the detector input | A single function takes profile, paths, diff text and `.gitattributes` content and returns the detector input | Writer | Focused test naming the constructor | pending |
| C-02 | Added lines parsed in memory | No subprocess per path anywhere in the constructor path | Writer | Focused parser test plus absence of any exec call in the new code | pending |
| C-03 | Focused RED before production code | The parser test fails for the requested behaviour, not for a missing symbol | Writer | Exact command, exit status, observed failure | pending |
| C-04 | GREEN after production code | Same focused checks pass | Writer | Exact commands and outcomes | pending |
| C-05 | Quoted paths survive | A path with a space or a non-ASCII byte keys the map by its real name | Writer | Focused test | pending |
| C-06 | Root commits and pure additions | `/dev/null` on the old side yields the whole file as added | Writer | Focused test | pending |
| C-07 | Renames and mode-only changes | A header with no hunk contributes no lines and no error | Writer | Focused test | pending |
| C-08 | Binary files | A binary body contributes no lines rather than garbage | Writer | Focused test | pending |
| C-09 | No-newline marker | `\ No newline at end of file` is counted as neither added nor removed | Writer | Focused test | pending |
| C-10 | Header sequences inside added lines | An added line beginning `+++` or containing `@@` is not read as a new file header | Writer | Focused test | pending |
| C-11 | Sensitive-path patterns leave the CLI command | The literal lives with the detectors | Writer | Grep of the command file plus a constructor test | pending |
| C-12 | `explain` output identical | Same range, same bytes, before and after | Coordinator | `sentinel explain` on at least three commits of differing kind, diffed | pending |
| C-13 | Review planner untouched | The plan derivation and its four consumers are unchanged | Coordinator | Diff inspection of the task-only change | pending |
| C-14 | Adapter-site inventory | Any new file that shells out is registered | Writer | `go test ./internal/adaptersites` | pending |
| C-15 | Build, vet, full suite | All pass | Writer, confirmed by coordinator | Exact commands and outcomes | pending |
| C-16 | Staged volume within budget | Each commit under the 400-line review budget | Coordinator | `sentinel check --staged` output per commit | pending |
| C-17 | Sentinel review of every task commit | A review record exists per commit | Sentinel | Review verdict and findings per SHA | pending |
| C-18 | Findings resolved or dispositioned | No block carried forward; every warning has a verified premise and a stated reason | Coordinator, verdict by Sentinel | Per-finding disposition | pending |
| C-19 | Language scan | Every new or changed artifact is English; legacy Spanish preserved | Coordinator | Scan scope and outcome | pending |
| C-20 | Worktree clean, unrelated paths untouched | Task paths only | Coordinator | `git status` and the named untouched paths | pending |
| C-21 | Final gate | Recorded with its exact command and outcome | Sentinel | `sentinel gate` result | pending |

**Owned paths:** the change package, the explain command, and their tests. Every
other path is out of scope, the review planner explicitly so.

**Worktree:** `/home/iseoane/0-workspace/vas.sentinel-worktrees/fu10-ticket-04`
on branch `fu10/ticket-04`, branched from `f9a913e`.
