# Decisions — closed work and its reasoning

Not a changelog: one entry per decision a future reader would otherwise
have to re-derive. Each entry states what was decided, when, why, and
where it landed. Dates are exactly as recorded in the former registers
(`follow-ups.md`, `docs/reingenieria/f0-deuda.md`, phase fichas F0-F9);
nothing is re-dated. Sources live in git history.

## Closed follow-ups (from the former `follow-ups.md`)

### Exposed-credential detection is Sentinel's own job (decided 2026-09-02, landed 2026-09-05)

A credential pasted into prose produced no signal because the class
filter correctly removes content evidence from documentation paths.
Sentinel owns this as a deterministic incident report — path plus matched
shape, non-blocking WARNING through `HallazgosDeterministas` in
`review`/`gate` with a section in `explain`, values never persisted —
independent of `security_sensitive`, which schedules reviews. The two
answer different questions and must not share a switch: FU-10 narrowed
that detector on purpose, so widening it back is explicitly forbidden.
Landed on `feat/exposed-credential-detection` (`internal/secret`
scanner); residual net-range coverage closed the same day on
`feat/credential-branch-flow` (per-commit factory in `AnalizarRama`,
populated by `pr review`/`pr create`).

### `sentinel doctor` preflights the review environment (landed 2026-09-05)

A review with missing tools died on the timeout instead of failing fast
(observed 2026-08-29: absent `ripgrep` burned two pre-push gates at ten
minutes each). The doctor resolves every configured agent plus the search
binary with `exec.LookPath` — never a shell probe, because `command -v`
can succeed through a shell function while the review subprocess fails —
and checks the six codegraph context gates, the hook target, and strict
yml loading. Advisory only, exits 0, never installs; version comparison
stays behind `--check-updates`. The per-agent probe is an unconditional
real one-word prompt: a green `--version` with a misconfigured model
proves nothing. Follow-up fixes the same day: interpreter-aware child
PATH for shebang launchers, UNKNOWN (not failed) for unprobed conditions,
effective global excludes passed explicitly so preflight predicts what
review sees.

### `model_verified` made reachable (decided and landed 2026-09-05)

The model prober was invoked and working, yet `model_verified:true` was
unreachable by construction: verification returned void and nothing
stamped the flag. `Verify` now returns a typed outcome, a match writes a
positive profile record, and the engine consults a minimal verifier
interface at stamp time; concurrent dimensions sharing one profile wait
for the single in-flight probe. `false` stays the honest default
everywhere. Landed on `feat/reachable-model-verified` (merged to main).

### P2 hygiene batch (merged as `b4405f3`)

One shared `process.ContainAfterCancellation` replaced both watchdog
copies; acpx failure routes carry a bounded stderr excerpt; the spawn
helper signature was simplified. Reviewed line-for-line for behavior
preservation.

### Daemon wire-parity tests stabilised (fixed by `f2aa7a4`)

Control-op classification no longer trusts in-memory bookkeeping over the
durable stream; the flake loop went 3/5 red to 0/5.

### codex-acp authentication (resolved, environment fix)

`-32000 Authentication required` was a stale login, refreshed via
`codex login`. No code involved.

### Ambiguous ticks, recorded honestly

Three checked items carry no landing reference, and their ticks
contradict their own text under the register's "tick when it lands"
convention — carried here as recorded positions, not as landed work:

- `runs attach` help discoverability: ticked, but no landing recorded.
- Adapter-family dispatch consolidation: ticked while its own trigger
  (a third adapter kind existing) has not fired; converges with the FU-4
  trigger in `future.md`.
- Durable raw-transcript threading: ticked while explicitly gated on
  unticketed capability-policy runtime work; the enforcement declaration
  itself is already durably recorded via admission capabilities.

### Configured-vs-serving model drift is a probe artifact (decided 2026-09-06)

