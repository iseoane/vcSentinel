# 01: Commit The Reengineering Skill Refactor

**What to build:** The reengineering phase-task skill lands as a recorded
change so the worktree holds nothing unrelated to FU-10. Without this, the
staged-volume check and every slice during FU-10 drags an unrelated skill
refactor along with it.

**Blocked by:** None (can start immediately).

**Status:** complete.

**Acceptance criteria:**

- [x] The skill refactor and its new reference documents are committed.
- [x] `git status` reports a clean worktree.
- [x] The commit touches only skill paths and says plainly that it is unrelated
      to the FU-10 work that follows.

## Evidence

- Commit `ac296d2`, 3 files changed, 171 insertions, 65 deletions.
- Staged candidate measured 0 authored lines; the 171 lines are informational
  and do not count toward the review budget.
- The diff was committed as recorded, without review, at the user's explicit
  instruction.
