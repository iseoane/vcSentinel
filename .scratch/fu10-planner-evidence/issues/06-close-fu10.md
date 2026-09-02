# 06: Close FU-10 In The Debt Ficha

**What to build:** FU-10 stops being an open follow-up. The debt ficha records
what was decided, what it cost, and what is still open, so a reader who never
saw this work can tell why the two surfaces now agree.

**Blocked by:** 05.

**Status:** ready-for-agent.

**Acceptance criteria:**

- [ ] FU-10 in the debt ficha records the resolution, the measured delta from
      ticket 03, and the path-class decision from ticket 02.
- [ ] The entry states plainly that the demonstrated instance was a false
      positive on the `explain` side, so a later reader does not mistake it for
      evidence that the planner under-reviewed that commit.
- [ ] Every acceptance matrix that cites FU-10 as the reason a candidate drew
      zero dimensions is re-read, and each citation is either still correct or
      corrected in place.
- [ ] Any residual — risk-rule calibration in particular — is recorded as a new
      follow-up with a target and a reason, not folded into this closure.
- [ ] No task path outside the debt ficha and the cited matrices is touched.
