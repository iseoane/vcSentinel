# 09: Add Recovery And Reconciliation

**What to build:** Interrupted durable runs are found, classified, and recovered
without fabricating completion or discarding failed invocations. One command
surfaces every non-terminal run with an evidence-based class — recoverable,
terminal-but-unprojected, corrupt, or orphaned — and recovery resumes only
operations whose contracts are idempotent, always under a fresh invocation
identity, and never by guessing an outcome the stream does not contain.

**Blocked by:** 06 (complete, merged at d0f9a3f) and 08 (complete, merged at
a2700f4).

**Status:** ready-for-agent

**Design contract (agreed analysis):**

- Scanning is explicit and read-only by default: `sentinel runs recover`
  without `--run` lists every non-terminal run in the store with its derived
  class and the reason; it writes nothing. Recovery of a specific run stays an
  operator action (`--run <id>`), preserving today's refuse-or-resume
  semantics built on `Controller.Recover`.
- Classification derives from verified event streams only, extending the R7
  read-time reconciliation instead of duplicating it:
  - recoverable: awaiting-decision heads whose recorded decision evidence is
    intact (today's resumable case);
  - terminal-but-unprojected: a valid terminal frame exists but the persisted
    state snapshot lags it — repaired by rebuilding the projection from the
    verified stream, never by inventing frames;
  - corrupt: hash-chain breaks, invalid transitions, unreadable tails
    (`IncompleteEventTailError` and friends) — reported with the exact defect,
    never silently repaired;
  - orphaned-canceled: the R7 reconciled view (owner died mid-cancellation),
    which is final unless the operator retries explicitly.
- Projection rebuild is deterministic: replaying the validated stream through
  the existing projection machinery must reproduce byte-identical state for
  healthy streams; rebuild only rewrites the lagging snapshot file when the
  stream verifies, leaving events untouched (append-only preserved).
- Resume-after-owner-loss creates a NEW invocation identity via the existing
  retry/recover machinery — the interrupted attempt keeps its failed/unknown
  record; nothing claims the old attempt completed.
- Operator-required states beat guesses: when evidence cannot decide between
  outcomes (e.g., provider may have side-effected but no terminal frame
  exists), the run is surfaced as requiring an operator decision with the
  exact missing evidence named — no automatic fabrication, no silent drop.
- Idempotence gate: recovery offers resume only where the adapter contract is
  idempotent (review dimension audits are); anything else lands in
  operator-required with the contract gap stated.
- Additive compatibility: no JSON tag changes to persisted structs; new
  surfaces are additive fields/commands; old stores without recovery metadata
  scan cleanly.

**Acceptance criteria:**

- [ ] Focused tests prove the scan lists every non-terminal run with the right
      class and writes nothing (byte-stable store during scan).
- [ ] Focused tests prove each class: recoverable awaiting-decision;
      terminal-but-unprojected repaired by verified rebuild; corrupt reported
      with exact cause (bad hash, invalid transition, truncated tail);
      orphaned-canceled carried from reconciliation.
- [ ] Focused tests prove projection rebuild reproduces byte-identical state
      for healthy streams and repairs only the lagging snapshot after a
      verified terminal frame.
- [ ] Focused tests prove resume-after-owner-loss uses a fresh invocation
      identity and preserves the interrupted attempt's record.
- [ ] Focused tests prove operator-required surfacing names the exact missing
      evidence and performs no write.
- [ ] Tests prove `runs recover --json` exposes the classification machine-
      readably with stable shapes.
- [ ] Tests prove additive compatibility: stores from previous units scan and
      operate unchanged; legacy `recover --run` behavior intact.
- [ ] The implementation records build, vet, tests, guardian, independent
      `code-review`, rollback boundary, and follow-ups here.

## Evidence

*(appended per slice)*

## Follow-ups

*(recorded at closure)*

### Slice 1 — classification machine and read-only scan
- Commits: classifier (212), classification pins (400), scan CLI (77), CLI
  contract pins (117) — all hook-enforced.
- Total classifier in internal/store: recoverable / terminal-unprojected /
  corrupt (exact error text) / orphaned-canceled (reuses R7 windowed
  reconciliation) / operator-required / settled (totality value, excluded
  from scan). Fixed precedence: corrupt beats all; orphaned beats awaiting;
  terminal head + broken tail stays corrupt (no dishonest "verified" claims).
- ScanRecoveries is pure read path: no locks, no writes, byte-stability proven
  via SHA-256 tree digests before/after every scan test; stray dirs skipped
  like ListExecutionIDs.
- `runs recover` without --run lists the table; exit 0 empty, exit 4 iff any
  operator-required row; --run path byte-identical; JSON via existing stable
  convention with [] never null.
- Dual-axis loop: spec PASS 5/5 (adversarial totality hunts clean; write-order
  analysis proves crash-between-append-and-snapshot lands unprojected not
  corrupt; status vs recover share the same reconciliation windowing);
  standards CLEAN with 3 accepted notes folded into slice 3: docs/runs-cli.md
  must document the new shape + scan mode + corrupt-exit-0 rationale;
  --expected-revision without --run should be rejected explicitly.
- Verification snapshot: gofmt empty; build+vet linux+windows; focused suites
  green; -race store clean; scan/recovery tests ×10 deterministic.
