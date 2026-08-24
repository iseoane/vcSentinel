# 16: Add The Production ACP/acpx Adapter (A2)

**What to build:** ACP/acpx becomes an optional production adapter behind the
same admitted invocation envelope and durable lifecycle semantics as the CLI
adapters, serving the three production output agents (claude, codex,
opencode). The adapter normalizes ACP protocol events inside its own package
boundary, records honest identity and evidence, supports cancellation through
the R7 ownership contracts, and refuses runs whose capability policy demands
restrictions no available backend can enforce.

**Blocked by:** 07 (R6, complete), 08 (R7, complete), 11 (A1 decision,
complete).

**Status:** closed

**Audit:** Critical milestone — independent `code-review` plus Judgment Day
are mandatory before closure.

**Design contract (agreed analysis, from A1 evidence):**

- New package `internal/acpadapter`; nothing under `internal/agentrun`,
  `internal/execution`, or `internal/store` learns any ACP concept.
- The adapter drives the `acpx` CLI (`--format json --json-strict`, global
  options BEFORE the agent token) as a child process, parses strict NDJSON,
  and maps outcomes by terminal `stopReason` (`end_turn` → success;
  `cancelled` → canceled class), never by exit code (both exit 0 live).
- Framing tolerance (C2): non-JSON or oversized upstream stdout lines are
  skipped and counted, never fatal; stderr stays non-authoritative.
- Identity (C1/C8): effective model comes from the initialize result
  `configOptions[id=model].currentValue` when the adapter exposes it
  (opencode does; claude-agent-acp 0.60.0 does NOT — verified); fallback is
  the configured echo, else empty. `AgenteEfectivo.Esfuerzo` is never
  fabricated: configured value or empty. Binary identity composes the
  launcher plus agent token.
- Capability admission (C6): policies demanding filesystem/network/
  child-agent RESTRICTION guarantees are admitted only with a declared
  enforcement backend able to guarantee them on the current platform
  (verified: claude native sandbox via project settings works headless on
  Linux/WSL2; unsupported on native Windows; opencode has none — probed
  ungated). Otherwise reject before launch or run grant-by-design declared
  in the durable record. `MaxRuntime` maps to `--timeout`;
  `MaxOutputBytes` is enforced consumer-side while draining chunks.
- Process ownership (R7): the adapter implements `TreeProvider`
  (`OwnedTree() *process.Tree`) across the real spawn chain
  npx → node(acpx __queue-owner) → npm exec → node(<agent>-acp);
  cooperative cancel goes through `session/cancel` semantics of acpx
  (`<agent> cancel`) with bounded escalation inherited from the controller.
- Codex status: `codex-acp` currently fails with `-32000 Authentication
  required` despite CLI login — support is gated on that environment fix;
  contract tests must not depend on live agents (fake `acpx` via the
  helper-process pattern), so codex work proceeds without it.
- Raw stream bytes are retained for hash admission consistent with R6
  DurableTransport evidence handling.

**Acceptance criteria:**

- [x] `AcpxAdapter` executes prompts through acpx and returns normalized
      output assembled from `agent_message_chunk` text.
- [x] Terminal `stopReason` alone decides success vs canceled vs failed;
      pinned by table tests including the both-exit-0 trap.
- [x] Non-JSON and oversized stdout lines are skipped, counted, and reported
      without failing the turn.
- [x] `AgenteEfectivo` reports launcher+agent binary, observed-or-configured
      model, and never invents effort.
- [x] Admission rejects restriction-demands without a capable declared
      backend before spawning; accepted runs record the enforcement
      declaration.
- [x] Context cancellation triggers cooperative cancel and owned-tree kill
      across the deep spawn chain; late output cannot re-settle (JD-A1-style
      regression proof). *(Amendment, recorded at closure: delivered as ctx
      containment kill across the owned registry plus controller escalation
      authority — cooperative session/cancel is documented N/A for stateless
      exec; late-output non-resettlement is guaranteed by the controller's
      settlement authority proven in R7.)*
- [x] Config selects the adapter explicitly (kind: acpx + agent token +
      enforcement declaration); absence changes nothing for existing users.
- [x] Contract tests compare prompt/review shapes against `CLIAdapter`
      behavior using the fake acpx runner; A1 committed fixtures serve as
      golden transcripts where shapes match.
- [x] Operator documentation covers fallback ordering, diagnostics, and the
      codex auth blocker.
- [x] Build, vet, full tests, guardian, independent code-review, and
      Judgment Day recorded here before closure.

**Out of scope:** building OS sandbox infrastructure (deferred decision);
changing CLI adapter behavior; daemon/TUI surface; remote hosts; solving the
codex-acp authentication gap (tracked follow-up).

