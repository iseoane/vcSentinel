package main

// Long English help texts referenced by the commandHelpTexts registry in
// help_commands.go. Keeping them here lets the registry stay a compact
// key-to-constant map.

import "fmt"

// Usage lines shared between runtime error paths and --help output so the two
// can never drift apart. Call sites keep their exact historical bytes by
// prefixing these constants.
const (
	explainUsageArgs = "vcsentinel explain [<base>..<head>] [--json]"
	explainUsage     = "usage: " + explainUsageArgs

	sliceApplyUsage  = "Usage: vcsentinel slice apply --plan <plan.json> --answers <answers.json>"
	consentDiffUsage = "Usage: vcsentinel consent-diff grant|revoke|status"

	runsStartUsage         = "Usage: vcsentinel runs start --prompt <text> [--policy-id <id>]"
	runsStatusUsage        = "Usage: vcsentinel runs status [--run <id>] [--json]"
	runsLogsUsage          = "Usage: vcsentinel runs logs --run <id> [--after <activity-number>] [--limit N]"
	runsRespondUsage       = "Usage: vcsentinel runs respond --run <id> --text <answer>"
	runsAbortUsage         = "Usage: vcsentinel runs abort --run <id>"
	runsRetryUsage         = "Usage: vcsentinel runs retry --run <id> [--expected-revision N]"
	runsRecoverRepairUsage = "Usage: vcsentinel runs recover --repair <id>"
	runsVerifyUsage        = "Usage: vcsentinel runs verify --run <id>"
	runsPruneUsage         = "Usage: vcsentinel runs prune --older-than <duration> [--json]"
	runsAttachUsage        = "Usage: vcsentinel runs attach [--run <id>] [--after <activity-number>] [--follow]"
	runsDaemonUsage        = "Usage: vcsentinel runs daemon start|status|stop"
)

const (
	checkHelp = `Purpose: measure added authored code in this repository. A normal check is advisory; --staged enforces the 400-line review limit for the pending commit.

Usage:
  vcsentinel check [--json] [--staged]

Flags:
  --json     Emit the machine-readable report instead of text.
  --staged   Measure only staged changes; exit 1 when authored code exceeds 400 lines.

Examples:
  vcsentinel check
  vcsentinel check --staged
`
	sliceHelp = `Purpose: group pending changes into reviewable commits of at most 400 authored lines.

Usage:
  vcsentinel slice                Interactive flow (requires stdin).
  vcsentinel slice plan [--json] [--intent TEXT]
                                Propose selections without committing; exit 3 when a user decision is pending.
  vcsentinel slice apply --plan <plan.json> --answers <answers.json>
                                Apply an approved plan; commit only after every required decision is answered.

The plan/apply flow is the non-interactive alternative to the guided flow.

Example:
  vcsentinel slice plan --json > plan.json
`
	slicePlanHelp = `Purpose: propose reviewable selections without committing anything. Exit 3 when a user decision is pending.

Usage:
  vcsentinel slice plan [--json] [--intent TEXT]

Flags:
  --json       Emit the plan as machine-readable JSON.
  --intent     Briefly state what the changes are for; the text helps describe
               the proposed groupings and is optional.

Example:
  vcsentinel slice plan --intent "separate the parser fix" --json > plan.json
`
	sliceApplyHelp = `Purpose: apply an approved plan. It commits only when every pending decision has an explicit user answer.

Usage:
  ` + sliceApplyUsage + `

Flags:
  --plan     Path to the plan produced by 'slice plan' (required).
  --answers  Path to the JSON answers keyed by decision id (required).

Example:
  vcsentinel slice apply --plan plan.json --answers answers.json
`
)