(a) The configured models are valid and serve correctly. With an explicit
`--model`, both `opencode-go/glm-5.3-flash` (profile `cheap`) and
`opencode-go/muse-spark-1.3-contributor` (profiles `normal` and `deep`,
configured in `.vas_sentinel/vassentinel.yml:37-50`) answered under their
own identifier live on 2026-09-06.
(b) The mismatch records are false positives caused by the probe path.
The Verify probe reaches the agent through `CLIAdapter.EjecutarPrompt`
(`internal/agentadapter/cli.go:63`) → `ejecutarComando`
(`internal/agentadapter/cli.go:118`) → `ejecutarComandoConTimeout`
(`internal/agentadapter/cli.go:284`) → `comandoPrompt`
(`internal/agentadapter/cli.go:578`), which emits bare `opencode run`
with no `--model` flag. The probe
(`internal/modelprobe/verificador.go:115-152`) therefore always measures
the OpenCode default (`openai/gpt-5.6-sol`) instead of the configured
profile model and records `model_mismatch` against it: the store records
`.git/vas-sentinel/profiles/cheap.json`, `normal.json` and `deep.json`
all show `model_mismatch` of the configured models versus
`openai/gpt-5.6-sol` (recorded 2026-09-05). Semantic review serving is
unaffected: `reviewCommand` passes `--model` when a model is configured
(`internal/agentadapter/cli.go:400-401`), so audits run on the
configured models.
(c) Corrected configuration: none. The current `vassentinel.yml` values
stand; no configuration edit was made and none is needed.
(d) The correction owed is code, not configuration: the probe must
request the configured model (pass `--model` on the probe invocation).
Filed as a follow-up for a code unit and explicitly not done in this
docs-only decision.

## Closed FUs (from the former `f0-deuda.md`)

### FU-5: widened review context provider (resolved 2026-09-06)

The reviewer burned its budget searching for call sites the provider
could answer: `graph.ProveedorCodeGraph` returned only `affectedTests`.
It now also answers `caller`/`callee`/`impact` relations
(`review.RelationCaller/RelationCallee/RelationImpact`), with symbols
derived from the audited diff (added top-level Go declarations, sorted,
capped at 8) and every path behind `rutasSeguras` plus symlink and
containment validation. Landed on `feat/fu5-widen-context-provider`
(`91a3c3d`, `fe97ca1`, `a6f8c9d`; final gate PASS).

- Additive relations stack on top of the `affectedTests` budget instead
  of sharing it: 32 + 3x8 = 56 references. Conscious deviation from the
  item's original per-relation-share text: the first implementation cut
  affected-only recall from 32 to 8 and Sentinel blocked it as a
  regression.
- Bounded cost: up to 8 symbols x 3 relations, each subprocess on its
  own 3s budget.
- Both non-negotiable constraints intact: the `HEAD == sha` gate (the
  fix for the CRITICAL that blocked `627430d`) and the 3s
  per-subprocess timeout (explicit user decision in `5187fb0`).
- Degradation: every additive failure contributes zero references and
  never regresses `affectedTests`.
- Recorded evidence gap: three runs admitted by the `91a3c3d` review
  were pruned by retention before the settlement sweep; their verdicts
  stand in the ledger. Pruning, not corruption.

### FU-6: evidence-bound human dispositions (resolved 2026-09-04, merged 2026-09-05 as `811b9f1`)

Three finding statuses had no production writer and only refutation
cleared a block. `sentinel refute` (evidence-bound, clears its block),
`sentinel accept` (documents judgement, never clears) and `sentinel
reopen` (re-blocks) now exist; `fixed` deliberately gains no
per-fingerprint writer. Seventeen commits, full suite green.

### FU-7: dispositions reach the aggregates (landed 2026-09-01 as `c1655a2`)

Confirmed/refuted lived only in raw dimension findings while aggregates
carried empty status, so metrics read zero at zero coverage. A read-side
observation-only projection carries dispositions into the aggregates;
confirmation coverage went 0/837 to 78/837. No third finding shape.

### FU-8: double-counted terminal failures (closed 2026-09-03, completed 2026-09-05)

Outcomes were counted both from the stream and the snapshot. Counting now
happens once, and each aggregate failure carries its source
(outcome/semantic) plus coverage. The split-populations shape was
rejected: it would have broken the T9.5 exclusive-one-entry retention
assertion.

### FU-9: flattened identity stays unread (settled 2026-09-05)

426 of 850 event streams carry a flattened model string the producer
cannot read; the settlement keeps it that way — the current zero is
correct — with the reason stated at the read site. Reading flattened
values would attribute evidence to runs that never produced it. The
prober-reachability strand landed separately (see `model_verified`
above).

### FU-10: one evidence constructor for planner and `explain` (resolved 2026-09-02)

The planner classified from symbols and paths while `explain` saw full
evidence, so review was systematically weaker than the explanation of the
same commit. One shared constructor feeds both; two detectors were
narrowed with measurement and the prose guard verified to zero
dimensions. Evidence pin kept in git history (`docs/reingenieria/evidence/fu10-divergence.md` before this consolidation).

