# 17: Add Attach And The Run TUI

**What to build:** Humans can attach to a live repository host and observe one
run — lifecycle, invocations, decisions, evidence, and semantic outcome —
through a Bubble Tea terminal interface fed by cursor-based event replay.
Keyboard actions route through the daemon-preferred repository host with the
same idempotency and revision discipline as every other client. The view
survives endpoint loss through bounded reconnect with replay resumption from
the last seen cursor, and reaches an explicit terminal state.

**Blocked by:** 16 (complete, merged at 4058a04).

**Status:** complete

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

- Base: main at 4058a04 (post-D2 integration).
- Commits: feat(attach) pure run view model with cursor-based replay
  collector (+279), test(attach) pin replay semantics and view derivation
  (+316), feat(sentinel) runs attach list, snapshot, and follow modes
  (+267), test(sentinel) pin attach modes, daemon preference, and detach
  contracts (+383). All four staged candidates passed the pre-commit budget.
- Surface: internal/attach — ReplayCollector (strict-after-cursor,
  duplicate-suppressing, monotonic cursor, defensive Frames copy) and pure
  BuildRunView deriving invocation order (first-appearance Order since
  durable frames carry no attempt counter — documented honestly),
  creating-decision mapping, evidence hash-PRESENCE flags (raw bytes never
  surface), semantic outcome. CLI: `runs attach` list mode via reconciled
  projection walk (ScanRecoveries rejected with in-code rationale: it
  answers repair attention, not lifecycle truth), snapshot and --follow
  modes through the daemon-preferred host resolver with silent fallback;
  pagination bounded; reprint-on-change only; SIGINT/SIGTERM clean detach.
- Independent code review (dual axis): spec PASS; standards found one hard
  fix (usage promised SIGTERM but only os.Interrupt was registered — now
  mirrors daemon.Run exactly) plus adopted nits: Identity threaded end to
  end, redundant DeepEqual condition dropped after proving from store
  read-ordering docs that outcome text cannot arrive frame-less, unchanged-
  poll no-reprint and context-cancel detach tests added.
- Honest limits: Ctrl-C signal path itself is exercised through the
  cancellable-context seam rather than real signals (portable-signal
  testing deferred); rendering is plain text by design until slice 2 swaps
  the renderer keeping this pipeline.
- Verification: gofmt clean; build/vet OK; focused -race green across
  attach/cmd; GOOS=windows build OK.

*(slice 2 pending)*

## Evidence — slice 2 (bubbletea program)

- Base: branch state after slice-1 evidence commit 94cdb66.
- Commits: build(deps) add bubbletea and lipgloss (18 authored lines;
  bubbletea v1.3.10 + lipgloss v1.1.0 pinned, 15 indirect modules recorded),
  feat(tui) attach model core with snapshot observation (+241),
  feat(tui) key routing, reconnect backoff, and admission serialization
  guards (+290), feat(tui) host action commands and deterministic run view
  rendering (+240), three test commits (+318/+293/+226), feat(sentinel)
  follow mode renders through the program (+163). model.go and model_test.go
  were split by cohesion before staging so every candidate passed the budget.
- Surface: internal/tui Model over the unchanged internal/attach pipeline;
  keys q/ctrl+c quit, r refresh, a abort (fresh idempotency identity per
  keypress mirroring the CLI twin), e respond with hand-rolled textinput
  sub-state (enter/esc/backspace/ctrl+u; bubbles dep deliberately avoided),
  y retry gated on Retryable() carrying ExpectedRevision = view.Revision
  (brief said Sequence — the implementer's correction is right and this
  record adopts it: controller_retry compares Revision); reconnect =
  exponential backoff 500ms doubling capped 8s, MaxReconnectAttempts 8,
  cursor-resumed resubscribe proven strictly-after; exhaustion freezes an
  honest lost-contact state exiting infrastructure; terminal projection
  stops polling while y-through-freeze remains available per the keyboard
  contract; ObserveSnapshot extracted once and shared by snapshot and TUI
  paths (snapshot rendering byte-neutral).
- Independent code review (dual axis): spec PASS including byte-compat proof
  and removed-test audit (each superseded slice-1 contract traced to a live
  equivalent); standards found one HARD defect fixed — reconnect tick
  multiplication (manual refresh or action failure during reconnecting
  scheduled competing backoff chains burning attempts ~2x) now guarded by a
  pointer-shared backoffTracker plus conn-state key gating, with new tests
  proving r-inertness and single-chain action failures; also applied: detach
  context threaded into observe/action commands, dead Changed field removed
  (reprint suppression became structural under bubbletea frame diffing —
  documented where the field lived), stale test comment renamed. Accessors
  RunID/Principal/Provider kept: cmd tests consume them.
- Honest limits: golden determinism currently relies on lipgloss lazy color
  detection — slice 3 must pin the color profile explicitly instead of
  trusting env (CLICOLOR_FORCE could force ANSI into goldens); real-signal
  detach still exercised via the cancellable-context seam; adaptersites
  anchor refreshed for this branch's own line drift (206->215) and disclosed.
- Verification: gofmt clean; build/vet OK; focused -race green across
  tui/attach/cmd; GOOS=windows build OK; FULL suite green.

## Evidence — slice 3 (goldens, harness, docs)

