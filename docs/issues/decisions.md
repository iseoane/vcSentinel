# Decisions — closed work and its reasoning

Not a changelog: one entry per decision a future reader would otherwise
have to re-derive. Each entry states what was decided, when, why, and
where it landed. Dates are exactly as recorded in the former registers
(`follow-ups.md`, `docs/reingenieria/f0-deuda.md`, phase fichas F0-F9);
nothing is re-dated. Sources live in git history.

### The PR surfaces say what they ran, and only that (item 2, closed 2026-09-15)

Two different defects produced the same untrue sentence, and separating them
is the part worth keeping.

`verifyInternal` built its command list from `lint_commands`, `test_commands`
and `build_commands` only. This repository declares verification under
`validation.capabilities`, and `translateLegacyCommandsToCapabilities` converts
the legacy keys INTO capabilities and returns early when capabilities already
exist, so nothing ever filled those lists back in. The list came out empty and
`pr create`'s notice announced that no verification was configured. It now
resolves the declared validation profile when the legacy lists leave nothing to
run; those lists still win when present, so an older configuration keeps its
exact commands and ordering, and a profile that is absent or whose capabilities
carry no command leaves verification unconfigured rather than inventing
commands nobody declared.

The second defect is not about reading configuration at all. `pr review` runs
neither validation nor verification — that is its contract — but it passed an
unmarked empty verification, which renders identically to a run that looked and
found nothing. Its body stated "No validation commands configured" and its
attestation recorded lint, test and build as `not_configured`, minutes after
the gate had run four of those commands. Both read as facts about the
repository. `TemplateVerification.NotAttempted` now distinguishes "never
looked" from "looked and found nothing", and the attestation records
`not_attempted`.

That status change nearly broke publication, which is the lesson: `pr create`
validates what `pr review` persisted through `validStepStatus`, so emitting a
status that validator did not accept would have rejected every entry `pr
review` writes. Every package test still passed, because `pr review` authors
the attestation and `pr create` validates it with nothing exercising the round
trip. That test now exists and fails with `pr create rejects what pr review
authored`; it covers the attestation half only, and says so rather than
implying more.

The design doc for piece 4 documented the old statuses as correct, table and
worked example both, with a stated invariant that the two agree. Updating one
without the other would have left it contradicting itself while claiming to be
fixed; the review caught exactly that.

Landed in `52b4588`..`2bb410c`. Still open: the status vocabulary is declared
independently in `validStepStatus` and in `pipelineSteps`, which is the
coupling that nearly broke publication here.

### Orphaned runs are surfaced, not settled automatically (2026-09-15)

Item 1 asks that "a kill should retire its own runs instead of leaving them
for an operator". That was attempted and abandoned, and the reasoning is worth
keeping because the attempt looked reasonable.

The settlement already existed: `OrphanActiveRuns` handles cross-process
residue with its honesty rules intact, and only a graceful daemon shutdown
invoked it. What was missing was the ability to decide that an owner is gone,
so the attempt recorded `OwnerPID` in `RunPolicy` and claimed it atomically.
Review returned ten CRITICAL findings, and four were the same design fault
seen from different sides: policy.json is the IMMUTABLE admission record, so
putting ownership there broke `Start`'s idempotency with
`ErrImmutableConflict`; `UpdateRunCommit` and `UpdateRunWorktree` rewrite that
file without the execution lock, so a stale write could restore a dead PID
over a live owner and let reconciliation cancel active work; ownership was
never released at terminal state, so a `Retry` from another live process was
rejected; and a PID alone cannot survive reuse between the liveness check and
the append.

Doing it properly means a separate lease record outside the immutable
comparison, holding a process GENERATION (PID plus start time) rather than a
PID, written under the execution lock with compare-and-swap, released at
settlement, and fenced at every append. Estimated at five slices and roughly
650-700 lines in `store` and `controller`, both contract-bearing.

Rejected in favour of the cheaper answer, ~120 lines and no new races: `runs
status` now runs the same read-only classifier `runs recover` renders and says
how many runs were left unsettled, naming the command that inspects them. The
human still decides.

The deciding argument: `runs abort --orphaned` deliberately refuses to settle
without an operator's stated reason because the CLI cannot prove a process is
dead. The automatic design is an attempt to prove exactly that, and the ten
findings are what proving it costs. The pain item 1 actually records is that
the operator never learned the residue existed — three accumulated unnoticed
in one session — and that is what was fixed.

Landed in `e09a8bc`..`07167af`. Revisit only if runs ever execute unattended,
with no operator to notify; then the lease design above is the right one and
should be built completely rather than patched onto the policy record.

### A killed process no longer keeps its snapshot for a day (2026-09-15)

`internal/validation` defers `PurgeSnapshots`, so a process killed
mid-validation never runs it, and the next purge skipped the leftover because
`info.ModTime().After(cutoff)` was true — the orphan was too RECENT for the
24-hour retention. It survived at least a day, registered in `git worktree
list`; three accumulated in one session. The flock was never the obstacle: the
OS releases a dead process's lock.

Reclaiming every unlocked snapshot was rejected: snapshots are a deliberate
per-tree reuse cache, purge runs after every `CreateSnapshot`, and an unlocked
snapshot is indistinguishable from a warm one. What distinguishes them is now
recorded rather than guessed — `AcquireSnapshot` writes its PID to a holders
file beside the lock and the release removes it, so a holder that is no longer
alive proves abandonment and the entry is reclaimed regardless of age, while
an entry with no holder keeps the ordinary age rule and the cache survives.
PID reuse can only produce a false ALIVE, which defers the orphan to the age
ceiling; it can never produce a false DEAD, because dead requires that no
process holds that PID. That asymmetry is why no process start time is
compared.

Three defects found in review and fixed before merge, each worth keeping:
unlinking the primary lock file after releasing it let one process hold an
unlinked inode while another locked a fresh one at the same path, so the
primary lock is now never removed; a live PID originally skipped the age
cutoff entirely, so a reused PID could protect an orphan forever — the age is
now the ceiling in every case, which is what the code's own comment already
claimed; and a process killed between the publication rename and `git worktree
repair` left a checkout whose metadata pointed at the temporary path, which
neither removal attempt could reclaim, so purge now repairs and retries before
falling back to filesystem removal and a prune.