**Suggested slices:**

1. `internal/acpadapter` core: spawn construction, strict NDJSON parser with
   framing tolerance, stopReason classification, identity capture, raw-stream
   retention; golden-fixture and helper-process tests. No wiring.
2. Review-path integration: prompt/review/contextual methods with snapshot
   discipline parity, cancellation + OwnedTree across the spawn chain,
   MaxRuntime/--timeout and MaxOutputBytes caps.
3. Config/factory wiring, C6 admission validation, operator docs, CLIAdapter
   parity contract tests.
4. Full verification, independent code-review, Judgment Day, closure.

## Evidence — slice 1 (adapter core)

- Implemented by the delegated implementer, verified independently by the
  orchestrator: `internal/acpadapter/{adapter,parse,identity}.go` plus
  `adapter_test.go` and three golden fixtures; no wiring touched.
- Verification battery: gofmt clean; `go build ./...` OK; `go vet` OK;
  focused suite `-count=1` + `-race` OK; FULL `go test ./...` green after one
  forced remediation — `TestAdapterExecutionSitesInventory` required
  registering the new spawn site in `internal/adaptersites/inventory.go`
  (ClassShared, same seam class as `cli.go`; declarative metadata, not
  wiring).
- Dual-axis review of the staged diff (base 4058a04):
  - Spec: contract claims verified point-by-point (flag order pinned,
    exit-code ignored via pure Classify, violations non-fatal, identity
    precedence observed>configured>empty, RawStream verbatim). Findings:
    golden fixture invented a model value → replaced with the live-verified
    `opencode/big-pickle` (provenance: A1 `initialize-model-option.json`);
    adaptersites registration flagged as out-of-slice → justified in-place
    as verification-driven (suite-green requirement).
  - Standards: 2 hard findings fixed — new-code identifiers were Spanish per
    the orchestrator's own spec error (`AgenteEfectivo{Binario,Modelo,
    Esfuerzo}` → `EffectiveAgent{Binary,Model,Effort}`, method
    `EffectiveIdentity()`, `Empty()`; JSON tags unchanged agent/model/
    effort); path construction now `filepath.Join`. Also applied:
    `-test.run=^...$` helper anchor, and `readBoundedLine` now drops
    post-cap bytes so memory stays bounded on pathological lines.
- Honest incident record: during review-fix application the orchestrator
  overwrote `parse.go` with misdirected content; the file was reconstructed
  from spec and validated by the pre-existing test battery as behavioral
  oracle. One real defect was caught and fixed in that reconstruction
  (`chunkProbe` decoded the params wrapper instead of its contents).
- Slice volume 892 authored lines [CRITICO] → committed through the
  sanctioned slice plan/apply flow.


## Evidence — slice 2 (review path, cancellation, ownership)

- Implemented by the delegated implementer; verified independently by the
  orchestrator (build/vet/race on touched packages + full suite).
- Delivered: `review.go` (EjecutarPrompt / EjecutarRevision /
  ReviewWithContext; owned-spawn core via `process.Spawn` with containment
  watchdog mirrored from cli_review_context.go); `adapter.go` gains
  MaxOutputBytes, atomic OwnedTree publication, `--cwd` insertion;
  helper fake extended (sleep-long, oversized-output, arg-recording modes);
  exported pure-delegation wrapper `agentadapter.CreateReviewSnapshot`
  chosen over duplication/relocation to preserve snapshot-discipline parity.
- Cancellation semantics pinned: caller-cancel → cancellation class wrapping
  ctx err; runtime deadline only → timeout class; cooperative session/cancel
  documented out of scope (stateless exec; ctx kill + controller escalation
  covers it). MaxOutputBytes breach → failure class even when truncated
  stream contained end_turn (classification checks exceeded first).
- Dual-axis review findings applied: stale inventory anchor corrected
  (Symbol AcpxAdapter.Command @190 after spawn moved to review.go; line
  re-pinned after comment insertion shifted EjecutarPrompt); no-mistakes
  string-matching assertion removed from budget-breach test (class
  assertion suffices); interface-conformance exception documented on the
  legacy Spanish method names (structural AuditorAgente contract);
  snapshot errors now carry the acpx detail prefix.
- Accepted low-severity follow-ups (parity-inherited): tree publication
  window identical to cli.go; cancel-during-grace can flip timeout→canceled
  label (evidence labeling only); extract watchdog into process package next
  time it is touched; surface stderr in failure details; drop vestigial
  spawnHelper nil-error signature.
- Verification: gofmt/vet/build OK; focused + `-race` OK; FULL suite green
  (zero FAIL). Volume 690 authored [CRITICO] → committed through the
  sanctioned slice plan/apply flow.


## Evidence — slice 3 (wiring, admission, docs, parity)

- Implemented by the delegated implementer; verified independently.
- Delivered: config schema `kind: acpx` + agent token + enforcement
  declaration (legacy configs pinned byte-identical at parse AND construction
  level); factory wiring via `AcpxBridge` (satisfies existing structural
  contracts, refuses plain commit path like claude/opencode CLIs, forwards
  honest identity); C6 admission at construction with platform checks;
  snapshot discipline relocated to neutral leaf `internal/reviewsnapshot`
  (import-cycle-free home; delegation shims keep CLI behavior untouched);
  parity contract tests via fake helper (no live agents); operator doc
  docs/arquitectura/acpx-production-adapter.md (enforcement matrix,
  fallback ordering, codex auth blocker, rollback).
- Dual-axis review adjudications: relocation verified token-for-token
  identical to the previous discipline; import-cycle claim confirmed real;
  bridge judged a justified boundary (real adaptation work, not a middle
  man); doc statements verified against code (no doc lies).
- Review findings fixed in this slice: new Spanish identifier renamed
  (`commitLanguageOrDefault`); **C6 pairing gap closed** — enforcement
  claude-sandbox now requires agent token claude, pinned by fail-fast test
  row (previously claude-sandbox+opencode constructed fine); reviewsnapshot
  gained its own SafePaths filter table + Create happy/error/cleanup tests
  (security-relevant filter no longer testless at its new home).
- Known issue recorded (NOT this slice's regression): internal/daemon
  wire-parity tests flake intermittently under full-suite load; reproduced
  at base HEAD with diff stashed; passes isolated and -count=3. Follow-up:
  stabilize daemon wire-parity tests.
- Verification: gofmt/vet/build OK; focused -race OK; full suite green
  modulo the documented daemon flake; inventory anchors refreshed after
  line shifts. Volume 993+ authored [CRITICO] → committed through the
  sanctioned slice plan/apply flow.


## Evidence — slice 4 (verification, reviews, closure)

- Final battery on the complete unit: gofmt/vet/build OK; FULL `go test
  ./...` green with the documented pre-existing daemon wire-parity flake
  passing isolated and `-count=3` (follow-up recorded below); guardian
  measured per-slice and enforced via plan/apply throughout.
- Independent review mapping: slices 1-3 each received dual-axis (Standards +
  Spec) review over their real staged diff with findings fixed before their
  commits — collectively covering 100% of the ticket's real diff. Judgment
  Day then ran as the stronger adversarial audit on the frozen full unit.
- Judgment Day (mandatory, critical milestone): snapshot of dace16e exported;
  round 1 produced CRITICAL JD-E (both judges — evidence/enforcement dropped
  before durable visibility), plus single-judge CRITICALs JD-R (runs-abort
  blind to acpx trees) and JD-C (concurrent tree publication loss), verified
  by the orchestrator as third reader; warnings JD-D (stale doc pairing
  claim) and JD-M (budget over-retention). Round-1 fixes: enforcement
  retention + accessor + Result.Enforcement + bridge String(); context-aware
  prompt path with bridge forwarding and promptRunAdapter ctx/OwnedTree
  forwarding (cmd-level pins); mutex-guarded start-ordered tree registry
  with interleaving proof; doc pairing rule; exact budget truncation.
  Re-judgment 1: all fixes verified correct by both judges; converged
  remainder held CRITICAL (durable records carried only output; doc
  overclaimed raw-stream-hash durability). Round-2 fixes: runs-prompt
  admission stamps an `agent.enforcement` capability via the sanctioned
  NewCapability mechanism at runStartCommand→ResolveAdmissionRequest (empty
  declarations skipped, canonical-form promotion only without a daemon
  endpoint, non-acpx delegates byte-identical, pinned by two new tests);
  doc overclaim replaced with exact three-part behavior. Re-judgment 2
  (final): ZERO criticals from both judges; residue = one WARNING on doc
  precision (daemon-relayed admissions deliberately omit stamping) —
  accepted as info and corrected in this closure commit.
- Accepted follow-ups: durable raw-transcript threading gated on
  capability-policy runtime work (store schema); stabilize daemon
  wire-parity tests under load; extract containment watchdog into process
  package; solve codex-acp -32000 auth gap; surface stderr in failure
  details; simplify vestigial spawnHelper signature; consolidate adapter-
  family dispatch when a third kind appears.

**Status:** closed

**JUDGMENT: APPROVED** (round-2 re-judgment, both judges zero-critical;
one doc-precision WARNING accepted and fixed in closure).
