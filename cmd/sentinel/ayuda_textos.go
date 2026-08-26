package main

// Long English help texts referenced by the ayudaComandos registry in
// ayuda_comandos.go. Keeping them here lets the registry stay a compact
// key-to-constant map. The bytes are moved verbatim from the former inline
// literals, so every help output stays byte-for-byte identical.

import "fmt"

// Usage lines shared between runtime error paths and --help output so the two
// can never drift apart. Call sites keep their exact historical bytes by
// prefixing these constants.
const (
	usoExplainArgs = "sentinel explain [<base>..<head>] [--json]"
	usoExplain     = "uso: " + usoExplainArgs

	usoSliceApply        = "Uso: sentinel slice apply --plan <plan.json> --answers <respuestas.json>"
	usoConsentimientoDif = "Uso: sentinel consentimiento-diff otorgar|revocar|estado"

	usoRunsStart         = "Usage: sentinel runs start --prompt <text> [--policy-id <id>]"
	usoRunsStatus        = "Usage: sentinel runs status [--run <id>] [--json]"
	usoRunsLogs          = "Usage: sentinel runs logs --run <id> [--after <cursor>] [--limit N]"
	usoRunsRespond       = "Usage: sentinel runs respond --run <id> --text <answer>"
	usoRunsAbort         = "Usage: sentinel runs abort --run <id>"
	usoRunsRetry         = "Usage: sentinel runs retry --run <id> [--expected-revision N]"
	usoRunsRecoverRepair = "Usage: sentinel runs recover --repair <id>"
	usoRunsVerify        = "Usage: sentinel runs verify --run <id>"
	usoRunsPrune         = "Usage: sentinel runs prune --older-than <duration> [--json]"
	usoRunsAttach        = "Usage: sentinel runs attach [--run <id>] [--after <cursor>] [--follow]"
	usoRunsDaemon        = "Usage: sentinel runs daemon start|status|stop"
)

const (
	textoAyudaCheck = `Purpose: measure added authored code lines. Whole-worktree measurement is advisory; --staged enforces the 400-line review budget for the pending commit candidate.

Usage:
  sentinel check [--json] [--staged]

Flags:
  --json     Emit the machine-readable report instead of text.
  --staged   Measure only the staged commit candidate; rejects over 400 authored lines with exit 1.

Examples:
  sentinel check
  sentinel check --staged
`
	textoAyudaSlice = `Purpose: split pending modifications into reviewable commits of at most 400 authored lines.

Usage:
  sentinel slice                Interactive flow (requires stdin).
  sentinel slice plan [--json]  Propose selections without committing; exit 3 when user decisions are pending.
  sentinel slice apply --plan <plan.json> --answers <answers.json>
                                Apply an approved plan; commits only when every decision has an explicit answer.

Flags:
  --json     Emit the plan as JSON (slice plan).
  --plan     Path to the plan produced by 'slice plan' (slice apply).
  --answers  Path to the JSON answers for every pending decision (slice apply).

Example:
  sentinel slice plan --json > plan.json
`
	textoAyudaSlicePlan = `Purpose: propose reviewable selections without committing anything; exit 3 when user decisions are pending.

Usage:
  sentinel slice plan [--json]

Flags:
  --json  Emit the plan as machine-readable JSON.

Example:
  sentinel slice plan --json > plan.json
`
	textoAyudaSliceApply = `Purpose: execute an approved plan, committing only when every pending decision has an explicit user answer.

Usage:
  ` + usoSliceApply + `

Flags:
  --plan     Path to the plan produced by 'slice plan' (required).
  --answers  Path to the JSON answers keyed by decision id (required).

Example:
  sentinel slice apply --plan plan.json --answers answers.json
`
)