const (
	reviewHelp = `Purpose: review one or more commits against the selected checks and save the result. The default target is HEAD.

Usage:
  vcsentinel review [<sha>|<expr> ...] [--dims a,b] [--profile X] [--answer "..."] [--timeout N] [--all] [--chain] [--gate] [--json] [--prune]

Flags:
  --dims     Comma-separated checks to run (logic, style, design, tests, security, spec).
  --profile  Use a named review profile instead of the configured default.
  --answer   Text used to answer a question raised during the review.
  --timeout  Per-agent time limit for this invocation, in seconds.
  --all      Review each commit up to the target that has not been reviewed.
  --chain    Review the branch or range through the target.
  --gate     Exit 1 when the review has a critical finding.
  --json     Emit machine-readable output.
  --prune    Remove saved review records for commits that no longer exist.

Example:
  vcsentinel review HEAD --dims logic,tests --gate
`
	refuteHelp = `Purpose: record evidence that one reviewed finding is not valid.

Usage:
  vcsentinel refute --sha SHA --fingerprint FP --reason TEXT --line-start N --line-end M

Flags:
  --sha          Reviewed commit SHA holding the finding (required, exact).
  --fingerprint  Stable fingerprint of the finding to answer (required, exact).
  --reason       Why the finding's premise is false (required, non-empty).
  --line-start   First evidence line, 1-based (required).
  --line-end     Last evidence line, 1-based; at most 20 lines per range (required).

The evidence file and lines come from the reviewed commit. vcSentinel checks
them before saving your answer. An invalid or ambiguous fingerprint, an unsafe
path, or unreadable answer history fails without saving anything. A refutation
clears only the matching finding's block; accepting a finding does not clear it,
and reopening a cleared finding blocks it again.

Example:
  vcsentinel refute --sha abc12345 --fingerprint 1f4902c8 --reason "the committed implementation is safe" --line-start 2 --line-end 2
`
	acceptHelp = `Purpose: record that a person accepts the risk described by one reviewed finding.

Usage:
  vcsentinel accept --sha SHA --fingerprint FP --reason TEXT

Flags:
  --sha          Reviewed commit SHA holding the finding (required, exact).
  --fingerprint  Stable fingerprint of the finding to answer (required, exact).
  --reason       Why the risk is acknowledged (required, non-empty).

Acceptance documents a decision but does not clear the finding's block. It can
answer only a finding that has not already been answered. An invalid or
ambiguous fingerprint, an unsafe path, or unreadable answer history fails
without saving anything.

Example:
  vcsentinel accept --sha abc12345 --fingerprint 1f4902c8 --reason "residual risk acknowledged for this release"
`
	reopenHelp = `Purpose: record that one previously cleared finding applies again.

Usage:
  vcsentinel reopen --sha SHA --fingerprint FP --reason TEXT --line-start N --line-end M

Flags:
  --sha          Reviewed commit SHA holding the finding (required, exact).
  --fingerprint  Stable fingerprint of the finding to answer (required, exact).
  --reason       Why the finding applies again (required, non-empty).
  --line-start   First evidence line, 1-based (required).
  --line-end     Last evidence line, 1-based; at most 20 lines per range (required).

The evidence file and lines come from the reviewed commit. Only a refuted or
fixed finding can be reopened; a finding that still blocks has nothing to
reopen. vcSentinel checks the evidence before saving your answer. Invalid or
ambiguous input, an unsafe path, or unreadable answer history fails without
saving anything.

Example:
  vcsentinel reopen --sha abc12345 --fingerprint 1f4902c8 --reason "the guard is bypassed on this path" --line-start 2 --line-end 2
`
	gateHelp = `Purpose: run the configured validation checks and report whether they pass.

The gate answers "does this change pass the repository's required checks right
now?" It runs the selected validation profile without an agent. It does not
review code quality; use ` + "`vcsentinel review`" + ` for that.

Usage:
  vcsentinel gate --stage pre-commit|pre-push|pr [--profile X]

Flags:
  --stage    Where the check is being run (required): pre-commit, pre-push, or pr.
  --profile  Validation profile from validation.profiles (default standard).

Exit codes:
  0  PASS: every configured command passed.
  1  VALIDATION_FAILED: a command reported a failure, with its output as evidence.
  4  INFRASTRUCTURE_ERROR: configuration, HEAD, planning, storage, or execution failed.

Example:
  vcsentinel gate --stage pre-commit
`
	statusHelp = `Purpose: show repository volume, saved review records, and recent command activity.

Usage:
  vcsentinel status [--json] [--prune]

Flags:
  --json   Emit machine-readable JSON instead of text.
  --prune  Remove review records for commits that no longer exist, then report.

Example:
  vcsentinel status --json
`
	metricsHelp = `Purpose: show local measurements about reviews, fixes, and tracked agent tasks.

Usage:
  vcsentinel metrics [--json]

Flags:
  --json   Emit machine-readable measurements. Unknown values remain null.

The command reads local vcSentinel records only. Corrupt or unreadable records
are reported as errors instead of being treated as an empty result.

Example:
  vcsentinel metrics --json
`
	doctorHelp = `Purpose: check whether the local tools and configured agents needed for review are ready.

Usage:
  vcsentinel doctor [--check-updates]

Flags:
  --check-updates   Compare the installed version with the latest published release. Off by default because it uses the network.

This is an advisory check: it reports warnings and exits 0, and it never
installs anything or blocks a command. It checks configured agents, search and
code-analysis tools, the local index, and the repository pre-commit check. Each
configured agent receives a minimal real prompt. If a check cannot run, it is
shown as UNKNOWN rather than reported as a failure. Warnings include the
command to run when one is available.

Example:
  vcsentinel doctor
`
	explainHelp = `Purpose: describe the size, changed areas, detected characteristics, risks, and suggested split for a commit range.

Usage:
  ` + explainUsageArgs + `

Flags:
  --json  Emit machine-readable JSON instead of text.

Example:
  vcsentinel explain HEAD~3..HEAD
`
	consentDiffHelp = `Purpose: manage permission to share small diffs with configured agents when vcSentinel generates commit messages.

Usage:
  ` + consentDiffUsage + `

Actions:
  grant   Grant permission for this repository.
  revoke  Revoke permission for this repository.
  status  Show the current permission state.

Example:
  vcsentinel consent-diff grant
`
)

