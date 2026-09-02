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
- [ ] `sentinel review` of the commit completes with no unresolved finding.
