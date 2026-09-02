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