const (
	prHelp = `Purpose: prepare and publish a pull request.

Usage:
  vcsentinel pr review [...]
  vcsentinel pr create [...]

pr review analyzes the current branch and saves its result and evidence locally;
it does not publish anything. pr create publishes only after it finds a
previously saved pr review result for this branch and commit. pr create does
not review the branch itself.

Run 'vcsentinel help pr create' or 'vcsentinel help pr review' for their flags.
`
	prCreateHelp = `Purpose: publish a pull request using a previously saved pr review result.

pr create does not review the branch. It requires a pr review result saved for
the current branch and commit, verifies the required checks, and then publishes
the saved title and body through GitHub. If the saved review or its evidence is
missing, it stops instead of reviewing on your behalf.

Usage:
  vcsentinel pr create [--base X] [--parent X] [--chain-pr] [--force --reason "..."]

Flags:
  --base       Comparison branch and default target for the pull request.
  --parent     Explicit parent branch for a stacked pull request; only this branch's own changes are considered.
  --chain-pr   Mark this branch as a stack layer and allow publication when it is larger than the normal limit. Without --parent, the parent must be resolved safely.
  --force      Override a failed validation result; requires --reason.
  --reason     Explanation recorded with --force.

Example:
  vcsentinel pr create --base main
`
	prReviewHelp = `Purpose: review the current branch and save its result and evidence without publishing a pull request.

This command does not publish. It reports commits that still need individual
review with 'vcsentinel review'.

Usage:
  vcsentinel pr review [--base X] [--parent X] [--overview] [--json]

Flags:
  --base       Comparison branch (default main).
  --parent     Explicit parent branch for a stacked pull request; inherited findings are shown separately and do not block this branch.
  --overview   Include the pull-request overview in the analysis.
  --json       Emit machine-readable JSON.

Example:
  vcsentinel pr review --base main --json
`
)

