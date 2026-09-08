package main

// Long English help texts referenced by the commandHelpTexts registry in
// ayuda_comandos.go. Keeping them here lets the registry stay a compact
// key-to-constant map. The bytes are moved verbatim from the former inline
// literals, so every help output stays byte-for-byte identical.

import "fmt"

// Usage lines shared between runtime error paths and --help output so the two
// can never drift apart. Call sites keep their exact historical bytes by
// prefixing these constants.
const (
	explainUsageArgs = "sentinel explain [<base>..<head>] [--json]"
	explainUsage     = "usage: " + explainUsageArgs

	sliceApplyUsage  = "Usage: sentinel slice apply --plan <plan.json> --answers <answers.json>"
	consentDiffUsage = "Usage: sentinel consent-diff grant|revoke|status"

	runsStartUsage         = "Usage: sentinel runs start --prompt <text> [--policy-id <id>]"
	runsStatusUsage        = "Usage: sentinel runs status [--run <id>] [--json]"
	runsLogsUsage          = "Usage: sentinel runs logs --run <id> [--after <cursor>] [--limit N]"
	runsRespondUsage       = "Usage: sentinel runs respond --run <id> --text <answer>"
	runsAbortUsage         = "Usage: sentinel runs abort --run <id>"
	runsRetryUsage         = "Usage: sentinel runs retry --run <id> [--expected-revision N]"
	runsRecoverRepairUsage = "Usage: sentinel runs recover --repair <id>"
	runsVerifyUsage        = "Usage: sentinel runs verify --run <id>"
	runsPruneUsage         = "Usage: sentinel runs prune --older-than <duration> [--json]"
	runsAttachUsage        = "Usage: sentinel runs attach [--run <id>] [--after <cursor>] [--follow]"
	runsDaemonUsage        = "Usage: sentinel runs daemon start|status|stop"
)

