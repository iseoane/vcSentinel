# The Production ACP/acpx Adapter

**Ticket:** 16 (`A2`), slice 3. **Audience:** operators configuring and
diagnosing `vassentinel.yml` agents that run through the ACP/acpx strategy.
**Design basis:** [`acpx-capability-mapping.md`](acpx-capability-mapping.md)
(A1 verdict `suitable-with-constraints`, constraints C1-C8).

## What it is

`internal/acpadapter` drives an ACP-capable agent through the `acpx` CLI as a
child process (`npx -y acpx@latest` by default) and normalizes its strict
NDJSON protocol stream into a single result. It is a second adapter family
beside the historical CLI adapters:

- Outcomes classify from the terminal result's `stopReason` alone
  (`end_turn` → success, `cancelled` → cancellation, anything else →
  failure). Exit codes are never authoritative: real backends exit 0 for both
  completed and cancelled turns.
- Non-JSON or oversized upstream stdout lines are skipped and counted as
  framing violations; they never fail the turn. stderr is non-authoritative.
- Effective identity is honest: the model comes from the initialize result's
  `configOptions[id=model].currentValue` when the backend exposes it
  (opencode does; claude-agent-acp does not), falling back to the configured
  echo, else empty. Effort is the configured echo or empty — never invented.
- Process ownership spans the deep spawn chain
  (`npx → node(acpx __queue-owner) → npm exec → node(<agent>-acp)`) through
  the owned-tree contracts, so controller escalation can terminate the whole
  tree.
- Review runs reuse the exact read-only snapshot discipline of the CLI
  adapters (shared `internal/reviewsnapshot` package): committed content
  only, materialized into an isolated directory passed to acpx via `--cwd`.

## Configuration example

```yaml
active_agent: claude-acpx
agents:
  claude-acpx:
    kind: acpx          # selects the ACP/acpx family (absence = CLI family)
    agent: claude       # acpx agent token: claude | codex | opencode | custom
    model: claude-sonnet-x        # configured echo, reported verbatim
    reasoning_effort: high        # configured echo, reported verbatim
    enforcement: claude-sandbox   # restriction backend declaration (C6)
```

Keys `kind`, `agent`, and `enforcement` are new for the acpx family. Any
agent entry without them parses and behaves byte-identically to before.
Unknown values are rejected at construction time — before any process is
launched.

## Enforcement matrix (C6)

| Declaration | Linux / macOS / WSL2 | Native Windows | Notes |
| --- | --- | --- | --- |
| *(absent)* or `none` | admitted, grant-by-design | admitted, grant-by-design | No restriction backend is promised. Nothing is rejected; nothing is guaranteed. |
| `claude-sandbox` | admitted | **rejected at construction** | Declares the project's Claude Code native sandbox settings as containment. Verified headless on Linux/WSL2 via acpx in A1 follow-up probes. There is no verified native-Windows containment, so declaring it there fails fast with a platform-limitation error; run under WSL2 instead. |
| any other value | rejected at construction | rejected at construction | Explicit error naming the known values. |

Backend-specific status:

- **opencode**: has no gating mechanism — probed ungated in A1. Use
  `enforcement: none` and treat runs as grant-by-design; declaring
  `claude-sandbox` with an opencode token fails construction before any
  process can start (admission fail-fast: that declaration applies only to
  the claude agent token, because the sandbox belongs to Claude Code
  settings), so a mismatched pair can never reach launch.
- **codex**: currently blocked by the `codex-acp -32000 Authentication
  required` gap (fails despite CLI login). Support is gated on that
  environment fix; contract tests run without live agents, so codex work
  proceeds in parallel. Tracked follow-up.

## Fallback ordering

- **Explicit selection** (recommended): set `active_agent:` to the acpx entry
  name, or name it in a review profile (`profiles.<name>.agent:`). Selection
  then always constructs the acpx family, regardless of PATH contents.
- **`active_agent: auto`**: adapters chain in yml/PATH order, and every entry
  must resolve a binary in PATH. An acpx entry participates only if its
  *entry name* resolves in PATH — the npx launcher itself is not consulted.
  Give the entry the real binary's name if you want auto to consider it, or
  prefer explicit selection to keep the ordering deterministic.
- Mixed chains are supported: each member keeps its own family, and fallback
  stays per-request.

## Diagnostics

- `sentinel runs status|logs|verify` — review-path outputs are hash-admitted
  as evidence by DurableTransport (R6), and runs-prompt durable requests
  record an `agent.enforcement` capability carrying the declared backend —
  except under a configured daemon endpoint, where admission stays in legacy
  explicit form by design (conservative relayed path; capability stamping
  applies to the in-process admission only). Raw NDJSON transcripts are
  retained programmatically on `*acpadapter.Result`
  (`RawStream`/`ObservedModel`/`StopReason`/`Violations`/`UsageJSON`); durable
  raw-transcript threading is an explicit follow-up gated on capability-policy
  runtime work (store schema).
- Outcome classes come from the terminal `stopReason`, never exit codes. A
  turn that streamed output but produced no terminal result classifies as
  failure (or timeout/cancellation when a budget fired).
- Framing violations (non-JSON/oversized stdout lines skipped while parsing)
  are counted per run and surfaced in the normalized result; a rising count
  usually means the upstream adapter is polluting its own protocol stream.
- `MaxOutputBytes` breaches end the turn deterministically as failure, even
  if the truncated stream claimed `end_turn` afterwards.

## Rollback

Remove `kind: acpx` (or point `active_agent` back at a plain agent entry):
the factory rebuilds the historical CLI adapter and behavior is unchanged —
pinned by tests asserting legacy entries still construct exactly as before.
The acpx code paths are otherwise inert for configurations that do not
declare the family.