const (
	textoAyudaReview = `Purpose: audit one or more commits by dimension and store an append-only review record.

Usage:
  sentinel review [<sha>|<expr> ...] [--dims a,b] [--profile X] [--answer "..."] [--timeout N] [--all] [--chain] [--gate] [--json] [--prune]

Flags:
  --dims     Comma-separated subset of dimensions to audit (logic, style, design, tests, security, spec).
  --profile  Review profile override.
  --answer   Answer text recorded with the revision.
  --timeout  Per-call agent timeout override in seconds.
  --all      Audit every commit up to the target that has no review record yet.
  --chain    Audit the whole range from upstream/main to the target.
  --gate     Escalate any CRITICAL verdict to exit code 1.
  --json     Emit machine-readable output.
  --prune    Standalone cleanup of review records for commits that no longer exist.

Example:
  sentinel review HEAD --dims logic,tests --gate
`
	textoAyudaGate = `Purpose: run deterministic validation followed by semantic review of HEAD.

Usage:
  sentinel gate --stage pre-commit|pre-push|pr [--profile X]

Flags:
  --stage    Lifecycle stage invoking the gate (required): pre-commit, pre-push, or pr.
  --profile  Validation profile from validation.profiles (default standard). It selects validation configuration, never review configuration.

Example:
  sentinel gate --stage pre-commit
`
	textoAyudaStatus = `Purpose: guardian summary: worktree volume, review records, and recent events.

Usage:
  sentinel status [--json] [--prune]

Flags:
  --json   Emit machine-readable JSON instead of text.
  --prune  Delete review records for commits that no longer exist, then report.

Example:
  sentinel status --json
`
	textoAyudaExplain = `Purpose: explain the change profile, detected characteristics, risk, and cohesion of a commit range.

Usage:
  ` + usoExplainArgs + `

Flags:
  --json  Emit machine-readable JSON instead of text.

Example:
  sentinel explain HEAD~3..HEAD
`
	textoAyudaConsentimientoDif = `Purpose: manage the local per-user consent to expose diffs to external agents (required before slice can generate commit messages through an agent).

Usage:
  ` + usoConsentimientoDif + `

Actions:
  otorgar  Grant consent for this repository.
  revocar  Revoke consent for this repository.
  estado   Show the current consent state.

Example:
  sentinel consentimiento-diff otorgar
`
)

const (
	textoAyudaPr = `Purpose: pull-request operations. Without a recognized subcommand the arguments pass through to 'gh pr create'.

Usage:
  sentinel pr [gh pr create passthrough arguments]
  sentinel pr create [...]
  sentinel pr review [...]

Run 'sentinel help pr create' or 'sentinel help pr review' for their flags.
`
	textoAyudaPrCreate = `Purpose: analyze the branch, apply the blocking gate, and publish the pull request through gh with the honest verification template.

Usage:
  sentinel pr create [--base X] [--chain-pr] [--force --reason "..."]

Flags:
  --base      Comparison branch for the analysis.
  --chain-pr  Publish the full branch even when it exceeds the review budget.
  --force     Override a red validation verdict; requires --reason.
  --reason    Explicit motive recorded alongside --force.

Example:
  sentinel pr create --base main
`
	textoAyudaPrReview = `Purpose: dry-run analysis of the unpublished branch: audit matrix, summary, and the single-versus-chained PR decision. Publishes nothing.

Usage:
  sentinel pr review [--base X] [--only-unaudited] [--overview] [--json]

Flags:
  --base            Comparison branch (default main).
  --only-unaudited  Restrict the analysis to commits without a review record.
  --overview        Include the PR overview in the analysis.
  --json            Emit machine-readable JSON.

Example:
  sentinel pr review --base main --json
`
)