Landed in `f35f294`..`495892f`. Remaining accepted cost: primary `.lock` files
are never removed, so one empty file accumulates per distinct tree ever purged.
A parent-lock scheme to collect them was judged more complexity than an empty
file per tree is worth.

### A finding fixed inside a branch no longer blocks its PR (2026-09-15)

`pr review` reported a four-commit branch as blocked by five findings, every
one of them already fixed by a later commit of that same branch; the net audit
of the delivered diff contributed none of them. The PR was judged on how the
branch reached its state rather than on what it delivers. Rebasing did not
clear it — verified twice — because blob indexes preserve coverage by content.

Item 14 had made this deliberate, and for a real reason: a `FixedIn` credit
once hid four later CRITICAL findings on `41c644d`. That protection is kept.
What item 14 could not distinguish is a partial fix from a complete one that
landed later in the same branch; what it actually measured was whether the old
SHA's findings had been recomputed, which for an immutable commit never
happens, so the documented escape hatch (re-audit the blocked SHA) is
structurally unreachable for the commit-plus-correction-rounds shape.

It is now measured: a finding contributes to the branch projection only while
its recorded evidence still exists at the head. A partial fix leaves that
evidence and keeps blocking by construction; a complete one removes it and
stops. The check is the one carried dispositions already used, split so a
completed negative is distinguishable from an unverifiable one — branch
blockers fail closed and keep blocking whenever the evidence cannot be read.
`FixedIn` semantics are untouched and still claim no more than provenance:
`RenderSummary` renders a fix credit only with a non-empty `FixedIn`, so a
record retired by evidence alone claims none.

Absence is established over the paths that changed between the finding's
commit and the head, not over its original file alone: a later commit that
MOVES the offending code elsewhere must land it in one of those paths. Two
CRITICAL findings arguing this was still insufficient were refuted by the
repository owner with evidence (`a36097e`, lines 314-333): a file unchanged
across that range already held the fragment when the finding was recorded, so
it is pre-existing rather than candidate-caused, and this project's contract
makes base-only findings follow-ups rather than blockers.

Landed in `3fbdf4a`..`668d0cd`. The branch projection reads `HEAD` rather than
an explicit head parameter; threading one would have changed exported
signatures, so the determination is recorded at the seam naming every call
site and the symptom if it stops holding: a report retiring or retaining
findings against an unrelated checkout's tree.

## Closed follow-ups (from the former `follow-ups.md`)

### A retired block no longer hides a later re-audit (item 14, closed 2026-09-10)

`isPending(record)` was `record.FixedIn == ""`, and `MarkFixed` keeps the
first fix, so a record credited with a fix was skipped by
`effectiveBranchFindings` forever — even when a later revision recorded
genuine CRITICAL findings. Observed live: `41c644d` carried `fixed_in
ec9be13` from its first revision and four CRITICAL findings from its
second, and the branch projection treated it as retired.
`RecordHasActiveBlock` already read the current findings correctly, so the
two predicates disagreed and only the branch-level one was wrong.

Resolved by demoting `FixedIn` to provenance rather than by recording the
moment of the fix: the `FixedAt` field the item proposed was written and
then deliberately dropped (`8be639a`). `isPending` became the exported
`RecordPending(record, dispositions)`, which answers over the record's
CURRENT findings (RULE 2) overlaid with the standing human answers, so a
partial fix cannot retire a record that still carries live CRITICAL
findings and a re-retirement keeps the first fix's provenance. The branch
blockers, the pending risks, the summary table, the inherited section of a
stacked PR and the `sentinel status` listing all decide with it, and the
human listing renders `fix credited to <sha>` rather than `fixed in <sha>`
because the credit is no longer a claim that the block is gone.

Two surfaces deliberately do not follow it, recorded so nobody reads the
rule as universal: `sentinel status --json` still emits `fixedIn` raw,
which is consistent with the field being provenance and had no reader in
this repository; and `VerdictDeBranch` still contributes the raw `Result`
of a record that counts (RULE 1, unchanged), so a refuted finding can leave
the branch verdict at `block` while the risks and blockers of the same
report are empty.

Consequence for daily use: crediting a `fix(` commit retires nothing by
itself. A block clears only by re-auditing the blocked SHA
(`sentinel review <sha>`) or by a human `refute`. `recordFixes` and
`MarkFixed` are untouched and still record the link for metrics.

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
(`internal/modelprobe/verifier.go:115-152`) therefore always measures
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

### Probe requests the configured model (decided and landed 2026-09-06)

Follow-on to the probe-artifact decision above: the owed code correction
landed. `comandoPrompt` appends `--model <configured>` when `Config.Model`
is set, mirroring `reviewCommand`, so the Verify probe measures the
configured profile model instead of the provider default
(`internal/agentadapter/cli.go`). No flag is added when no model is
configured; the prompt still travels via stdin. Pinned by
`TestPromptProbeRequestsConfiguredModelOpenCode`,
`TestPromptProbeRequestsConfiguredModelClaude` and
`TestPromptProbeWithoutModelAddsNoFlag`
(`internal/agentadapter/cli_probe_model_test.go`). Landed as `1504cb8`
(plus `79b776e` for English artifacts and precise parity wording), merged
to main in `0878378`. Existing `model_mismatch` profile records stay as
honest history.

### Gate collects first-parent diff for merge commits (decided and landed 2026-09-06)