const (
	checkHelp = `Purpose: measure added authored code lines. Whole-worktree measurement is advisory; --staged enforces the 400-line review budget for the pending commit candidate.

Usage:
  sentinel check [--json] [--staged]

Flags:
  --json     Emit the machine-readable report instead of text.
  --staged   Measure only the staged commit candidate; rejects over 400 authored lines with exit 1.

Examples:
  sentinel check
  sentinel check --staged
`
	sliceHelp = `Purpose: split pending modifications into reviewable commits of at most 400 authored lines.

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
	slicePlanHelp = `Purpose: propose reviewable selections without committing anything; exit 3 when user decisions are pending.

Usage:
  sentinel slice plan [--json]

Flags:
  --json  Emit the plan as machine-readable JSON.

Example:
  sentinel slice plan --json > plan.json
`
	sliceApplyHelp = `Purpose: execute an approved plan, committing only when every pending decision has an explicit user answer.

Usage:
  ` + sliceApplyUsage + `

Flags:
  --plan     Path to the plan produced by 'slice plan' (required).
  --answers  Path to the JSON answers keyed by decision id (required).

Example:
  sentinel slice apply --plan plan.json --answers answers.json
`
)

const (
	reviewHelp = `Purpose: audit one or more commits by dimension and store an append-only review record.

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
	refuteHelp = `Purpose: record an evidence-bound human refutation of one reviewed finding.

Usage:
  sentinel refute --sha SHA --fingerprint FP --reason TEXT --line-start N --line-end M

Flags:
  --sha          Reviewed commit SHA holding the finding (required, exact).
  --fingerprint  Stable fingerprint of the finding to answer (required, exact).
  --reason       Why the finding's premise is false (required, non-empty).
  --line-start   First evidence line, 1-based (required).
  --line-end     Last evidence line, 1-based; at most 20 lines per range (required).

The evidence path is taken from the finding itself, never from the caller.
The evidence is read from the audited Git object and must match the snapshot
exactly; the finding's line must sit inside the range. A missing or ambiguous
fingerprint, an unsafe path, and a corrupt dispositions log all fail closed
without persisting anything. The answer is appended to the separate
dispositions log: persisted review revisions are never mutated. A valid
refutation clears only its matching finding; accepted_by_user never clears a
block, fixed clears it, and reopened blocks again.

Example:
  sentinel refute --sha abc12345 --fingerprint 1f4902c8 --reason "the committed implementation is safe" --line-start 2 --line-end 2
`
	acceptHelp = `Purpose: record a human acceptance of one reviewed finding.

Usage:
  sentinel accept --sha SHA --fingerprint FP --reason TEXT

Flags:
  --sha          Reviewed commit SHA holding the finding (required, exact).
  --fingerprint  Stable fingerprint of the finding to answer (required, exact).
  --reason       Why the risk is acknowledged (required, non-empty).

Acceptance documents judgement without clearing the block: the finding keeps
blocking under the shared rule, exactly as before. Only an unanswered finding
(pending, confirmed, or legacy status-less) can be accepted; an already
answered one, an unsafe path, and a corrupt dispositions log all fail closed
without persisting anything. The answer is appended to the separate
dispositions log: persisted review revisions are never mutated.

Example:
  sentinel accept --sha abc12345 --fingerprint 1f4902c8 --reason "residual risk acknowledged for this release"
`
	reopenHelp = `Purpose: record an evidence-bound human reopen of one cleared finding.

Usage:
  sentinel reopen --sha SHA --fingerprint FP --reason TEXT --line-start N --line-end M

Flags:
  --sha          Reviewed commit SHA holding the finding (required, exact).
  --fingerprint  Stable fingerprint of the finding to answer (required, exact).
  --reason       Why the finding applies again (required, non-empty).
  --line-start   First evidence line, 1-based (required).
  --line-end     Last evidence line, 1-based; at most 20 lines per range (required).

The evidence path is taken from the finding itself, never from the caller.
The evidence is read from the audited Git object and must match the snapshot
exactly; the finding's line must sit inside the range. Only a cleared finding
(refuted or fixed) can be reopened; a still-blocking finding has nothing to
reopen. A missing or ambiguous fingerprint, an unsafe path, and a corrupt
dispositions log all fail closed without persisting anything. The answer is
appended to the separate dispositions log: persisted review revisions are
never mutated. A reopened finding blocks again under the shared rule.

Example:
  sentinel reopen --sha abc12345 --fingerprint 1f4902c8 --reason "the guard is bypassed on this path" --line-start 2 --line-end 2
`
	gateHelp = `Purpose: run deterministic validation followed by semantic review of HEAD.

Usage:
  sentinel gate --stage pre-commit|pre-push|pr [--profile X] [--timeout N]

Flags:
  --stage    Lifecycle stage invoking the gate (required): pre-commit, pre-push, or pr.
  --profile  Validation profile from validation.profiles (default standard). It selects validation configuration, never review configuration.
  --timeout  Per-call agent timeout override in seconds, for this invocation only. Same meaning as in review: it replaces review.timeout without editing the yml.

Example:
  sentinel gate --stage pre-commit
`
	statusHelp = `Purpose: guardian summary: worktree volume, review records, and recent events.

Usage:
  sentinel status [--json] [--prune]

Flags:
  --json   Emit machine-readable JSON instead of text.
  --prune  Delete review records for commits that no longer exist, then report.

Example:
  sentinel status --json
`
	metricsHelp = `Purpose: print deterministic local aggregates from the durable store.

Usage:
  sentinel metrics [--json]

Flags:
  --json   Emit machine-readable aggregates with stable units and null for unknown measurements.

The command reads only the local Git common directory. Corrupt or unreadable
evidence is reported as an error instead of being treated as an empty store.

Example:
  sentinel metrics --json
`
	doctorHelp = `Purpose: preflight the local environment a review depends on, so a missing tool fails fast here instead of surfacing as a review timeout.

Usage:
  sentinel doctor [--check-updates]

Flags:
  --check-updates   Compare the installed version against the latest published release. A network call, off by default.

Advisory only: reports and exits 0 like check. It never gates anything and
never installs — every warning prints the exact command to run instead.
Binaries resolve with exec.LookPath, never a shell probe. Each configured
agent must resolve and answer a minimal real prompt; the reviewer's search
binary, the codegraph binary, index, and six context gates, and the
pre-commit hook target are checked the same way. A condition whose prober did
not run renders as UNKNOWN with no remedy — never as a failure. A probe still
running when the 60s budget expires reports a timeout (the agent is slow or
wedged), never a provider failure.

Example:
  sentinel doctor
`
	explainHelp = `Purpose: explain the change profile, detected characteristics, risk, and cohesion of a commit range.

Usage:
  ` + explainUsageArgs + `

Flags:
  --json  Emit machine-readable JSON instead of text.

Example:
  sentinel explain HEAD~3..HEAD
`
	consentDiffHelp = `Purpose: manage the local per-user consent to expose diffs to external agents (required before slice can generate commit messages through an agent).

Usage:
  ` + consentDiffUsage + `

Actions:
  grant   Grant consent for this repository.
  revoke  Revoke consent for this repository.
  status  Show the current consent state.

Example:
  sentinel consent-diff grant
`
)

