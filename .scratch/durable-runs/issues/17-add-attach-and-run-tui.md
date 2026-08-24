# 17: Add Attach And The Run TUI

**What to build:** Humans can attach to a live repository host and observe one
run — lifecycle, invocations, decisions, evidence, and semantic outcome —
through a Bubble Tea terminal interface fed by cursor-based event replay.
Keyboard actions route through the daemon-preferred repository host with the
same idempotency and revision discipline as every other client. The view
survives endpoint loss through bounded reconnect with replay resumption from
the last seen cursor, and reaches an explicit terminal state.

**Blocked by:** 16 (complete, merged at 4058a04).

**Status:** in_progress

**Design contract (agreed analysis):**

- Data pipeline first: a pure typed RunView model built from
  Subscribe-from-cursor pages (the daemon wire op) plus Inspect snapshots —
  no TUI code in that layer, fully unit-tested. The TUI is a thin renderer
  and command dispatcher over it.
- Dependency addition sanctioned by the roadmap itself: charmbracelet
  bubbletea (with lipgloss for styling) enters go.mod at this unit. Version
  pinned; transitive footprint recorded in evidence. No other new deps.
- Keyboard set (minimal honest v1): quit; refresh; abort; respond (text
  input sub-view); retry when the projection says retryable. Abort/respond
  rely on the existing idempotency identity plus server-side admission
  serialization; retry pins ExpectedRevision from the last observed head —
  this is the revision-aware mapping the roadmap asks for, documented per
  key in the help footer.
- Reconnect: a lost or failed exchange flips the model to a reconnecting
  state with bounded backoff redials; on success the attach resumes by
  requesting events strictly after the last applied cursor, so nothing is
  duplicated or skipped. A terminal projection freezes the view into a
  final state with an exit hint instead of polling forever.
- `runs attach` without --run renders a list of non-terminal runs to pick
  from (durable query, not a daemon roundtrip requirement), then attaches.
- Rendering determinism: golden views are plain strings produced by View()
  from fixed model states (teatest or plain golden files, whichever the
  chosen bubbletea generation supports cleanly); no random timestamps inside
  rendered output — timings stay in the data layer fields that goldens omit.
- Windows/Linux: bubbletea's cross-platform input stack is accepted as-is;
  CI proofs run on Linux with GOOS=windows compile gates like D2.

**Acceptance criteria:**

- [ ] `runs attach --run <id>` renders lifecycle, invocations, decisions,
      evidence presence, and semantic outcome for a run served over a real
      daemon socket in tests.
- [ ] Replay from cursor: attaching mid-run shows prior events exactly once,
      proven by a seeded stream test asserting the applied sequence.
- [ ] Keyboard abort/respond/retry reach the host and produce the same
      durable effects as their CLI twins; retry carries ExpectedRevision.
- [ ] Endpoint loss triggers bounded reconnect with cursor-resumed replay;
      terminal projection freezes the view with an exit hint.
- [ ] Golden views pin rendering of at least running, awaiting-decision,
      succeeded, canceled, and reconnecting states.
- [ ] List mode without --run shows non-terminal runs and exits cleanly.
- [ ] Evidence records build, vet, tests, guardian, independent code-review
      per slice, Judgment Day at closure, rollback boundary, follow-ups.

**Out of scope:** multi-run dashboards; mouse support; remote hosts (D4/D5);
changing any durable format; autostarting daemons.

**Suggested slices:**

1. Pure attach read-model (replay + snapshot merge into RunView), list-mode
   query, CLI skeleton rendering text snapshots — zero TUI deps.
2. Bubble Tea program: model/update/view, keyboard dispatch through the
   daemon-preferred host, reconnect-with-cursor loop, terminal freeze;
   dependency landing commit separated.
3. Golden views for the five pinned states, daemon integration harness
   end-to-end, documentation touch-ups, Judgment Day closure.

## Evidence — slice 1 (pure read-model + CLI skeleton)

*(pending)*

## Evidence — slice 2 (bubbletea program)

*(pending)*

## Evidence — slice 3 (goldens, harness, closure)

*(pending)*
