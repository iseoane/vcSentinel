# 06: Close FU-10 In The Debt Ficha

**What to build:** FU-10 stops being an open follow-up. The debt ficha records
what was decided, what it cost, and what is still open, so a reader who never
saw this work can tell why the two surfaces now agree.

**Blocked by:** 05.

**Status:** complete.

**Acceptance criteria:**

- [x] FU-10 in the debt ficha records the resolution, the measured delta from
      ticket 03, and the path-class decision from ticket 02.
- [x] The entry states plainly that the demonstrated instance was a false
      positive on the `explain` side, so a later reader does not mistake it for
      evidence that the planner under-reviewed that commit.
- [x] Every acceptance matrix that cites FU-10 as the reason a candidate drew
      zero dimensions is re-read, and each citation is either still correct or
      corrected in place.
- [x] Any residual — risk-rule calibration in particular — is recorded as a new
      follow-up with a target and a reason, not folded into this closure.
- [x] No task path outside the debt ficha and the cited matrices is touched.

## Evidence

- FU-10 in `docs/reingenieria/f0-deuda.md` carries a `Resolved 2026-09-02`
  section with the decision, the measured cost, the false-positive reading, the
  two detector narrowings, and the prose guard.
- Three citation sites re-read rather than assumed:
  `f9-observabilidad.md:747`, `t9-4a-acceptance.md` C-21, and
  `t9-4b-acceptance.md` at C-15 and in the correction narrative.
- Each correction is a dated note appended to the original text, never a
  rewrite. Those records were true when they were written, and the phase
  convention is to supersede rather than edit history.
- The corrections are measured, not reasoned. Every documentation commit those
  records cite still derives `none` under the full evidence, read from the
  pinned artifact's shared arm, which is what production computes since ticket
  05: `a1a5803`, `780c900`, `91bfefc`, `95e8e0b`, `8d659d7`, `5178e4f` and
  `a200316` all `none` with zero invocations. `68a2910` is configuration, not
  documentation, and stays `elevated` with five.
- `780c900` is the commit FU-10 used as its demonstration, and it is the sharpest
  confirmation: `explain` called it `high por security_sensitive presente`
  through a substring match inside `cached_input_tokens`, and both surfaces now
  agree on `none`.
- Residuals opened during the sequence and left open with targets and reasons:
  FU-11 (no signal survives the class filter for a credential in prose), FU-13
  (net planning classifies from the sanitised path list), FU-14 (half the
  detectors classify without the repository attributes). FU-12 is next by the
  agreed order.
- No path outside the debt ficha and the cited records was touched.