const (
	prHelp = `Purpose: pull-request operations.

Usage:
  sentinel pr create [...]
  sentinel pr review [...]

The legacy 'sentinel pr [gh arguments]' passthrough was removed: use 'sentinel pr create' instead.

Run 'sentinel help pr create' or 'sentinel help pr review' for their flags.
`
	prCreateHelp = `Purpose: analyze the branch, apply the blocking gate, and publish the pull request through gh with the honest verification template.

Usage:
  sentinel pr create [--base X] [--parent X] [--chain-pr] [--audit-pending] [--force --reason "..."]

Flags:
  --base            Comparison branch for the analysis; context/default base for stacked layers.
  --parent          Explicit stacked parent branch: only the own diff against it is reviewed, and the PR targets it.
  --chain-pr        Declare this branch as a stack layer and publish it even when oversized. Without --parent, the parent branch is resolved strictly and fails closed without a reliable signal.
  --audit-pending   Audit every branch commit without a review record instead of only reporting the gap (the net audit already covers the whole picture; this is the more expensive opt-in).
  --force           Override a red validation verdict; requires --reason.
  --reason          Explicit motive recorded alongside --force.

Example:
  sentinel pr create --base main
`
	prReviewHelp = `Purpose: dry-run analysis of the unpublished branch: audit matrix, summary, and the single-versus-chained PR decision. Publishes nothing.

Usage:
  sentinel pr review [--base X] [--parent X] [--audit-pending] [--overview] [--json]

Flags:
  --base            Comparison branch (default main).
  --parent          Explicit stacked parent branch: reviews only the own diff against it; inherited findings render separately (non-blocking).
  --audit-pending   Audit every branch commit without a review record instead of only reporting the gap (the net audit already covers the whole picture; this is the more expensive opt-in).
  --overview        Include the PR overview in the analysis.
  --json            Emit machine-readable JSON.

Example:
  sentinel pr review --base main --json
`
)