const runsHelp = `Purpose: manage tracked agent tasks that can be started, monitored, answered, stopped, retried, repaired, verified, followed, or cleaned up.

Usage:
  vcsentinel runs <subcommand> [flags]

Subcommands:
  start    Start a task from a prompt: --prompt <text> [--policy-id <id>] [--json]
  status   Show one task or all tasks: [--run <id>] [--json]
  logs     Read recorded activity: --run <id> [--after <activity-number>] [--limit N] [--json]
  respond  Send an answer to a waiting task: --run <id> --text <answer> [--json]
  abort    Ask a task to stop: --run <id> [--orphaned --reason "..."] [--json]
  retry    Retry a failed task: --run <id> [--expected-revision N] [--json]
  recover  Find or repair tasks needing recovery: --run <id> [--expected-revision N] [--json]
           or --repair <id> [--json]
  verify   Check one task's recorded history: --run <id> [--json]
  attach   List, snapshot, or follow a task: [--run <id>] [--after <activity-number>] [--follow]
  daemon   Manage the repository service: start|status|stop
  prune    Remove old completed task records: --older-than <duration> [--json]

Exit codes:
  0  Success, including a valid no-op or clean stop.
  1  Usage error: missing or unsupported command or flag.
  2  The requested task was not found.
  3  An expected revision did not match the saved record.
  4  The requested action is not valid for the task's current state.
  5  Storage, agent, service, or other infrastructure failure.

Only the flags listed for a subcommand are accepted. Pruning is explicit and
never automatic. See docs/design/runs-cli.md for JSON output and state details.
`

