# Slice 14 — Readable Operations & Activity Column Headers

## Goal
The ACTIVITY pane must say WHAT each run is ("review", "gate pre-push",
...) instead of a bare sha, and must label its columns. The operation
name is currently unknowable after start because the store persists
only derived hashes; we plumb an operator-facing label additively
through the exact evolution pattern the store already documents.

## Scope
A) internal/store — additive label at admission:
   - `RunPolicy` gains `Operation string json:"operation,omitempty"`.
     Document it next to ParentRunID's precedent: old records never
     carry it, old readers ignore unknown keys, omitempty keeps legacy
     persisted bytes identical. CreateRun already marshals the whole
     policy into policy.json — no schema work beyond the field.
   - New accessor `ReadRunOperation(runID string) (string, error)`:
     reads policy.json and returns Operation (empty when absent).
     Missing run => the same not-found error vocabulary other readers
     use.
B) Producers set honest labels (inspect each site first; keep labels
   lowercase, no invented data):
   - cmd/sentinel/review_transport.go root + chained children:
     "review" plus the dimension/segment information that site can
     honestly access (if dimensions are not reachable there, plain
     "review" and note why in the ticket).
   - internal/gate/gate_durable.go root: "gate <stage>" using the stage
     the gate is running under; children: the most specific honest
     label available at :237.
   - cmd/sentinel/comandos_runs.go generic starts (~:330): "run".
C) internal/presence — surface it:
   - `RunSummary` gains `Operation string`; RecentRuns fills it via
     store.ReadRunOperation (one extra small read per summary, limit 3
     — acceptable; document). Failures keep the existing per-run error
     wrapping style.
D) internal/tui/art — readable rows + headers:
   - Under the " ACTIVITY" heading, one dim caption row aligned to the
     real columns: blank icon cell, FLOW(16), STAGE(20), STATE(9),
     AGE. Rendered for both mock and real panes? NO: mock stays
     byte-identical (visual contract); captions render only in the
     real-data pane path.
   - Run rows: flow = fitRunes(Operation, 16) when Operation != "",
     else today's 12-rune id prefix; stage stays "rev N"; detail line
     unchanged (full id lives there). Summary rows unchanged.

## Non-goals
- No retroactive labeling of the 167 existing runs (they render the id
  fallback by design); no prompt/candidate content anywhere near disk;
  no changes to hashing or identity; "/" filter still future work.

## Acceptance
- store tests: round-trip CreateRun+ReadRunOperation; empty Operation
  omitted from bytes (golden-ish assertion on policy.json content);
  missing run error vocabulary.
- presence test: Operation surfaced through RecentRuns; absent =>
  empty string.
- art tests: caption row present once under ACTIVITY with correct
  column anchors; flow uses operation when present, id fallback when
  not (both pinned); parity incl. caption row across widths; mock
  goldens untouched.
- Full verification battery green; bypass recorded if indivisible.

## Review decisions
- Budget bypass granted by standing operator authorization: the label
  plumbs store -> producers -> presence -> render as one unit (548
  lines); the guardian plan decision 05ebfbf7a4d653a0 resolves as
  bypass.
- Review runs are labeled plain "review": dimensions arrive per-Run()
  after admission, so per-dimension labels would require redesigning
  the immutable admission contract (verified against the transport
  flow).
- Operation labels sanitize control characters at the render boundary
  (operator-supplied command text must never split or recolor rows);
  column widths deduplicated into shared constants.
- ReadRunOperation treats decode-but-empty shape as legacy-empty, not
  corrupt: presence degrades softly either way; divergence from
  ReadExecutionRequest accepted and documented.
- Known cosmetic: the focused row's "▸" prefix shifts its columns one
  rune off the caption line (pre-existing affordance); left as-is.