- Base: branch state after slice-2 evidence commit 8aa35b5.
- Commits: fix(execution) retry and recover cross-check durable truth over
  stale bookkeeping (+139 incl. adaptersites anchors), feat(tui) pinned-
  color golden views for six attach states (+261 across render.go, goldens,
  view footer fix), test(tui) real-daemon acceptance harness (+299/+177 —
  one rejected 459-line candidate split by cohesion), docs(runs) attach
  section (+64 informational), test(sentinel) drain seeded workers before
  teardown (+58).
- Surface: tui.RenderPlain pins lipgloss's color profile to termenv.Ascii
  for view-string production (termenv promoted indirect->direct; zero new
  modules); production interactive rendering keeps detection. Six goldens
  under internal/tui/testdata (running, awaiting_decision, succeeded,
  canceled_orphaned, reconnecting_attempt_3, lost_contact) with an escape-
  free tripwire (0x1b byte assertion) and an -update regeneration idiom.
  Harness: real foreground daemon.Run in a temp repo + RemoteHost provider +
  headless Model.Update driving — AC1 abort-through-keyboard lands durable
  cancellation evidence; AC2/AC4 forced-disconnect flips reconnecting and
  the resumed replay applies exactly-once (collector sequences equal a full
  fresh replay, strictly increasing); AC5 is the golden set itself.
  docs/runs-cli.md gains the attach section (modes, keys, exit contracts).
- Product race fixed beyond slice code (disclosed housekeeping): wire Retry/
  Recover could answer ErrRunNotActive from stale live bookkeeping while the
  durable head was already terminal (finish appends the terminal event
  before releasing worker state). Both guards now cross-check durable truth
  through one shared refusal decision; white-box deterministic pins added;
  the twin Recover guard was caught by the mandated stress and fixed under
  the same pattern. Stress: 15/15 parity rounds green under concurrent load;
  two consecutive full suites green after the drain audit below.
- Drain audit of every ticket-17 test (table in report): one offender fixed
  (attach model-construction fixture released+settled+quiesced), seedDurableRun
  hardened for all callers, harness tests quiesce before durable reads;
  stress -count=3 -race green under concurrent store/execution load.
- Independent review flags adopted: color profile pinned explicitly instead
  of env-trust; helpFooter no longer advertises keys that are inert while
  disconnected (its own contract violated pre-fix); goldens are escape-free
  rather than byte-ASCII because View() intentionally shares the CLI's UTF-8
  glyph vocabulary — accepted as consistency over purism.
- Verification: gofmt clean; build/vet OK; focused -race green; CLICOLOR_
  FORCE=1 + TERM=xterm-256color env-independence proven; GOOS=windows build
  OK; FULL suite green twice consecutively after the drain audit.

## Judgment Day closure

### Round 1 (initial dual judgment)

Ledger: confirmed-severe 1 (JD-D3-1 provider cross-closing, B=CRITICAL +
A=WARNING); single-judge warnings JD-D3-2 (terminal-boot never froze),
JD-D3-3 (terminal boot skipped detach watcher), JD-D3-4 (footer advertised
inert refresh). Fix round 1 executed all four; scoped re-judgment 1
confirmed D3-2/3/4 resolved but surfaced two residuals in the new caching
provider lifecycle: R1 (Judge B critical-rated: Reset-then-redial could
release a host under an in-flight exchange; test gap admitted) and R2
(Judge A warning: transport-level ACTION failures bypassed Reset/reconnect;
frozen sessions reused dead connections).

### Round 2 (final authorized round)

Fixes: poisoned-swap-close provider lifecycle (Reset marks poisoned without
I/O; Host dials fresh, swaps, then releases the poisoned host through its
own mutex-serialized Close — safety structural via RemoteHost exchange
serialization, adversarial in-flight test added proving release waits for
completion); connection-level action failures now route through
handleHostError semantics with frozen sessions unfreezing into reconnecting
on contact loss (semantic rejections stay status-line-only); deterministic
white-box pins added. Slice-plan human decision honored: bypass approved by
the operator for the unsplittable 458-line semantic unit (id
2f714488523686c4), committed through sentinel slice apply.

Scoped re-judgment 2: both judges converge on ONE residual —
**JD-D3-R3: the connection-level classifier in actions.go misses two
RemoteHost dead-connection surfaces** ("operation failed without a
classified error" and "cannot decode the <op> result", which markConnDead
proves unusable at client.go:252-265), leaving frozen sessions without
automatic reconnect until manual refresh on those paths. A=WARNING
(pre-existing surface), B=CRITICAL (behavior-activated by cached reuse).
Mechanism agreed; no contradiction. Fix budget exhausted (2/2 rounds,
2/2 re-judgments).

### Terminal state

**JUDGMENT: ESCALATED ⚠️** — escalated to the operator with the residual
recorded. Practical blast radius: narrow (requires an unclassified transport
failure exactly during a frozen-session action; recovery is one manual
`r` refresh). RESOLVED by operator decision (option 1): the classifier extension landed
as ordinary tracked work after escalation (fix commit classifying both
surfaces + table rows), closing the residual outside the judgment-day
budget with full regression coverage.

## Evidence — slice 3 (goldens, harness, closure)

*(pending)*