const (
	runsStartHelp = `Purpose: start a tracked agent task from a prompt.

Usage:
  ` + runsStartUsage + ` [--json]

Flags:
  --prompt     Prompt for the configured agent (required).
  --policy-id  Policy name to use (default operator).
  --json       Emit machine-readable JSON.

The command returns the task, job, and invocation identifiers so you can
inspect or control it with the other runs commands.

Example:
  vcsentinel runs start --prompt "refactor the parser" --json
`
	runsStatusHelp = `Purpose: show the current state of tracked tasks.

Usage:
  ` + runsStatusUsage + `

Flags:
  --run   Show only this task.
  --json  Emit machine-readable JSON.

Without --run, the command lists all tasks known to this repository.

Example:
  vcsentinel runs status --run r1 --json
`
	runsRespondHelp = `Purpose: send an answer to a task that is waiting for a decision.

Usage:
  ` + runsRespondUsage + ` [--json]

Flags:
  --run   Task waiting for the answer (required).
  --text  Answer text to send (required).
  --json  Emit machine-readable JSON.

Example:
  vcsentinel runs respond --run r1 --text "use option B"
`
	runsAbortHelp = `Purpose: ask a running task to stop.

Usage:
  ` + runsAbortUsage + ` [--json]

Flags:
  --run        Task to stop (required).
  --orphaned   Retire a task whose saved record is unfinished but whose owner is gone; requires --reason.
  --reason     Explain the --orphaned decision.
  --json       Emit machine-readable JSON.

Without --orphaned, an unreachable task is not silently marked as stopped.

Example:
  vcsentinel runs abort --run r1
`
	runsRetryHelp = `Purpose: retry a task that failed.

Usage:
  ` + runsRetryUsage + ` [--json]

Flags:
  --run                Failed task to retry (required).
  --expected-revision  Retry only if the saved record still has this revision.
  --json               Emit machine-readable JSON.

Use --expected-revision when you want the command to stop rather than act on a
record that changed after you inspected it.

Example:
  vcsentinel runs retry --run r1 --expected-revision 4
`
	runsRecoverHelp = `Purpose: find tasks whose saved state needs attention, or repair one task when the command proves that it finished but its summary was not updated.

Usage:
  vcsentinel runs recover [--run <id>] [--expected-revision N] [--json]
  ` + runsRecoverRepairUsage + ` [--json]

Modes:
  without --run or --repair  Read-only scan of unfinished tasks; exit 4 when a task needs a human decision.
  --run <id>                 Show recovery information for one task.
  --repair <id>              Rebuild the saved summary only for a task proven safe to repair.

Flags:
  --run                Task to inspect.
  --expected-revision  Proceed only if the saved record still has this revision.
  --repair             Task to repair.
  --json               Emit machine-readable JSON.

--expected-revision is rejected on the full scan and cannot be combined with
--repair.
`
	runsVerifyHelp = `Purpose: check that one task's saved activity records are complete and consistent.

Usage:
  ` + runsVerifyUsage + ` [--json]

Flags:
  --run   Task to verify (required).
  --json  Emit machine-readable JSON.

Example:
  vcsentinel runs verify --run r1
`
	runsPruneHelp = `Purpose: explicitly remove old completed task records that are no longer referenced by review history.

Usage:
  ` + runsPruneUsage + `

Flags:
  --older-than  Keep only records whose last activity is newer than this duration (required).
  --json        Emit machine-readable JSON.

Unfinished, corrupt, orphaned, referenced, or still-needed records are kept.
Nothing is removed automatically.

Example:
  vcsentinel runs prune --older-than 720h --json
`
	runsAttachHelp = `Purpose: watch one tracked task, list tasks that can be watched, or follow a task until it finishes.

Usage:
  ` + runsAttachUsage + `

Modes:
  without --run        List every unfinished task that can be watched; the
                       command exits 0 even when the list is empty.
  --run <id>           Print one plain-text snapshot, including activity after
                       --after (default 0). It returns immediately for both
                       finished and unfinished tasks.
  --run <id> --follow  Open the live view and refresh it from the last activity
                       number until the task finishes or you detach.

Flags:
  --run    Task to observe.
  --after  Include activity strictly after this activity number.
  --follow Keep refreshing; requires --run.

Exit contract:
  0  clean quit in every mode. A followed task freezes when it finishes, and
     SIGINT/SIGTERM detach as cleanly as pressing q.
  2  task not found.
  4  the requested action is not valid for the task's current state.
  5  storage or other infrastructure failure.

Follow-mode keys:
  q quit · r refresh · a abort · e respond · y retry (when retryable)
`
	runsDaemonHelp = `Purpose: manage the repository-local background service used by tracked tasks.

Usage:
  ` + runsDaemonUsage + `

Subcommands:
  start   Claim this repository, settle tasks that can be recovered safely,
          serve local requests, and wait for a stop signal.
  status  Show the service owner and connection details.
  stop    Ask the service to shut down cleanly and print a summary of
          unfinished tasks whose owner disappeared.

Flags:
  none. These three subcommands take no flags; extra arguments are usage
  errors.

Exit codes:
  start   0 on a clean stop; 4 when another service already owns the
          repository; 5 for another failure.
  status  0 while a service is answering; 2 when none is running; 5 when the
          saved service record is unreadable or incomplete.
  stop    0 after a clean shutdown; 2 when no reachable service is running;
          5 for a corrupt record or a shutdown failure.
`
	tuiHelp = `Purpose: open the full-screen interactive dashboard for registered repositories and tracked tasks.

Usage:
  vcsentinel tui

Flags:
  none. Any extra argument or flag is rejected with exit 1.

Notes:
  If this repository has no running service, the interactive dashboard starts one in
the background and owns it for the session, stopping it cleanly on exit. A
service owned by another session is left untouched. Startup or readiness
failures exit 5 before the interface opens; a stop failure prints an error but
keeps a successful exit.

Example:
  vcsentinel tui
`
)

// runsLogsHelp interpolates fmt.Sprint(runsLogsDefaultLimit), which is
// not a constant expression, so this one entry is a var.
var runsLogsHelp = `Purpose: read the recorded activity of one task in pages.

Usage:
  ` + runsLogsUsage + ` [--json]

Flags:
  --run    Task whose activity is read (required).
  --after  Read records after this activity number.
  --limit  Maximum number of records (default ` + fmt.Sprint(runsLogsDefaultLimit) + `).
  --json   Emit machine-readable JSON.

Example:
  vcsentinel runs logs --run r1 --after 7 --limit 20 --json
`
