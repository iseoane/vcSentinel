# Future — deliberately postponed

Each item names the condition that brings it forward. Nothing here is
forgotten; everything here waits on something stated.

## Shared note: token observability (updated 2026-09-06)

Tokens used to be the missing measurement behind FU-3 (cost attribution,
in `actionable.md`) and the shared-evidence cache (also in
`actionable.md`): no adapter reported token usage or price on the wire,
identity coverage measured 0 of 325 in the FU-9 store scan, and any value
written without a producer would be invented.

That is no longer true for tokens. All three adapter paths now report
token usage on the wire and carry it into the durable metrics: ACP/acpx
from the terminal result's usage member; direct opencode (`--format json`)
as input, output, total, cached-read and reasoning tokens summed over
every step_finish event; direct claude (`--output-format json`) as input,
output, cached-read and reasoning tokens, with Total always nil because
the wire carries no total member. Both direct paths retain the raw usage
evidence on the result (`UsageJSON`).

FU-3 stays blocked, narrowed to price, scope and reuse. Price has an
observed carrier on both direct wires (claude's `total_cost_usd`,
opencode's per-step `cost`) but deliberately no Usage destination and no
pricing table — the F9 precedent: a recorded determination that the value
stays nil with provenance, not a gap to fill by estimate. Scope and reuse
remain sourceless. The shared-evidence cache item's token prerequisite is
satisfied by the producers above; its remaining closing conditions
(snapshot, cache key, equivalence measurement) are its own, in
`actionable.md`.

## D4: remote host contract

- Postponed: mutual auth, repository/run authorization, disconnect
  ownership transfer, and trust boundaries for code, prompts, credentials
  and evidence.
- Comes forward when: a concrete operator use case exists for running
  durable runs on another machine. None exists today; the local daemon
  plus attach TUI cover current needs.
- Cheap to keep parked: the architecture is already prepared
  (transport-neutral six-op RepositoryHost, JSON envelopes, cursor replay,
  idempotency identities). Origin: roadmap D4.

## D5: complete remote control

- Postponed: remote adapter/host implementation, partition reconnect,
  hostile-network tests.
- Comes forward when: D4 has landed AND an approved threat model exists.
  The R10 dependency is already satisfied. Origin: roadmap D5.

## Admission upgrade via native sandboxes

- Postponed 2026-09-05 (reclassified from P1 to parked): codex reviews
  admitted with `mode=read-only`, claude via project sandbox settings
  including `denyRead`, opencode grant-by-design declared.
- Comes forward when: an adapter exposes a path-scoped search permission,
  or reviews start running on untrusted third-party content.
- The gap stays documented and unmitigated: the enforcement is
  adapter-native, so it can only be requested from each adapter and
  verified, not built here. Live probe 2026-08-26 showed OpenCode
  `grep`/`glob` permission rules matching search expressions rather than
  searched paths, so injected content can point them at arbitrary host
  paths; `read` is exactly confined and Claude confines `Grep`/`Glob` by
  path.

## FU-1: Spanish strings sweep in production surfaces

- Postponed by decision 2026-09-05: post-legacy production code still ships
  user-facing Spanish strings (instances recorded in
  `internal/gate/gate_durable.go`, `internal/setup/uninstall.go` and the
  validation orchestration messages), against the language policy for new
  artifacts.
- Comes forward when: a unit already opens one of the listed files, or a
  decision makes the policy externally visible.
- Coordinate with FU-2 when it moves: both touch the same files.

## FU-4: producer-seam structural debt

- Postponed by decision 2026-09-05: the T9.1b producer seam reverses the
  dependency direction (the domain engine imports `internal/acpadapter`
  and must change per provider result shape), metrics finalization crosses
  into review as a callback, `observedAgent` owns two capability descents
  that can diverge, and `internal/review/diagnostic.go` reads an agent
  CLI's unstructured terminal presentation inside the domain layer.
- Comes forward when: the unit adding a third adapter kind performs this
  restructuring as part of its own work. Restructuring now would rewrite
  the most delicate piece of the system for no observable benefit, and
  paying for it twice is the outcome the trigger exists to avoid.

## Record the reviewer turn count in the legacy review ledger

- Postponed by decision 2026-09-08: the recalibration of the OpenCode
  reviewer turn budget (`actionable.md` item 2) only needs the consumed
  turn count on the durable path. Also writing it through
  `internal/review`'s append-only ledger, so a direct `sentinel review`
  fed the same sample, was considered and rejected.
- Measured reason for rejecting it: the durable path already produces the
  sample faster than the decision needs. Counting `"stop_reason":"end_turn"`
  occurrences in `events.jsonl` across this repository's durable store
  (one per invocation; the `outcomes/` copies are indented and do not
  double-count) gave 0 on 2026-09-05, 0 on 2026-09-06, 93 on 2026-09-07
  and 51 on 2026-09-08 — 144 completing invocations in the two days since
  the snapshot began carrying the whole committed tree. A stable percentile
  needs tens, not hundreds.
- It also adds no evidence: `sentinel review` and `sentinel runs` traverse
  the same `CLIAdapter`, the same `ToolPolicy` and the same prompts, so the
  turn-consumption distribution is one population sampled twice, not two
  samples.
- Comes forward when: the turn count is wanted as per-review observability
  in `status` and the fichas for debugging a single review without going
  through `runs`. That is an observability goal of its own, not part of the
  budget recalibration, and it is the only benefit this item still carries.

## Adapters and ACP for codex and pi

- Postponed by decision 2026-09-08: semantic review supports exactly two
  providers with path-confined tool permissions. `reviewCommand` builds an
  OpenCode invocation or a Claude one and rejects everything else with
  "semantic review is unavailable: path-confined tool permissions are not
  configured for this provider". Both codex and pi are installed and driven
  daily on this machine, and neither can act as a reviewer.
- Why it is worth more than provider variety: a day of heavy reviewing showed
  that three of the four recurring failure classes are properties of the
  chosen provider, not of this repository. The Bun runtime OpenCode ships
  leaves a ~14 MB temporary file per invocation that nothing removes (540
  files, 2,945 MB measured); OpenCode's own SQLite state raced itself with
  `CREATE TABLE workspace` whenever concurrent dimensions shared a home, which
  is the reason provider isolation had to be built at all; and its agent-level
  `"*": "ask"` rule with deny dominance auto-rejects any tool outside the
  allowed set in a non-interactive run, killing a dimension on its first turn
  — observed for `todowrite` and for `read`. A second and third independent
  reviewer would turn "the reviewer is unavailable" into "this reviewer is
  unavailable", and would let a provider-specific defect be diagnosed by
  comparison instead of by inference.
- Comes forward when: a reviewer-side failure blocks work and no configured
  alternative can take over, or when the turn-budget item needs a provider
  that reports a comparable per-turn count. Note the asymmetry it would have
  to respect: the Claude branch deliberately ignores `defaultReviewToolCalls`
  because no confirmed flag caps its turns, and its snapshot confinement is
  tool-pattern based, which this repository already documents as not being an
  OS sandbox. A new adapter must state which of the two disciplines it can
  actually enforce rather than claiming both.
- Related: `internal/acpadapter` and `internal/agentadapter/acpx.go` already
  carry a provider-neutral contract with usage and stop-reason plumbing, so
  the seam exists; what is missing per provider is the path-confined
  permission profile and the stream scanner.

## Compare against no-mistakes on resource use and PR presentation

- Postponed by decision 2026-09-08, raised by the repository owner after a day
  in which `pr create` was OOM-killed six times: no-mistakes does not exhibit
  this memory behaviour on the same machine, and its pull-request template is
  considerably clearer than the one rendered here.
- Two separate questions, and they should not be merged into one:
  first, WHY its flow costs less — whether it audits less, audits
  sequentially, avoids materialising a validation worktree, keeps evidence out
  of tmpfs, or simply does not run a full test suite inside the publish path.
  Second, what its template does better. The template rendered here leads with
  a verdict line and a symbol matrix; a reader has to learn the legend before
  the page means anything, and the reviewer's own `⛔` was mistaken for a block
  during this very session when it means unavailable.
- Evidence available already: `internal/evidence/publish.go` bounds one
  publication at 500 files, 256 MB total and 64 MB per file, on the stated
  grounds that agent-produced evidence must fail the publish closed rather
  than push gigabytes. That is a resource ceiling of exactly the kind item 1 of
  `actionable.md` asks for, already implemented by the project being compared
  against.
- Comes forward when: item 1 is designed, since the comparison is direct input
  to it, or when the PR template is next touched. Read the source rather than
  inferring from behaviour: it is a public Go repository
  (`kunchenguid/no-mistakes`), and this session already read its custody and
  evidence packages.
- Related: the language-policy gap found in the same session. Running the
  review with `active_agent: "claude"` produced findings written in Spanish
  and persisted them that way into the ledger, against the English-artifact
  policy in `AGENTS.md`. No agent profile constrains the reviewer's output
  language, so this is not specific to that provider.