const (
	runsStartHelp = `Purpose: start a durable run from an operator prompt executed by the configured agent chain.

Usage:
  ` + runsStartUsage + ` [--json]

Flags:
  --prompt     Prompt text for the agent (required).
  --policy-id  Policy identifier (default operator).
  --json       Emit machine-readable JSON.

Example:
  sentinel runs start --prompt "refactor the parser" --json
`
	runsStatusHelp = `Purpose: show durable-run state summaries.

Usage:
  ` + runsStatusUsage + `

Flags:
  --run   Restrict the report to one run.
  --json  Emit machine-readable JSON.

Example:
  sentinel runs status --run r1 --json
`
	runsRespondHelp = `Purpose: answer a pending decision of one awaiting run.

Usage:
  ` + runsRespondUsage + ` [--json]

Flags:
  --run   Run awaiting the decision (required).
  --text  Response text delivered to the run (required).
  --json  Emit machine-readable JSON.

Example:
  sentinel runs respond --run r1 --text "use option B"
`
	runsAbortHelp = `Purpose: request cancellation of one active run.

Usage:
  ` + runsAbortUsage + ` [--json]

Flags:
  --run   Run to cancel (required).
  --json  Emit machine-readable JSON.

Example:
  sentinel runs abort --run r1
`
	runsRetryHelp = `Purpose: retry a failed run under optimistic concurrency control.

Usage:
  ` + runsRetryUsage + ` [--json]

Flags:
  --run                Failed run to retry (required).
  --expected-revision  Refuse unless the stream head matches this revision.
  --json               Emit machine-readable JSON.

Example:
  sentinel runs retry --run r1 --expected-revision 4
`
	runsRecoverHelp = `Purpose: inspect recovery classes and repair terminal-but-unprojected runs.

Usage:
  sentinel runs recover                     Read-only scan of every non-terminal run; exit 4 when an entry needs an operator decision.
  sentinel runs recover --run <id> [--expected-revision N]
                                            Recovery context for one run.
  ` + runsRecoverRepairUsage + `
                                            Rebuild the lagging snapshot by replaying the whole verified stream under one lock.

Flags:
  --run                Target run.
  --expected-revision  Refuse stale repairs unless the stream head matches.
  --repair             Identity of the run to repair.
  --json               Emit machine-readable JSON.

--expected-revision is rejected on the scan and cannot combine with --repair.
`
	runsVerifyHelp = `Purpose: verify the integrity of one run's event stream.

Usage:
  ` + runsVerifyUsage + ` [--json]

Flags:
  --run   Run to verify (required).
  --json  Emit machine-readable JSON.

Example:
  sentinel runs verify --run r1
`
	runsPruneHelp = `Purpose: explicit operator maintenance: remove ONLY terminal execution records older than the cutoff that no review provenance references. Nothing purges automatically.

Usage:
  ` + runsPruneUsage + `

Flags:
  --older-than  Duration cutoff for the last event of candidate records (required).
  --json        Emit machine-readable JSON.

Example:
  sentinel runs prune --older-than 720h --json
`
	runsAttachHelp = `Purpose: observe one durable run live: list attachable candidates, print a point-in-time snapshot, or follow a run with the terminal UI.

Usage:
  ` + runsAttachUsage + `

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
	runsDaemonHelp = `Purpose: manage the repository-local foreground daemon.

Usage:
  ` + runsDaemonUsage + `

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
	tuiHelp = `Purpose: open the full-screen control center over the repository registry snapshot, refreshed live while the session is open.

Usage:
  sentinel tui

Flags:
  none. Any extra argument or flag is rejected with exit 1.

Notes:
  When no daemon is live for the current repository, the control center starts one detached and owns it for the session, stopping it gracefully on exit. A daemon already owned by another session is left untouched. Startup or readiness failures exit 5 before the interface opens; stop failures print one error line but keep exit success.

Example:
  sentinel tui
`
)

// runsLogsHelp interpolates fmt.Sprint(runsLogsDefaultLimit), which is
// not a constant expression, so this one entry is a var. The emitted bytes
// match the former inline literal exactly.
var runsLogsHelp = `Purpose: read the verified event stream of one run with cursor pagination.

Usage:
  ` + runsLogsUsage + ` [--json]

Flags:
  --run    Run whose events are read (required).
  --after  Read events after this cursor.
  --limit  Maximum number of events (default ` + fmt.Sprint(runsLogsDefaultLimit) + `).
  --json   Emit machine-readable JSON.

Example:
  sentinel runs logs --run r1 --after 7 --limit 20 --json
`