`gate --stage pre-push` on merge `ae804a5` collected an empty diff and
landed `NEEDS_USER_REVIEW` for no content reason: `git show` emits an
empty combined diff on clean merges. `git.DiffCommit` and
`git.ArchivosDeCommit` now diff against the first parent when `rev-list
--parents` reports more than one parent (`internal/git/commit.go`,
new private `isMergeCommit`); non-merge paths are byte-identical, and all
existing callers (`comandos_gate.go`, `comandos_review.go`,
`review/rama.go`) are covered without changes. Pinned by
`TestDiffCommitOnMergeReturnsFirstParentDiff` and
`TestCommitFilesOnMergeListsBranchFiles`
(`internal/git/commit_test.go`), including negative first-parent
assertions and the two-parent guard owned by the shared helper. Landed as
`30fe12a` (fix), `058bc94` plus `660fc91` (review-driven test
hardening), fast-forwarded to main. Review verdicts: `warn` on `30fe12a`
(four non-blocking findings, all addressed) and `warn` on `058bc94`
(one ADVISORY, addressed by `660fc91`). Deliberate non-fixes: one extra
local `rev-list` subprocess per audited commit (negligible; merging
detection and diff into one call would add shallower indirection), and
new English comments beside Spanish legacy blocks (the standing English
rule outweighs matching the file).

### Direct adapters report token usage; cache design now measurable (decided and landed 2026-09-06)

Actionable item 1 ("Cache shared audit evidence") was blocked on the
shared token-observability note: no adapter reported usage, so cache value
could not be measured. That prerequisite has landed on
`feat/medicion-adaptadores-directos`: direct OpenCode reviews parse
`--format json` step_finish tokens
(`internal/agentadapter/opencode_review.go`), direct Claude reviews parse
`--output-format json` result usage (`internal/agentadapter/claude_review.go`),
both feeding the existing `reviewexec` to metrics plumbing with absent
fields as nil, raw usage retained, review text byte-identical, and
requested/observed identity kept separate. ACP/acpx already reported
terminal usage. Live proof: one cheap logic review recorded
input/output/cached observations with a valid `runs verify`; final gate
PASS.

Deliberately not done: the cache design itself — shared snapshot, stable
evidence envelope, prompt reorder, cache key. It stays in `actionable.md`
as unblocked work, to be selected with measured input-token, cached-read,
latency and review-equivalence numbers. Cost stays unmapped on purpose
(F9 precedent); FU-3 narrows to price, scope and reuse.


### A pull request reports its unaudited commits instead of auditing them (decided 2026-09-08, landed 2026-09-08)

`pr review` and `pr create` audited every branch commit that carried no
review record, and then ran the net audit as well, so the same code
reached the reviewer twice. That is not the redundancy it looks like —
`runNetReview` plans from the NET diff's own aggregate risk through
`PlanForProfile`, so the two ask different questions — but the PR verdict
never depended on the per-commit half, and paying for both fell due at
the worst moment: a branch whose commits were never reviewed as they went
made the pull request settle the whole accumulated bill at once. One
measured run audited five commits plus the net diff, ran about an hour,
and ended by exhausting the machine's memory.

Decided: a pull request names the commits with no record and points at
`sentinel review <sha>`; it never audits them on the operator's behalf,
and it never blocks on their absence. The net verdict stays the only
gate. `sentinel pr create --audit-pending` restores the old behavior for
callers that want it in one pass. `sentinel pr review` rejects both
`--audit-pending` and `--only-unaudited`; the latter was retired rather than
kept as a silent no-op because it had come to describe the default, and its
help text ("restrict the analysis to commits without a review record") never
matched what it did (skip auditing entirely).

Two consequences were handled with it. `pr create` refused to publish on
an empty per-commit history, which is now a legitimate state, so the
refusal requires a missing net verdict too. The `--json` report and both
event details now carry the unaudited commits, because an unaudited commit
and an audited one with no findings were indistinguishable to a machine
consumer.

Kept deliberately: per-commit records still attribute a finding to one
commit, are what `refute` and `accept` operate on, and survive rebases
through the blob index. Nothing about them changed except who pays for
creating them.

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

### Provider state lives outside the evidence snapshot (landed 2026-09-08)

`reviewEnvironment` used to set `isolationRoot := snapshot`, so `HOME`,
`USERPROFILE`, `OPENCODE_TEST_HOME` and every `XDG_*` path pointed at the
published review snapshot: the audited evidence tree and the provider's
writable home were one directory. That is why its directories were 0700, why
sealing them read-only broke every review with `EACCES` on
`mkdir '<snapshot>/.local'`, why concurrent dimensions auditing one SHA raced
each other's SQLite state with `CREATE TABLE workspace`, and why a contaminated
tree was rejected by `publishedSnapshotUsable` and rematerialized instead of
reused — so the per-SHA dedup was real on disk while earning nothing.

Each OpenCode invocation now gets its own writable isolation root, created as a
sibling of the resolved snapshot and removed by whoever created it. Claude and
generic providers keep their own environment; Claude's is additionally stripped
of any inherited `OPENCODE_AUTH_CONTENT`, which closes a cross-provider
credential path that predated this work. The root is reaped by its own prefix
case, and its containment is proven rather than assumed: the snapshot is
resolved with `EvalSymlinks` and a root that would land inside it is removed
with the call failing closed.

Verified on a real review rather than by tests alone: one published tree for
the audited commit instead of the twelve a contaminated tree forces, zero
provider directories inside it, one live isolation root per concurrent
invocation, all cleaned up afterwards, and every dimension completing with no
`CREATE TABLE workspace` error.

Three CRITICALs were found and fixed on the way, all by review and none
catchable by the suite: the isolation was first applied unconditionally and so
sent OpenCode's environment and credentials into the Claude process while
hiding Claude's own HOME; the isolation root then sat where no reaper could
reach it; and `filepath.Abs` left the containment guarantee holding only for
paths with no symlinked component.

Merged as 3c2dc96.

### Published snapshot removal restores owner write access (landed 2026-09-08)

Published regular files are deliberately read-only (0400, or 0500 for a
committed executable), so every store cleanup path routes through
`removeReadOnlyStoreEntry`, which restores owner write access before deleting:
the stale reaper, the failed-publication cleanup and every staging path. Linux
unlinks a read-only file inside a writable directory, which is why the whole
suite passed while the defect existed; Windows refuses, so there the reaper
could remove nothing and residue grew without bound.

The expectation is pinned per platform the way `publishedPermMatches` pins
validation: Linux adds the owner write bit while preserving the rest, Windows
forces 0600 to clear the read-only attribute. Only the Linux branch is
executed on this machine; the Windows branch is compile-verified.

