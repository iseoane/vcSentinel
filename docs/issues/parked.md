# Parked — analysed and declined

Each entry keeps its reopen trigger in full: the trigger is the whole
reason the entry survives. None of these is scheduled; all of them can
come back on their stated condition.

## FU-2: oversized files split (partial split landed 2026-09-05, remainder declined)

- Analysis: 22 production files exceed the 500-line guardian threshold
  (worst `internal/review/engine.go` at 1245 lines, `cmd/sentinel/main.go`
  at 1179). The pr-subcommand split landed (`cmd/sentinel/comandos_pr.go`
  into `internal/app/pr/`); the 11 remaining files stay untouched.
- Declined because: `review.Fingerprint` hashes symbol-or-file and no
  finding populates the symbol component, so moving code makes live blocks
  unaddressable (measured 2026-09-05: 19 fichas cite
  `cmd/sentinel/comandos_review.go`, 99 cite `cmd/sentinel/main.go`).
- Trigger to revisit: a file's size actually obstructs a change someone
  needs to make, or the fingerprint gains a symbol component that survives
  a move. Resume with a file carrying few live blocks, never with
  `main.go`.

## FU-15: corrupt object indistinguishable from a collected one (declined 2026-09-05)

- Analysis: the purge reads a missing commit object as "no longer exists",
  which authorises deleting a ficha whose object is present but unreadable
  while `HEAD` stays readable. The defended cases hold (wholesale
  unreadable store caught by the `HEAD`-anchored guard, ordinary orphans
  decided by branch containment, other query failures aborting the purge);
  the remaining exposure needs object-level corruption leaving `HEAD`
  intact. Both cheap fixes were rejected (`git fsck` disproportionate per
  purge and per SHA; treating absence as absence leaks gc-collected fichas
  forever).
- Trigger to revisit: an observed instance, or a cheaper way to
  distinguish a collected object from a corrupt one.

## FU-20: semantic failures on success-terminal runs (declined 2026-09-05)

- Analysis: the review engine's corrective-retry shape records semantic
  failure classes on invocations whose reconciled outcome is terminal
  success, so the snapshot pairs a success stream with a failing snapshot;
  once retention collects the stream, the run reads failed (a one-time
  reclassification of roughly 140-180 pre-existing runs). The T9.5
  agreement guard already stops further accumulation; the open producer
  question is whether a semantic failure should attach to a
  success-terminal run at all or force failure at the source.
- Trigger to revisit is unchanged: contradicted runs accumulating (watch
  the `snapshot contradicts terminal success` keep reason in prune
  reports), or the review transport changing its finalization.