### FU-11: credential scanner (decided 2026-09-02, landed 2026-09-05)

See the credential decision above; commits `15bfb6e`, `a2d4824`,
`bb7db5d`.

### FU-12: ledger anchored on the common directory (closed 2026-09-02)

Reviews from linked worktrees filed records where main-checkout readers
never looked. Writes now go through `sharedReviewLedger`
(`cmd/sentinel/shared_ledger.go`); the destructive reader was fixed in
`b59778b`, `190cf55`, `29367b2`; the purge walks every ledger, and a
question the purge cannot answer never authorises a deletion. Deliberate
cost, not debt: 65 old worktree fichas stay where they are, still
enumerated by prune paths but no longer read by review/status/pr.

Migration half resolved 2026-09-06: `MigrarDesdeV1` had no production caller (tests only) and its output type `CompatV1` / `IndiceCommit.V1` had no reader, so the whole family was removed (`85ef280`: `internal/store/migracion.go` and its test, the `V1` field on `IndiceCommit`, and the one production comment naming it). Anchoring closed the write-loss half; deletion closed the dead-code half. The 65 old fichas stay put under the deliberate cost above.

### FU-13: net planning classifies from the complete path list (resolved 2026-09-02)

Sanitised paths dropped legal filenames and with them route-based
evidence. The complete list feeds planning; the sanitised list stays for
interpolation surfaces only.

### FU-14: detectors honour repository attributes (resolved 2026-09-02)

Half the detectors ignored `.gitattributes`, so generated trees fired
behavioral classification. Nature-of-code detectors moved to the
attribute-honouring classifier; the which-surface check stays on the raw
path check for a stated reason. Pinned by tests that resolving must
update, not delete.

### FU-16: unreadable ledger fails closed (resolved 2026-09-02)

Directory enumeration swallowed I/O errors, so an unreadable ledger read
as empty — destructive for prune provenance. Enumeration now propagates
errors; a missing directory stays a real, distinct answer.

### FU-17: corrections require ancestry (resolved 2026-09-02)

A fix on one branch could clear a block recorded on another. The audited
commit must now be an ancestor of the fix commit; no failure reads as
"no". Residual, recorded not repaired: ancestry plus file overlap cannot
tell topicality, so `fixed_in` means a later commit touched these files
and passed review — not that the premise was re-tested.

### FU-19: tolerant finding parsing (landed 2026-09-05)

One unparseable field rejected a whole CRITICAL finding and reported
`unavailable` instead of `block`. Parseable fields now survive with the
raw value preserved verbatim; other schema rejections still reject, and
the raw value never enters the fingerprint.

## Phase decisions (F0-F9 reengineering, all phases closed)

F1 closed `ac455be`, F2 `a1cd03b`, F3 retroactively on commits
`66c680a..3a9632a`, F4 `fc9cb82`, F5 `91dc65f`, F6 `a883cac`, F7
`802e7e1`, F8 `a244833`, F9 `65c9edd` (2026-09-04), phase 2 `9fb3925`.
Per-task acceptance narratives are gone with the fichas; what survives
here is what a reader would otherwise re-derive.

### Architecture and dependency direction

- Business logic lives in `internal/`, `cmd/` only parses flags
  (`internal/gate/gate.go`); the first step of extracting `package main`.
- `internal/store` imports `internal/review`, never the reverse; the
  cycle breaks through the structural `StoreBlobs` interface defined in
  `internal/review`. Easy to re-violate.
- `ContextProvider` lives in `internal/review` to fix the inverted
  dependency; CodeGraph is context-only and unreachable from validation
  scoping by type, not by discipline.
- Snapshots, store, graph cache and blob store always resolve the Git
  common directory, never the per-checkout git dir
  (`git.ObtenerGitCommonDir`).

### Gate and validation

- Deterministic validation blocks; semantic review advises.
- Exit codes 0/1/2/4, with 1 deliberately shared by validation and
  review failures.
- Incomplete graph means full validation: completeness is the authority,
  partial scope without a complete graph is rejected by construction, and
  `go.mod`/build/CI changes always force full validation. Time
  optimisation never degrades gate confidence.
- Unknown config keys are explicit errors with line numbers under strict
  loading everywhere: for a gate, a silent failure is the worst failure
  mode.
- Validation runs against the frozen tree snapshot; inplace mode exists
  because a clean checkout has no unversioned files.