The helper also tolerates an already-removed entry, because it stands in for
`os.RemoveAll`, which succeeds on a missing path, while `filepath.WalkDir`
reports the lstat failure. Two callers count a nil result as one reaped entry,
so an entry lost to a concurrent race had stopped being counted.

Two Sentinel WARNINGs were accepted with reason rather than fixed: `removalMode`
exists only for linux and windows, which is pre-existing — darwin already fails
to build through `internal/process` `newPlatformOwner` and `publishedPerm`, and
the documented platforms are Windows and Debian; and the chmod walk is
path-based, so a same-user symlink swap inside the 0700 per-uid store root
could redirect it.

Merged as 4010883.

### Shared review snapshots are SHA-keyed and retained (decided and landed 2026-09-08)

`reviewsnapshot.Create` now publishes one retained snapshot for each audited
commit SHA. Concurrent reviewers lease the same complete tree through
per-SHA locks; the reaper only removes entries that remain stale after it
holds the exclusive lock. Published files preserve committed executable mode
and are owner-read-only, while the snapshot directory remains a usable
reviewer working directory.

The store is deliberately scoped to Debian/Linux and Windows, matching the
repository support policy. Its package-local nonblocking locks are not
extracted into `internal/git`: their lease and reaper semantics differ from
the existing Git snapshot locks. Metadata validation detects accidental
corruption and incomplete publication; it is not a same-UID security
boundary, so per-lease content hashing was not added.

**Amended: the store is now bounded on both axes (item 3, landed as `6b781d5`,
PR 1).** Retention alone was not a ceiling. `sentinel review HEAD --all` on this
repository audits 871 commits, needs about 5.0 GB against a 3.8 GB tmpfs, and
filled `/tmp` to 100% on 2026-09-08; the remaining dimensions failed with
`no space left on device`. The wrong flag was the immediate cause, but a store
with no ceiling on a tmpfs is one bad invocation away from filling the disk.

Both halves landed, because either alone leaves the other axis unbounded:

- A total-size ceiling. `storeCapacityLimit` derives the bound from the
  filesystem through `statfs` rather than from a magic number, at one tenth of
  capacity, on the stated grounds that free space includes unrelated data and
  evicting this store cannot repair another's overrun.
  `reapSharedStoreCapacity` evicts the least recently leased unleased published
  tree first, each under its own lock, so a candidate that cannot be taken never
  blocks the others.
- `staleSnapshotAge` lowered from 24 hours to one. The window in which retention
  buys anything is minutes — one review's five dimensions — and at most an hour
  for format retries and chained reviews of the same commit. Twenty-four hours
  bought almost no extra reuse while multiplying the worst-case residue by every
  commit touched in a day. Measured that afternoon: 50 published trees and
  446 MB, none older than an hour, so the reaper correctly refused to remove any
  of it while /tmp sat at 94%. The value is pinned by an exact-equality test
  (`snapshot_test.go`), so lowering it again is a decision, not a drift.

What the item predicted and got wrong, recorded so the reasoning is not
reused: it expected retention to be worthless until the PR verification notice
(item 2) landed. That was a misattribution. What made retention earn anything
was moving provider state out of the evidence snapshot (see that entry above),
after which the tree count falls to one per audited commit. Item 2 is
unrelated and still open.

The provider's own residue beside this store — roughly 14 MB per reviewer
invocation from the Bun runtime OpenCode ships — is NOT bounded by this
ceiling and remains item 5 in `actionable.md`.


## Closed actionable items (from `actionable.md`)

### A rebase that changed no content no longer pays for a re-audit (item 13, landed as `61bb46a`)

Opened 2026-09-10 and measured on this repository the same night: renaming one
commit message rewrote the SHAs of the six commits above it, every review record
was orphaned, and `sentinel review` re-audited all six — about **thirty model
calls**. Five of the six had byte-identical content to the version already
reviewed. Nothing about the code had changed. The cause was that the ledger is
keyed by commit SHA while the coverage it records is a property of the CONTENT,
so a rebase, an amend or a cherry-pick throws away every semantic verdict.

The machinery already existed and was simply not wired to this path:
`store.AlreadyReviewed` recognised a blob reviewed under a different SHA, and its
only consumer was `internal/review/branch.go`, the `pr review` path.
`sentinel review <sha>` never asked.

Decided and landed in two tiers, because the commit message is real input to the
audit: identical blobs with an identical message reuse every dimension; identical
blobs with a different message reuse everything except `spec`, which is
re-audited because it compares what was promised against what was delivered. An
adopted record carries the destination commit message and records its origin SHA
as provenance, so a reused verdict is never indistinguishable from a fresh audit.

The reuse predicate carries the preconditions each review round exposed, and they
are the part a future reader would otherwise re-derive: the file-to-blob mapping
must match exactly rather than the set of hashes, the origin must be a single
deterministically selected commit, its record must exist and be authoritative
rather than supplementary, and the destination must not be among the candidates.
Adoption merges both revision histories instead of replacing either, keeping the
append-only contract. Blob coverage that outlives a pruned ledger record is
discarded rather than treated as fatal: losing the reuse beats being unable to
audit.

Two things this deliberately does not fix. A record carried forward is still a
record about content, not about the branch, so rebasing without running `review`
again leaves the ledger with holes exactly as before. And the per-dimension
version — re-audit only the dimensions that read a changed file — was judged
finer and rarer and was not built.

Consequence for item 0, which stays open: this removed one of the three causes
that made "unlinked fix" look like a `fix(` prefix problem. The three cases seen
on 2026-09-09 had three different causes — a review that came out `unavailable`
(which correctly retires nothing), a fix commit with no record at all, and a
rebase that orphaned the link. Only the first is item 0's subject.

### The review flow's five pieces, and where each question ended up (items 12, 4, 10, 11 — closed 2026-09-15)

[`docs/design/review-flow-ownership.md`](../design/review-flow-ownership.md)
allocated one question to each command and ordered the work as five pieces. All
five are done, so that design is now history rather than a work order. What
survives is the allocation itself, stated by the repository owner on 2026-09-09,
because every item was drifting away from it independently:

