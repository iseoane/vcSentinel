# 09: Prototype ACP/acpx Capability Mapping (A1)

**What to build:** A throwaway experiment that determines whether ACP/acpx
(headless CLI client for the Agent Client Protocol) can satisfy the invocation,
permission, cancellation, streaming, and identity contracts of the durable-run
controller. The deliverable is a capability-mapping document, captured live
fixtures, a recorded protocol-gap list, and a suitability decision:
`suitable`, `suitable-with-constraints`, or `unsuitable`.

**Blocked by:** 04 (complete). Note: the worktree base already includes the R7
merge, but A1 deliberately targets the stable controller port from R3 and does
not depend on cancellation ownership contracts.

**Status:** in-review

**Scope (from roadmap):**

- map `CapabilityPolicy` (FilesystemRead, FilesystemWrite, Network, Shell,
  ChildAgents, InteractiveInput, MaxRuntime, MaxOutputBytes) to concrete
  acpx/ACP mechanisms or mark GAP;
- verify model, binary, session, and effective-capability identity reporting;
- test output streaming (`--format json --json-strict` NDJSON), cooperative
  cancellation, child processes, timeout, and failure classification;
- identify protocol gaps without modifying production review paths;
- record the suitability decision with cited evidence.

**Environment facts (verified):** node v25.9.0 + npm 11.12.1 present; `acpx`
not installed (use `npx acpx@latest`); locally installed ACP-capable agents:
claude, codex, gemini, opencode (native `opencode-ai acp` adapter). Live probes
run against real agents with cheap prompts.

**Acceptance criteria:**

- [x] Capability mapping table covers all eight policy fields with observed
      evidence or an explicit GAP entry.
- [x] Identity section documents what acpx/ACP exposes per invocation: adapter
      binary identity, session identity scope, effective model observability —
      compared against Sentinel's `AgenteEfectivo` contract.
- [x] At least one real turn captured as an NDJSON fixture proving structured
      streaming during a live session.
- [x] Cancellation proof: mid-turn `session/cancel` executed, client-side
      observation captured, process-tree fate documented.
- [x] Failure classification: exit codes exercised for at least missing
      session and one operational failure; stdout/stderr discipline noted.
- [x] Protocol gap list recorded with UNCONFIRMED items separated from
      confirmed findings.
- [x] No production path touched: diff limited to `.scratch/durable-runs/`,
      docs, and throwaway probe code.
- [x] Decision record states suitable / suitable-with-constraints / unsuitable.
- [ ] Independent `code-review` runs against the fixed base before closure.
- [ ] Judgment Day validates the decision record. User-directed override of
      matrix "No": this verdict gates A2 architecture, so it is treated as
      decision-grade even though A1 is not a critical implementation milestone.
- [ ] Rollback executed at closure: probe code deleted; decision record and
      fixtures retained.

**Out of scope:** production adapter (A2); any change under `internal/**` or
`cmd/**`; daemon/TUI/remote work; answering pending slice decisions on the
user's behalf.

**Suggested slices:**

1. Research synthesis + capability mapping draft (documentation only).
2. Live probe harness: sessions, permissions, streaming, identity fixtures.
3. Cancellation, timeout, failure probes; verdict; decision record; reviews.

## Evidence — slice 1 (research synthesis)

- Live empirical probes replaced the planned doc-only research pass; the
  delegated background research report was never returned and its loss was
  accepted because every roadmap claim below is backed by a captured fixture
  instead of documentation.
- Full CLI surface captured (`fixtures/help-top.txt`): permission modes,
  `--permission-policy` JSON fields (`autoApprove`, `autoDeny`, `escalate`,
  `defaultAction`), `--timeout`, `--ttl`, `--no-fs`, `--no-terminal`,
  `--suppress-reads`, `--non-interactive-permissions deny|fail`.
- Sentinel-side contracts read from source: `CapabilityPolicy` exists only as
  a roadmap semantic contract; identity contract `AgenteEfectivo{Binario,
  Modelo, Esfuerzo}` plus `modelprobe` mismatch probe in
  `internal/agentadapter/efectivo.go` / `internal/modelprobe/verificador.go`.

## Evidence — slice 2 (live probe harness and fixtures)

Environment: acpx@0.13.1 via npx, node v25.9.0, Linux; adapter
`npx -y opencode-ai acp`; observed effective model `opencode/big-pickle`.

- Streaming turn captured end-to-end (`fixtures/exec-stream.jsonl`):
  `session/prompt` echo, typed `session/update` events
  (`available_commands_update`, `agent_thought_chunk`,
  `agent_message_chunk`, `usage_update`), terminal `stopReason:"end_turn"`
  with full usage including `thoughtTokens`.
- Identity: initialize response carries
  `configOptions[id=model].currentValue` with model catalog
  (`fixtures/deny-probe.jsonl` line 1); live `status` exposes pid/model
  (`fixtures/status-running.txt`); session metadata records exit signal and
  `disconnectReason: process_exit` (`fixtures/sess-show3.txt`).
- Session scoping `(agentCommand, cwd[, name])` confirmed twice: scoped
  lookup failed from a different cwd, then succeeded in scope; missing
  session prompt exits 4 (`fixtures/no-sess-err.txt`).
- Post-review evidence repairs: `fixtures/initialize-model-option.json`
  extracts the initialize response's model config option verbatim (the raw
  line exceeds the redaction threshold inside both stream captures);
  `fixtures/event-type-index.txt` censuses observed session/update types per
  unredacted original; `fixtures/exit-codes.txt` records captured exit codes
  (4 missing session, 3 timeout, 0 cancelled, 0 completed).

## Evidence — slice 3 (cancellation/failure probes, decision, audits)

- Cancellation decisive probe (`fixtures/cancel2-stream.jsonl`,
  `fixtures/status2-running.txt`): mid-turn cancel while running produced
  `stopReason:"cancelled"`, zero usage, after 204 streamed lines; prompt
  process still exited 0 → outcomes must classify by stopReason.
- Timeout: exit 3 with actionable stderr (`fixtures/timeout-err.txt`).
- Deny-all enforcement without hang, tool lifecycle events visible
  (`fixtures/deny-probe.jsonl`).
- Upstream stdout framing violation observed live (opencode adapter emitted a
  non-JSON `[skill-registry]` line); acpx logged parse failures to stderr even
  under `--json-strict`, skipped the line, exited 0.
- Shared-store discovery: acpx/opencode sessions share native storage;
  historical sentinel-generated sessions appeared in the global list.
- Decision record committed as
  `docs/arquitectura/acpx-capability-mapping.md`: **suitable-with-constraints**
  with constraints C1-C6 mapped to A2 design obligations.
- Independent code-review (two-axis, base a2700f4): Standards 0 hard
  violations / 4 judgement calls; Spec found the initialize-citation defect
  (fixed via `initialize-model-option.json`), event-type misattributions
  (fixed via `event-type-index.txt`), and missing exit-code captures (fixed
  via `exit-codes.txt`). Remaining judgement calls accepted: orphaned
  numeric fixture suffixes kept because prose pairs them; identical oversized
  redacted lines across captures are the same initialize response recorded
  twice, not duplication of distinct evidence.
- Judgment Day on the decision record (user-directed): pending.
- Rollback at closure: probe scripts were never committed (sandbox only);
  fixtures redacted of oversized lines to avoid leaking global skill lists.
