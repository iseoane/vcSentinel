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

**Status:** ready-for-agent

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

- [ ] `AcpxAdapter` executes prompts through acpx and returns normalized
      output assembled from `agent_message_chunk` text.
- [ ] Terminal `stopReason` alone decides success vs canceled vs failed;
      pinned by table tests including the both-exit-0 trap.
- [ ] Non-JSON and oversized stdout lines are skipped, counted, and reported
      without failing the turn.
- [ ] `AgenteEfectivo` reports launcher+agent binary, observed-or-configured
      model, and never invents effort.
- [ ] Admission rejects restriction-demands without a capable declared
      backend before spawning; accepted runs record the enforcement
      declaration.
- [ ] Context cancellation triggers cooperative cancel and owned-tree kill
      across the deep spawn chain; late output cannot re-settle (JD-A1-style
      regression proof).
- [ ] Config selects the adapter explicitly (kind: acpx + agent token +
      enforcement declaration); absence changes nothing for existing users.
- [ ] Contract tests compare prompt/review shapes against `CLIAdapter`
      behavior using the fake acpx runner; A1 committed fixtures serve as
      golden transcripts where shapes match.
- [ ] Operator documentation covers fallback ordering, diagnostics, and the
      codex auth blocker.
- [ ] Build, vet, full tests, guardian, independent code-review, and
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

*(pending)*

## Evidence — slice 3 (wiring, admission, docs, parity)

*(pending)*

## Evidence — slice 4 (verification, reviews, closure)

*(pending)*