### Change and risk

- Risk is a maximum over rules, never a weighted sum, and every level
  names the rule that produced it. Size raises depth and triggers split
  suggestions, never risk; the 400-line reviewability threshold is a
  different magnitude and the risk model must not touch it.
- Deterministic kind precedence: generated, dependency, infra, CI/CD,
  configuration, documentation, test-only, refactor, bugfix, feature.
- Absent evidence renders as unknown, never zero; zero is an explicit
  measured value. Unreadable evidence is an error, never an empty store.
- No characteristic without a detector; honest heuristics ship flagged.
- Split is suggested, never applied: rewriting history without a human is
  unacceptable, and A/R/E/C approval stays the sole guardian unlock.

### Findings and store

- Finding v2 contract: stable fingerprints over dimension, symbol-or-path,
  normalised evidence and rule; the evidence filter normalises
  whitespace so re-indentation never fails it, and discarding a finding
  never invalidates the run.
- Migration never fabricates evidence and never deletes: v1 entries keep
  content with empty fingerprints; migration is idempotent and a corrupt
  file is an explicit error.
- Blob reuse after rebase requires every file of the commit to match one
  single prior SHA; mixed provenance (squash) means re-audit — losing
  reuse beats losing real findings.

### Review and aggregation

- The engine stamps the effective producer from verified evidence; the
  model-reported identity is replaced, never trusted. Corroboration
  combines with noisy-or so independent agreement raises confidence.
- Supersede requires the same dimension plus overlapping location —
  learned from a blocked review where a whole-file gofmt would have
  erased unrelated security findings. The automatic gate stays unwired;
  supersede runs only in `pr create --force`.
- Retry only on transport error or timeout, never on invalid output;
  unlaunched priority agents are declared, never silently skipped.
- Context builds in ordered layers, trimmed from the top, never the
  bottom two; it reads from the frozen object store, never the worktree.
- The refuter gets one cheap call per blocking finding to try to prove it
  false; a refuted finding means user review, never silent discard.
- Stacked PRs never default to `main`: guessing the base wrong reports
  someone else's changes as your own, the worst review error. Parent inference is
  explicit flag, then `gh`, then tracking upstream, then strict-ancestor
  local inference, then explicit failure. Inherited findings are
  structural (a separate non-blocking slice), and net review archives
  findings for code the net diff no longer contains.

### Remediation

- No shell by type construction: the editor interface exposes only read
  and edit, so "make the tests pass" can never arrive as an instruction.
  Paths are validated before any I/O; the guard discards the whole fix
  before the editor is touched; one round only, because fix-review-fix
  loops are where cost explodes and reproducibility dies.
- `force_bypass` registers at bypass time even if publishing later fails;
  answer identity is (blob, question) with the accepted limitation that a
  new question reusing an ID inherits the old answer.

### Observability and retention

- Durable Runs is the sole execution authority; no parallel ledger.
  Snapshots are immutable, write-once, and retained outside the prunable
  execution directory; deletion is the idempotence guarantee.
- Class A (no producer — more observation cannot fix it) versus class B
  (producer exists, evidence incomplete): recording A as insufficient
  sample would imply observation fixes it, and it does not.
- The retention publication boundary is the remote base-branch ancestor
  (both alternatives measured and rejected); agreement, not presence,
  authorises collection; fichas and events survive pruning.
- `review.timeout` 900s is the only data-backed default change (325
  runs, 12.3% to 0% exceedance), with the censoring caveat recorded, not
  hidden. Series before and after the 2026-09-04 first collecting pass
  are not comparable.
- Per-model attribution covers ACP-served runs only; a plain CLI adapter
  contributes no observed identity because configured model and effort
  are declarations, not evidence.

### Process and governance

- Stable guardian versus dev binary with explicit promotion only;
  subagents never commit because committing approves a fragmentation plan
  and that approval is the sole human control over the guardian unlock;
  no `--yes` auto-approval ever, in any binary.
- Evidence rule: a verdict carries the test covering it, the execution
  proving it, or the exact code path — otherwise it is doubt, never
  verified. A test never seen failing proves nothing.
- Recompile and reinstall the guardian before slicing when slicing logic
  changed, or old-criterion commits get created.
- No AI attribution in commits or PRs, ever.
- Recorded honesty norms: F6 and F9 both refused to manufacture missing
  gate evidence, and the T9.3a executor deviation was recorded rather
  than presented as compliance.