- `review` — "is this piece well made". It looks at one commit, and it is the
  only writer of per-commit verdicts.
- `gate` — "does this work right now". It audits nothing.
- `pr review` — "the pieces together tell a coherent and complete story". It does
  NOT look at each piece again; it looks at what is only visible with all of them
  together.
- `pr create` — reviews NOTHING. It takes `pr review`'s report and publishes it.
  If no report exists, it refuses.

The one correction made along the way, and the reason two texts in the register
disagree: the contract first gave `gate` the per-commit audit. Working the flow
through end to end showed why that is wrong — "compiles and passes its checks" is
a property of the tree at one moment, not of a commit in isolation; split one
piece of work across seven commits and the third usually does not build alone. So
the two questions cannot share an owner.

**Item 12 (piece 1): the intent is captured when `slice` commits the work.** The
problem it replaced was that item 10 needed a statement of what a change was
supposed to do, and the obvious source — mining the conversations that produced
the commits — means reconstructing after the fact across several days, more than
one agent, and commits that may have no conversation at all. `slice` is the right
moment because the intent is present rather than reconstructed: the work just
finished, the conversation is current, the commit is small and concrete, and
`slice` already invokes an agent right there to write the commit message.
`slice plan --intent "<text>"` records it with `declared` provenance. Provenance
is non-negotiable and travels with the intent: "derived from the working
conversation" and "declared by the human" are not worth the same, and a reviewer
that cannot say which it got asserts more than it can support. The transcript
half was withdrawn and is [item 15](actionable.md) in `actionable.md`.

**Item 4 (closed by dissolution, 2026-09-09): there is no discarded verdict to
persist.** The item existed because `gate` audited `HEAD` and threw the verdict
away, so the fix looked like "make `gate` write a ficha". The premise was the
mistake — see the correction above — and `gate` stopped auditing (piece 3,
`144c9c8`) while `sentinel review` became the only writer of per-commit verdicts
(piece 2, `89cab36`).

Two constraints from that item are live and outlived it, and they are why the
entry is here rather than deleted:

- **A range record cannot inherit a per-commit record's rebase survival.** A
  per-commit ficha survives a rebase through its content blobs; a record keyed by
  `base..head` or by a hash of the net diff goes stale the moment the branch
  moves. A STALE range record is worse than none, because publication would then
  assert a verdict that does not describe the code being published, whereas
  "these commits have no record" is at least true. Any such record must be bound
  to an exact identity and refused when it does not match.
- **Each entry carries its own identity and is refused independently.** A single
  record spanning a per-commit verdict and a range verdict would inherit the
  range half's staleness and go stale atomically, discarding valid per-commit
  evidence along with the invalid range evidence.

Two reference designs were compared and neither was copied wholesale; they are
recorded because the comparison is the reusable part. gentle-ai answers the
KEYING question: its review-context records carry `target_identity` and
`revision` as `sha256:` digests rather than commit SHAs, plus a `lineage_id` that
threads the operations performed on one candidate — so hashing the candidate
makes a range, a net diff or any arbitrary candidate addressable, and the
obstacle was never that a range has no key but that the ledger indexed by commit.
Only the on-disk shape of two records and the documented contract were inspected;
whether it supports per-finding human disposition comparable to
`refute`/`accept`/`reopen` is UNVERIFIED. no-mistakes answers durability and
shareability instead: it anchors evidence in git itself through a create-only
compare-and-swap ref and publishes a run's evidence to an orphan branch on the
same remote, bounded at 500 files / 256 MB / 64 MB per file because evidence is
agent-produced and a runaway recording must fail the publish closed. The
trade-off to state before copying it: that evidence is PUSHED, hence visible to
anyone who can read the repository, whereas this project's ledger is deliberately
machine-local under the git common directory. Choosing one is choosing who the
audit trail is for.

**Item 10 (piece 4): `pr review` authors and persists the branch judgement.**
Before it, no flow received a statement of what the change was supposed to do.
The per-commit path at least passed the real commit message; the `pr review` and
`pr create` net audits instead received a literal `HonestNetIntention` constant
reading "No PR title/description exists before publication", while the `netAxes`
block went on to ask the reviewer whether the PR delivers what its title and
description promise. The reviewer was asked to verify conformance to something
it had just been told did not exist — worse than an empty field. That constant is
gone: `pr review` reads the recorded range intent, and when none is available it
sends an explicit no-recorded-intent value rather than leaving the reviewer to
infer an absent field.

What was corrected against an earlier reading of the item, and is worth keeping:
`pr review` is not a per-commit audit repeated at branch scale. Its net half is a
distinct unit — one `AuditCommit` over the whole range diff, planned from that
diff's own aggregate risk, asking about integration between commits, one commit
undoing another, net regression, undeclared contract breaks and net coverage —
and it carries a deterministic classifier that checks against git blobs whether a
later commit removed the code an earlier finding pointed at, rather than asking a
model to notice. `pr review` also rejects `--audit-pending`: it reports the gap
and points the operator at `sentinel review <sha>`.

Landed across PRs 7, 9, 10, 11 and 12 (`34b4f45`, `6d5fe1e`, `00b9c29`,
`a9b1881`, `e62e7e8`).

**Item 11 (piece 5): `pr create` publishes the stored judgement instead of
recomputing it.** Landed as `b1c53be` (PR 13). Before it, `RunPrCreateWith`
called the same `review.AnalyzeBranch` that `pr review` calls, from scratch, on
every invocation — re-deriving the merge base, re-reading the range diff and
re-running the net audit from zero, so an operator who reviewed before publishing
paid for it twice.

The subject of the report is the constraint that shaped the result, and getting
it wrong was the failure the item existed to prevent: a published PR report is
about the QUALITY OF THE IMPLEMENTED WHOLE, not about the quality of the commits
that carried it. Per-commit fichas answer a different question and are not the
raw material of a PR report, however cheap concatenating them would be.