const (
	textoAyudaRunsStart = `Purpose: start a durable run from an operator prompt executed by the configured agent chain.

Usage:
  ` + usoRunsStart + ` [--json]

Flags:
  --prompt     Prompt text for the agent (required).
  --policy-id  Policy identifier (default operator).
  --json       Emit machine-readable JSON.

Example:
  sentinel runs start --prompt "refactor the parser" --json
`
	textoAyudaRunsStatus = `Purpose: show durable-run state summaries.

Usage:
  ` + usoRunsStatus + `

Flags:
  --run   Restrict the report to one run.
  --json  Emit machine-readable JSON.

Example:
  sentinel runs status --run r1 --json
`
	textoAyudaRunsRespond = `Purpose: answer a pending decision of one awaiting run.

Usage:
  ` + usoRunsRespond + ` [--json]

Flags:
  --run   Run awaiting the decision (required).
  --text  Response text delivered to the run (required).
  --json  Emit machine-readable JSON.

Example:
  sentinel runs respond --run r1 --text "use option B"
`
	textoAyudaRunsAbort = `Purpose: request cancellation of one active run.

Usage:
  ` + usoRunsAbort + ` [--json]

Flags:
  --run   Run to cancel (required).
  --json  Emit machine-readable JSON.

Example:
  sentinel runs abort --run r1
`
	textoAyudaRunsRetry = `Purpose: retry a failed run under optimistic concurrency control.

Usage:
  ` + usoRunsRetry + ` [--json]

Flags:
  --run                Failed run to retry (required).
  --expected-revision  Refuse unless the stream head matches this revision.
  --json               Emit machine-readable JSON.

Example:
  sentinel runs retry --run r1 --expected-revision 4
`
	textoAyudaRunsRecover = `Purpose: inspect recovery classes and repair terminal-but-unprojected runs.

Usage:
  sentinel runs recover                     Read-only scan of every non-terminal run; exit 4 when an entry needs an operator decision.
  sentinel runs recover --run <id> [--expected-revision N]
                                            Recovery context for one run.
  ` + usoRunsRecoverRepair + `
                                            Rebuild the lagging snapshot by replaying the whole verified stream under one lock.

Flags:
  --run                Target run.
  --expected-revision  Refuse stale repairs unless the stream head matches.
  --repair             Identity of the run to repair.
  --json               Emit machine-readable JSON.

--expected-revision is rejected on the scan and cannot combine with --repair.
`
	textoAyudaRunsVerify = `Purpose: verify the integrity of one run's event stream.

Usage:
  ` + usoRunsVerify + ` [--json]

Flags:
  --run   Run to verify (required).
  --json  Emit machine-readable JSON.

Example:
  sentinel runs verify --run r1
`
	textoAyudaRunsPrune = `Purpose: explicit operator maintenance: remove ONLY terminal execution records older than the cutoff that no review provenance references. Nothing purges automatically.

Usage:
  ` + usoRunsPrune + `

Flags:
  --older-than  Duration cutoff for the last event of candidate records (required).
  --json        Emit machine-readable JSON.

Example:
  sentinel runs prune --older-than 720h --json
`
	textoAyudaRunsAttach = `Purpose: observe one durable run live: list attachable candidates, print a point-in-time snapshot, or follow a run with the terminal UI.

Usage:
  ` + usoRunsAttach + `

Modes:
  without --run        List every non-terminal durable run as an attach
                       candidate (read-only reconciled projection walk);
                       exits 0 whether the listing is empty or not.
  --run <id>           Print one plain-text observation snapshot rebuilt
                       from Inspect plus event replay strictly after
                       --after (default 0). Point-in-time view: terminal
                       and non-terminal runs alike exit 0.
  --run <id> --follow  Launch the Bubble Tea attach view over the same data
                       pipeline; it keeps repolling from the last applied
                       cursor until the run reaches its terminal state or
                       you detach.

Flags:
  --run    Run to observe.
  --after  Replay events strictly after this cursor.
  --follow Follow mode; requires --run.

Exit contract:
  0  clean quit in every mode: a followed run reaching its terminal state
     freezes the view and exits 0, and SIGINT/SIGTERM detach exactly as
     cleanly as pressing q. Other failures follow the shared runs codes
     (2 run not found, 4 invalid state, 5 infrastructure).

Follow-mode keys (the live footer lists only the keys active in the current
session state):
  q quit · r refresh · a abort · e respond · y retry (when retryable)
`
	textoAyudaRunsDaemon = `Purpose: manage the repository-local foreground daemon.

Usage:
  ` + usoRunsDaemon + `

Subcommands:
  start   Claim the repository, settle auto-recoverable interrupted runs,
          serve the local transport, and block until SIGINT/SIGTERM or a
          remote stop.
  status  Print the live owner pid/started-at/host/transport/address.
  stop    Ask the running daemon to shut down gracefully and print its
          orphaned-runs summary.

Flags:
  none. The three subcommands take no flags by contract: any argument beyond
  the subcommand name is rejected as a usage error.

Exit codes:
  start   0 on a clean stop; 4 when another live daemon already owns the
          repository (its pid is named); 5 otherwise.
  status  0 while a live daemon answers; 2 with a deterministic message when
          no live daemon is running for this repository; 5 when endpoint.json
          exists but is unreadable or incomplete — corruption is never silent.
  stop    0 after a graceful shutdown; a missing or unreachable endpoint
          follows the same not-running contract as status (exit 2); 5 on a
          corrupt record or a daemon-side shutdown failure.
`
)

// textoAyudaRunsLogs interpolates fmt.Sprint(runsLogsDefaultLimit), which is
// not a constant expression, so this one entry is a var. The emitted bytes
// match the former inline literal exactly.
var textoAyudaRunsLogs = `Purpose: read the verified event stream of one run with cursor pagination.

Usage:
  ` + usoRunsLogs + ` [--json]

Flags:
  --run    Run whose events are read (required).
  --after  Read events after this cursor.
  --limit  Maximum number of events (default ` + fmt.Sprint(runsLogsDefaultLimit) + `).
  --json   Emit machine-readable JSON.

Example:
  sentinel runs logs --run r1 --after 7 --limit 20 --json
`
