# ACP/acpx Capability Mapping and Suitability Decision (A1)

**Ticket:** 09 (`A1: Prototype ACP/acpx Capability Mapping`)
**Status of this record:** decision-grade evidence; validated by independent
review plus Judgment Day at the user's explicit direction.
**Verdict:** `suitable-with-constraints`.

## 1. Scope and method

This experiment determines whether acpx (headless CLI client for the Agent
Client Protocol) can satisfy the durable-run controller contracts described in
`run-control-implementation-plan.md`: invocation envelope, permission policy,
cancellation, streaming, identity, timeout, and failure classification. It is a
throwaway prototype: no production review path was modified, and per the
rollback boundary only this record and the fixtures survive closure.

Method: live probes against real agents through `acpx@0.13.1` on node
v25.9.0 (Linux), primary adapter `npx -y opencode-ai acp`, observed effective
model `opencode/big-pickle`. Every claim below cites a fixture under
`.scratch/durable-runs/a1-acpx/fixtures/`. Facts we could not reproduce are
marked UNCONFIRMED rather than asserted.

## 2. CapabilityPolicy mapping

Sentinel's roadmap contract is deny-by-default with eight semantic fields.
acpx enforces permissions per tool call through modes and a JSON policy
(`--permission-policy` with `autoApprove`, `autoDeny`, `escalate`,
`defaultAction`; see captured CLI surface in `help-top.txt`). The mapping is
real but coarser than our semantics.

| Sentinel field | acpx/ACP mechanism | Observed evidence | Mapping verdict |
| --- | --- | --- | --- |
| `FilesystemRead` | `--approve-reads` auto-approves reads/searches; ACP filesystem capability advertised unless `--no-fs`; `--suppress-reads` redacts payloads | CLI surface fixture | Partial: tool-scoped, not path-scoped |
| `FilesystemWrite` | Write tool calls surface as permission requests; deniable via policy or `--deny-all` | `deny-probe.jsonl`: read request executed under deny-all without hang, exit 0 | Partial |
| `Network` | No network-specific permission class exists | Absent from full CLI surface and stream event types | **GAP**: cannot enforce |
| `Shell` | Terminal capability advertised unless `--no-terminal`; shell executions appear as `tool_call` events subject to permissions | CLI surface fixture | Coarse: presence toggle, not command scoping |
| `ChildAgents` | No visibility or control over sub-agents spawned by the coding agent | Not present in any probe | **GAP**: cannot enforce |
| `InteractiveInput` | Permission requests ARE the interactive channel; `--non-interactive-permissions deny\|fail` prevents hangs headlessly | CLI surface fixture | Mappable |
| `MaxRuntime` | Client-side `--timeout <seconds>`; hard cut observed | `timeout-err.txt`: "Timed out after 3000ms", exit 3 | Satisfiable client-side |
| `MaxOutputBytes` | Nothing at protocol level; consumer must cap while draining chunks | Stream fixtures show unbounded chunk flow | **GAP**: consumer-side only |

Consequence under the roadmap rule "reject adapters that cannot satisfy
required restrictions": any admitted `CapabilityPolicy` that requires Network
or ChildAgents restriction guarantees must make Sentinel reject the acpx
adapter for that run. This is decision-grade for A2 design.

## 3. Identity

- **Adapter/binary identity:** `sessions show` reports the exact launch
  command (`npx -y opencode-ai acp`), pid, `agentStartedAt`,
  `lastExitCode/lastExitSignal/lastExitAt`, and `disconnectReason`
  (`sess-show3.txt` shows `SIGTERM` / `process_exit`). Process death with
  signal is first-class state.
- **Session identity:** scope key is `(agentCommand, cwd[, name])`.
  Sessions require explicit creation (no auto-create; missing session exits 4,
  `no-sess-err.txt`). Named workstreams supported. Lookup is scoped while
  listing is global — an asymmetry adapters must respect.
- **Effective model:** the initialize response carries a `model` config
  option whose `currentValue` is the effective model plus the full catalog —
  extracted verbatim into `initialize-model-option.json`
  (`currentValue: "opencode/big-pickle"`, catalog of 395 options); live
  `status` independently exposes `model:` while running
  (`status-running.txt`). Stream chunks do NOT carry model attribution.
  Conclusion: `AgenteEfectivo.Modelo` is observable at session admission
  time, which satisfies H4/T0.2 requirements; per-chunk model attribution
  does not exist.
- **Reasoning effort:** no protocol equivalent observed. UNCONFIRMED whether
  `_meta` carries effort metadata for some adapters.

## 4. Streaming

`--format json --json-strict` yields pure NDJSON on stdout. Captured turn
shape (`exec-stream.jsonl`, `cancel2-stream.jsonl`):