`pr create` now audits nothing. It requires a persisted `pr review` entry for the
branch whose head matches the current head and whose evidence is committed,
validates that attestation before any validation, push or publication, and exits
`1` naming what is missing rather than auditing its way out. It then replaces
only the `ci` details block and its attestation step in the stored body — every
other byte kept verbatim — and publishes the stored title through `--title` and
the composed body through `--body-file`. That boundary keeps a single author for
the branch-level judgement, so a PR cannot be published carrying a verdict nobody
could reproduce by running `pr review` themselves.

Deliberately unchanged: publication still blocks only on a red deterministic
validation, overridable with `--force --reason`. The semantic verdict stays
advisory, and composing the report from a record did not quietly turn it into a
gate.

One measurement the items called for was never run and is recorded as not owed:
comparing net-diff findings against the union of per-commit findings for the same
commits. It was a cost question posing as a design question. Even at total
overlap, a report assembled from per-commit fichas answers "how well is each
piece made" when a pull request asks "what does this change do as a whole, and is
it well resolved". The range entry was piece 5's deliverable, not an optimisation
of it. What the measurement would still decide is narrower: whether the
per-commit audit is worth running at all when the net audit covers the same
ground.

### The tmpfs that is memory: bounded, owned, and swept (items 16 and 5, landed 2026-09-17)

Three reviews were killed in one session on 2026-09-15 while `free` reported
5.2 GB available and the summed RSS of every process was under 1.1 GB. It was
not a shortage of anonymous memory. `/tmp` is tmpfs on this host, so its
contents ARE memory: it held 486 MB of orphaned provider context and 107 MB of
Bun residue. Removing them let the reviews complete. That measurement is what
parked item 1 — the ceiling it wanted may not be needed once the residue stops
accumulating — and these commits are the precondition it named.

**Item 16's stated premise was wrong, and correcting it is half of what was
learned.** The item said nothing collected provider context directories.
`reapSharedStore` did collect them, by age, and had all along. Reading the code
before believing the issue is what found that, and the same discipline applied
one step later would have avoided the reverted commit below: the other thing the
item suspected turned out to be deliberate, and the file said so.

**What looked like defect one was the design, and a code review caught it
before merge.** `storeCapacityLimit` is one tenth of the filesystem, about
380 MB here, and it was compared against `sharedStoreSnapshotBytes`, whose
filter keeps only `sha-` prefixed entries. The store measured 500 MB. Item 16
flagged that gap as a hypothesis worth checking, and the numbers matched, so it
was committed as a fix — counting provider state toward the ceiling (`39db2c5`).

That was wrong, and the reason was written in the file being edited. The doc
comment on `storeCapacityLimit` already stated the intent: measuring only
entries this package can remove is what "keeps reusable snapshots on an
otherwise-full filesystem". Eviction can only remove published trees. Counting
an unevictable floor turns the ceiling into a shredder: once provider state
alone exceeds the limit, the eviction loop exhausts every candidate, drops every
reusable tree, is still over the limit, and does it again on the next `Create`.

It was reachable by design rather than by accident. `newReviewEnvironment` runs
per OpenCode invocation, so per dimension: `review.parallel: 2` means two live
provider roots, the configuration item 1 documents used five, and item 16
measured these directories at 162 MB each. Three live roots exceed the 380 MB
ceiling with no orphan involved.

So `39db2c5` was reverted rather than repaired. A floor-subtraction — evict only
down to `limit - providerBytes`, skip when that is not positive — is coherent
and was rejected: it invents a second policy to make a regression survivable
instead of removing it. The rule that stands is the original one, now written
down at the measuring site rather than only at the limit: a ceiling measures
what its lever can move.

The observation behind item 16's hypothesis was still true; only the diagnosis
was wrong. The 500 MB was measured with orphaned provider directories present,
and what bounds those is collecting them at the source, which is the next two
commits rather than the accounting.

Two tests survive the revert and one is new. `TestCapacityReaperNeverSelectsProviderState` pins that eviction never touches provider state, which held
before and after. `TestCapacityReaperIgnoresProviderStatePressure` pins the
regression itself: provider state dwarfing the ceiling while the published tree
fits under it must evict nothing. Reintroducing the counting makes it fail with
one tree evicted, which is the shredder in miniature.

**Defect two: collection was late, and lateness was the whole problem.** Age
alone answers the wrong question — an orphaned directory is not old, it is
ownerless — and the sweep only ran on whatever `Create` happened next. A review
killed for memory pressure therefore left 162 MB resident through exactly the
window in which the next review ran, making each kill more likely to cause the
following one.

The owning PID now travels in the directory name, so the reaper decides from a
`ReadDir` alone. A sidecar file was rejected: it adds a write that a killed
process can skip, which is precisely the case this has to survive.
`ProviderStatePattern` and `ProviderStatePID` hold that format in one place and
`internal/agentadapter` builds the name through them. Landed as `0016c1f`.

`processAlive` is COPIED into `internal/reviewsnapshot` rather than shared with
`internal/git`, which owns the original from R1. This is not a style preference:
`internal/git` already imports `internal/reviewsnapshot`, so importing it back
closes an import cycle and does not compile. The copy keeps R1's fail-open
asymmetry verbatim — uncertain means alive — so PID reuse can only defer a
collection, never cause a false one. Verify the invariant with
`go list -deps ./internal/reviewsnapshot | grep -c internal/git`, which must
stay `0`.

Age remains the backstop rather than a rule that was replaced. A name carrying
no parseable owner is collected exactly as before, and a live owner is still
collected once it passes the age ceiling.

**Item 5: the residue next door was never ours and was fifteen times larger.**
Each restricted reviewer invocation leaves a roughly 14 MB `.<hex>-00000000.so`
file written by the Bun runtime OpenCode ships. Measured at 540 files and
2,945 MB on one occasion, and at 8 files and 82 MB while this work was being
written. No ceiling of ours touched it, and the symptom surfaces inside an
unrelated review as `no space left on device`, which invites blaming the
snapshot store — a misdiagnosis that already cost an afternoon once.

