# 10: Add Recovery And Reconciliation

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

### Slice 2 — deterministic rebuild and repair of terminal-unprojected
- Commits: repair primitive (131), refusal+determinism pins (310), CLI
  --repair (289), stale-snapshot crash-window polish (85) — hook-enforced.
- RepairTerminalUnprojected: full re-verification inside ONE lock hold
  (classify→verify→write→re-classify, TOCTOU closed), atomic write via the
  production temp+fsync+rename path, events bytes untouched (pinned), typed
  RecoveryNotRepairableError for every other class.
- ReplayProjection = single serialization source; byte-equality proven against
  twin stores with fixed timestamps (deterministic marshalRecord: declaration-
  order keys, scalar-only projection, UTC RFC3339Nano round-trip).
- CLI `runs recover --repair <id>`: prints class transition, exit 0 repaired /
  1 usage / 2 unknown / 4 refusals / 5 corruption; mutually exclusive with
  --run; repaired runs vanish from subsequent scans.
- eventLockWait const→var documented test seam; restore now t.Cleanup-based;
  no t.Parallel in repo so race-safe today.
- Integration: main gained D1 (repository host seam) and A1 mid-unit. Merged
  main cleanly (27f5e7a), renumbered ticket to 10 (2344d35). Semantic audit of
  the new API: lifecycle actions go through InProcessHost envelopes with
  resolveRunsPrincipal; pure store reads/maintenance stay direct — the scan
  and --repair paths follow exactly that split, so no envelope adaptation was
  required. Post-merge build/vet/windows/tests all green.
- Dual-axis loop: spec PASS (TOCTOU closed by single-lock-hold re-verification;
  byte determinism triple-proven; per-execution-dir locks isolate cross-run
  interference); standards CLEAN after fixes (stale-snapshot variant F1 added,
  fused comment N-2 moved, leak-proof N-4 cleanup).
- Accepted follow-ups: --expected-revision silently inert on scan/repair paths
  (reject explicitly in slice 3 docs pass); Rewritten=false branch defensive.
- Verification snapshot: gofmt empty; build+vet linux+windows; focused green;
  -race store clean; repair tests ×5 deterministic.

### Slice 3 — resume after owner loss and operator-required hardening
- Commits: relaunch machinery (97), evidence-naming reasons (17), e2e pins
  (221), CLI strictness + docs (241) — hook-enforced; worktree clean.
- Gap found and fixed: Controller.Recover/Retry refused the R7
  orphaned-canceled shape, contradicting "final unless the operator retries
  explicitly". Recovery now materializes ONLY the settlement the verified
  escalation transitions prove (never guessed; timestamp is recovery time;
  Error discloses reconciliation provenance) then relaunches through the
  existing NewRetryInvocation path — fresh invocation identity guaranteed by
  hash(lineage, attempt+1, retry); interrupted attempt's frames byte-preserved.
- Two-step append atomicity audited: crash windows land in honest states
  (settled-canceled re-recoverable via ordinary retry; unprojected repaired
  via --repair); CAS revision guards make duplicate settlements impossible;
  zero-frame race guarded fail-closed with typed error + focused subtest.
- Operator-required reasons name exact missing evidence per stream shape;
  CLI-level tree-digest zero-write pin.
- Strictness: --expected-revision rejected exit 1 on scan and --repair paths.
- Docs: runs-cli.md gains scan mode, classes table, exit-code rationale,
  --repair, JSON shapes, resume guarantee, torn-tail limitation cross-reference.
- Dual-axis loop: spec PASS 5/5 (crash-window honesty, no-fabrication gate tied
  to R7 windowing, identity collision impossible, revision travel correct);
  standards flagged B1 terminatingExtra — REFUTED by direct verification
  (symbol lives in slice-2's same-package test file; reviewer checkout stale),
  N1 guard added, two doc sentences added. Post-D1 envelope pattern respected:
  lifecycle writes stay inside Controller where all mutations live; host used
  for principal-stamped verification reads.
- Verification snapshot: gofmt empty; build+vet linux+windows; full suite green
  across 23 packages; -race clean on store/execution/cmd-sentinel; new tests ×5.

**Unit complete pending final verification snapshot.**