1. echoed `session/prompt` request with session id and content blocks;
2. `session/update` notifications typed by `update.sessionUpdate`:
   `available_commands_update`, `agent_thought_chunk`, `agent_message_chunk`,
   `usage_update`, and — when tools run under a permission mode —
   `tool_call` / `tool_call_update`. Per-file census in
   `event-type-index.txt` (oversized raw lines are redacted inside the
   stream fixtures; the index was generated from the unredacted originals);
3. terminal result with `stopReason` (`end_turn`, `cancelled`) and usage
   including `thoughtTokens` and `cachedReadTokens`.

Constraint observed live: the upstream adapter once wrote a non-JSON line
(`[skill-registry] ...`) to its stdout. acpx logged
`Failed to parse JSON message` diagnostics to stderr — even under
`--json-strict`, which suppresses only acpx's own non-JSON stderr — skipped the
line, continued, and exited 0. Consumers must therefore tolerate framing
violations from upstream and treat stderr as non-authoritative.

## 5. Cancellation

Cooperative `session/cancel` works mid-turn. Decisive probe: long turn
launched, cancel fired while `status: running`
(`status2-running.txt`, pid 911344, model visible). Result:
`stopReason:"cancelled"` with zero usage after 204 streamed lines
(`cancel2-stream.jsonl`). An earlier late-cancel attempt finished normally
with `end_turn`, confirming cancellation competes with turn completion.

The queue-owner process persists after cancellation (default TTL 300 s,
`--ttl 0` keeps it forever): cancellation ends the TURN, not the process tree.

## 6. Timeout and failure classification

Observed exit codes:

| Exit | Meaning | Evidence |
| --- | --- | --- |
| 0 | success — INCLUDING cancelled turns | `exit-codes.txt`, `cancelled-turn-exit.txt` |
| 3 | client-side timeout | `timeout-err.txt`, `exit-codes.txt` |
| 4 | prompt without a session in scope | `no-sess-err.txt`, `exit-codes.txt` |
| 1 | lookup failures (`sessions show` absent), commander unknown-option errors | probe transcripts |

Classification constraint: outcome classification MUST read `stopReason`
from the terminal JSON-RPC result; exit codes alone conflate completed and
cancelled turns.

## 7. Process topology and ownership

Real spawn chain observed: `npx → node(acpx __queue-owner) → npm exec →
node(opencode-ai acp) → agent binary`. For R7 process-tree ownership this
means: deep trees with npx/npm intermediates, a queue owner that outlives
turns by design, and explicit lifecycle management required (`--ttl`,
explicit session close/shutdown). `disconnectReason`/exit-signal metadata
supports orphan accounting.

## 8. Shared session store warning

acpx sessions for the opencode adapter share the agent's native session
storage: the global list surfaced dozens of pre-existing Sentinel-generated
OpenCode sessions (`/tmp/vas-sentinel-commit-*`) alongside this experiment's
probes. Production use would interleave machine sessions with human sessions
in the same history. A2 needs dedicated cwd/naming conventions for
sentinel-owned sessions and must accept shared visibility.

## 9. Confirmed gaps vs UNCONFIRMED

Confirmed gaps: Network restriction, ChildAgents visibility/control, output
byte caps, reasoning-effort reporting, path-scoped filesystem permissions.

UNCONFIRMED (not probed): `_meta` extension surface, session replay across
adapter restarts, Windows behavior of the spawn chain, other agents'
adapters (only opencode exercised live).

## 10. Decision

**`suitable-with-constraints`.** acpx/ACP satisfies the invocation envelope,
streaming, cancellation, timeout, and identity contracts needed by the
durable controller. It is fit as an optional production adapter strategy for
A2 provided every constraint below becomes an explicit A2 design obligation:

- **C1 — Identity at admission:** capture model from the initialize
  `configOptions` (`initialize-model-option.json`) or live `status`; no
  per-chunk attribution exists.
- **C2 — Framing tolerance:** filter oversized/non-JSON upstream stdout
  lines; treat stderr as non-authoritative even under `--json-strict`.
- **C3 — stopReason-based outcomes:** classify completion/cancellation from
  the terminal result, never from exit codes.
- **C4 — Queue-owner lifecycle:** define TTL policy and explicit shutdown;
  process ownership must span the deep npx/npm/node chain.
- **C5 — Store sharing:** sentinel-owned session naming and cwd scoping;
  accept interleaved human history in the shared store.
- **C6 — Enforcement honesty:** reject runs whose admitted policy requires
  Network or ChildAgents restrictions; enforce byte caps consumer-side.

Rollback executed per boundary: throwaway probe code was never committed;
fixtures and this record are the surviving artifacts.