The existing temporary-directory sweep gained a separate case rather than a
wider prefix, because the directory is shared with other programs.
`isBunResidueName` requires the whole shape and the caller requires a regular
file. Age is the only signal and that is a property of the problem, not a
shortcut: the file belongs to the Bun runtime, so there is no owner to ask.
`staleSnapshotAge` is reused rather than joined by a second threshold, and a
fresh matching file is kept, because a live invocation's file must never be
removed. The near misses are pinned as tests — no leading dot, a non-hex digit,
a wrong suffix — since a laxer pattern would delete other programs' files.
Landed as `3c0891e`.

**How the absence of regressions was established, because it is the part worth
copying.** Each commit's tests were run against the previous code with the
production change reverted, and in every case exactly the intended case failed
while the rest passed: one of six liveness cases, one of two entries in the
residue sweep. "Nothing that was collected before stopped being collected" is
therefore a measurement rather than a claim.

**And the limit of that method, which this work also demonstrates.** Every one
of those checks passed for the reverted commit too. Proving a change does what
it intends says nothing about whether it should; the counting change was
correct code for a goal that was wrong, and only a reviewer reading the
surrounding design caught it. A green RED-to-GREEN transition is evidence about
mechanism, never about premise.

**What this does NOT close.** Item 1 keeps its ceiling question and is still
parked. Its precondition is now met, but the re-measurement it asked for has not
been run, and a ceiling derived before that number exists would repeat the
mistake item 6 records for the turn budget. One observation for whoever runs it:
with no killed reviews in a session the store holds zero orphaned provider
directories, which matches item 16's note that a review which completes leaves
none. The re-measurement has to reproduce the kill, not merely run a healthy
review.

### `doctor` stopped denying the refusal the agent had already printed (item 18, landed as `a6da249`)

Opened 2026-09-15 from a measured hour lost. `sentinel doctor` reported
`opencode did not answer the probe prompt within 1m0s (the probe's own budget,
not a provider refusal)` and told the operator to check for a wedged process.
Running the same binary seconds later printed `Error: The usage limit has been
reached`. Every clause was false: the provider had refused, it said so, and no
process was running at all.

The message was the symptom. The defect was three linked causes.

The primary one made the adapter's error **unreachable**, not merely unlucky.
`productionDoctorEnv` handed `ProbeAdapterFor` a 60s budget and then raced it
against `time.After` on the same 60s. The adapter's context fires at T=60s,
*then* `cmd.Run` returns, *then* the error is built and sent. That ordering is
fixed, so whenever the child outlived the budget the outer branch won
deterministically and the provider's own words could never arrive. The fix is
not to delete the outer bound — it plausibly guards a process `exec` cannot
kill, where a grandchild holds the pipe open and `Wait` blocks past the
context kill. Instead the two branches were made to mean different things: the
adapter keeps the 60s budget and the outer bound waits one 5s grace longer, so
the adapter wins every ordinary deadline kill and the outer branch fires only
for a call the adapter itself cannot end. That is the one case where "wedged"
is honest.

Second, `runCommandWithTimeout` never set `cmd.Stderr` and returned the bare
process error, so the refusal was discarded at the source. It now captures both
streams into an exported `CommandFailure{Output, TimedOut, Err}`. The precedent
was already in the same file: `runCommitMessageCommand` had been wrapping
stderr all along.

Third, the false premise was asserted in **five** places, not one: the
`ProbeTimeout` doc comment, its `Error`, the rendered detail, the
`productionDoctorEnv` doc comment, and the `doctor` help text. The fourth is the
one that mattered most, because it stated the wrong invariant as design
rationale — "provider refusals arrive fast, so anything slower than the bound
means the agent is slow or wedged" — and would have had the next reader
re-derive the bug. A comment recording a falsehood is more expensive than the
code it describes.

What `doctor` says now: with captured output it prints the agent's own text and
calls the cause **indeterminate**, because a timed-out probe genuinely cannot
distinguish a refusal from a hang and should say so rather than pick one.
Without captured output it says the cause is unknown and names the command the
operator can run by hand. It never again asserts the negative. `AcpxBridge` was
left alone; item 18 pre-authorized that fallback, and the acpx path already
surfaces the provider error through the ordinary branch.

`WaitDelay` was evaluated and deliberately not adopted: it turns a successful
call with a lingering grandchild into an error, and would still need the grace
bound.

Verification worth reusing: revert only the production change and confirm
exactly the intended case fails. Dropping `cmd.Stderr` alone made the new test
fail with `error = "signal: killed"` — the literal symptom this item reports,
and proof that a fix limited to the fast-exit path would have left it intact.

Two review notes were raised against this change and both premises were
disproven in the code. `exec.ExitError.Stderr` is populated only by `Output`
and `CombinedOutput`, never by `Run`, and this path always used `Run`, so
setting `cmd.Stderr` regressed nothing. And `ProbeAdapterFor` passes the probe
budget explicitly rather than reading `review.timeout`, so the adapter and outer
budgets match by construction for both the CLI and acpx families.

### `pr review` stopped printing `unavailable` with no cause (item 17, landed as `35927d8`)

The evidence was never missing. `internal/review/engine.go` builds every
unavailable dimension as `DimensionResult{Verdict: unavailable, Reason:
err.Error(), InvocationID: …}`, and `Reason` always carries the provider's real
error text — not a fixed token. Those results reach
`res.Net.Audit.Dims[i].Result` intact. `AuditResult.String()` even renders
`↳ reason=%q class=%s` per unavailable dimension. It is printed by exactly one
caller, `cmd/sentinel/review_command.go:340`, which is `sentinel review`. The
`pr review` net path never calls it, and `VerdictLine` renders only the verdict
and a finding count. So the reason existed in memory, three frames from the
console, and was dropped at the last one.

The scope decision that shaped the fix: `RenderPRReviewBody` produces the string
stored as `store.PRReviewEntry.Body`, and `pr create` publishes that same stored
body through `gh`. The persisted entry and the published PR description are one
string, so they cannot be separated by picking a different render seam. A denied
`todowrite` permission is internal diagnostics and does not belong in a public PR
description. The cause therefore goes to the console
(`internal/app/pr/review.go`), to a new `PRReviewEntry.UnavailableCauses` field
(`omitempty`, so existing entries still decode), and to
`PrReviewJSONOutput["net_unavailable_causes"]` so a machine consumer sees it too.
`VerdictLine`, `RenderPRReviewBody` and `RenderBranchPRTemplate` were left
untouched, and a test asserts the denial text does not appear in the persisted
body.

Two branches, the same shape item 18 settled on for `ProbeTimeout`: a recorded
reason is named with its class, and an absent one says the cause is unrecorded
and prints the invocation id plus `sentinel runs logs --run <run-id>`. The
admission-vs-infrastructure classification reuses
`reviewexec.IsAdmissionReason`, so no second rule exists. Nothing here changes a
verdict; it follows the `ContextSkipReason` precedent of observational-only
metadata.

Verification: reverting only the console loop made both new tests fail with the
literal reported symptom — `⛔ **Net audit verdict: unavailable** — 0 finding(s)
on net diff 89abcde..0123456` and nothing else.

Left alone deliberately: `engine.go:1209` still carries its own `"admission: "`
string literal rather than the shared `reviewexec.AdmissionReasonPrefix`. It
predates this change and duplicating the constant is not what item 17 reports.

### The audit prompt now renders its shared evidence first (item 7, partial, landed as `4c6fa11`)

Measured before touching anything, and the number is the point. On commit
`a6da249` (14.6 KB diff), a six-dimension audit sent 98104 bytes. The longest
common prefix across those six prompts was **68 bytes — 0.42% of everything
sent**. The cause was ordering, not volume: `buildPromptWithContext` opened with
`Audit ONE <unit> against the "<dim>" dimension`, so all six prompts diverged at
byte 68, before the commit message, the diff, the CodeGraph context and the
permitted paths — which are byte-identical across every dimension of one audit.
The ~14.6 KB of shared evidence was re-sent six times with zero provider
prefix-cache eligibility.

The shared evidence envelope now renders first and the dimension contract plus
output schema render as a suffix, which is item 7's own closing condition. On
the test's fixture the shared prefix went from 68 bytes to 7881, from under 1%
to 85% of the shortest prompt. Verified as a pure reordering: the rendered
prompt's set of lines is identical before and after, so no instruction was
dropped, added or reworded. The anti-injection rule moved with it and now
appears BEFORE the first untrusted block rather than after, which it has to,
since those blocks are now earlier.

Nothing else changed. No cache mechanism, no cache key, no provider flag, no
change to tool policy, evidence policy, dimension coverage or severity rules.
`Fingerprint` was verified not to take prompt text as an input — it hashes only
Dimension, symbol/path, Evidence and Title — so a reorder cannot move a
fingerprint.

A latent defect was fixed on the way, found while verifying the diff rather
than reported by the writer. `answersSection` used to be concatenated INTO the
`fmt.Sprintf` format string, so a `--answer` containing a verb consumed
positional arguments. An answer of `the threshold is 100%d and %s of cases`
rendered as `100%!d(string=BEGIN_REVIEW) and END_REVIEW of cases` followed by
`%!s(MISSING)`: the output-schema delimiters were eaten and the reviewer
received a broken output contract, which can only have produced an unparseable
answer and therefore an `unavailable` verdict. Passing the section as an
argument makes it inert, and
`TestBuildAuditPromptAnswersWithFormatVerbsAreLiteral` locks that in.

**Item 7 stays OPEN, deliberately.** Its closing condition names four
measurements — input tokens, cached-token reads, latency and
review-equivalence — and only the first has a number. The 0.42%→85% figure is
cache ELIGIBILITY computed from the rendered bytes; it is not evidence that the
provider grants a prefix cache on the `opencode run --pure` path, not a latency
measurement, and not proof that reordering preserves verdicts. The durable
store held zero measured runs when this landed, so no cached-token reads were
available to consult. What remains of item 7 is that live measurement plus the
provider cache-key question, and neither should be answered by argument.

`--pure` was checked and does not foreclose it: it means "run without external
plugins" and says nothing about sessions or caching.

### Cache-write tokens got a destination (item 7 prerequisite, landed as `e647010`..`bb4ce77`)

The live audit of 2026-09-17 produced 3065189 cached input tokens against 322
fresh ones and could not be interpreted. The cold one-dimension probe — zero
prior invocations — already reported 126705 cached READS on its own, so reads
are dominated by intra-invocation multi-turn re-reads and the provider's own
system-prompt cache, not by one dimension reusing another's. Item 7 turns on
exactly that distinction, and the number that draws it was being discarded.

Both adapters already observed it and dropped it for the same stated reason.
`claudeUsageProbe` recorded that `cache_creation_input_tokens` "is observed on
the wire but intentionally unmapped: acpadapter.Usage has no destination for
it", and `opencodeUsageTokens` said the same of `cache.write`, adding that
"inventing one is a store-schema change outside this slice". This was that
change: `acpadapter.Usage` gained `CacheWriteInputTokens`, threaded to every
surface that already carried `CachedInputTokens` — the adapters, the execution
observation and its clone, the store record and its validation list, the
metrics aggregate and `mergeUsage`, and `sentinel metrics` in both text and
JSON.

acpx was checked rather than assumed: its captured wire (`end-turn.jsonl`,
`cancelled.jsonl` and the helper-process transcripts) reports no cache-write
member, so the field stays nil there and the comment records why. No key was
invented.

Plumbing only, deliberately. No ratio, no cache-hit rate, no derived metric.
What to compute from the two numbers is item 7's decision, and it should be
made against a measurement rather than ahead of one.

The four properties that needed pinning are pinned: the Claude adapter surfaces
cache creation from a probe-shaped usage member and keeps an observed zero as a
non-nil pointer; the OpenCode adapter SUMS `cache.write` across step_finish
events (5 + 7 = 12, non-zero on both steps, so the sum is genuinely exercised);
tokens with no cache member leave the field nil and it renders as `null`, never
`0`; and a record persisted before this change still decodes with the field
nil. Verified by reverting only the two adapters, which failed exactly those
tests with `CacheWriteInputTokens = nil`.
