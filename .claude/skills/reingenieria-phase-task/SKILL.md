---
name: reingenieria-phase-task
description: "Trigger: phase F0-F9, task TN.M, reingenieria, Codex delegation, sentinel review, sentinel gate. Implement and verify one phase task independently of its implementer."
license: Apache-2.0
metadata:
  author: iseoane
  version: "1.3"
---
## Activation Contract

Load when implementing or verifying a task from `docs/reingenieria/f{N}-*.md` (id `T{N}.{M}`), in Claude Code or opencode. Applies whether the calling agent implements the task or verifies another agent's (e.g. Codex's) implementation of it.

## Hard Rules

- When delegating implementation, instruct the delegate to build and test only, never to self-review. The calling agent always runs the semantic review itself — never trust the implementer's own verdict on its own diff. Let the delegating agent choose the model and reasoning effort best suited to the task unless the user explicitly selects them. Record the effective agent identity, model, and reasoning effort from execution evidence; never hard-code a provider, model, or effort.
- Give every delegated agent its own dedicated Git worktree. An agent may read and write only inside its assigned worktree. Run job-status and durable-run commands from that same worktree so repository and workspace identities remain aligned. Never delegate into a shared worktree.
- Never treat a "completed" notification as proof. For Sentinel-managed execution, retain the returned run ID, require a settled state from `sentinel runs status --run <id>`, and validate its evidence with `sentinel runs verify --run <id>`. Use `runs logs` or `runs attach` for observation and `runs recover` only according to its reported recovery class. For an external harness, use its authoritative job status; never infer completion from process listings.
- Let `sentinel review` determine the applicable review scope and dimensions from the candidate it inspects. Do not require a separate review of a presumed backend or logic commit.
- **Sentinel produces the semantic verdict; the calling agent never does.** Do not skip the review because the change looks small, obvious, or is your own work. Do not decide in advance which findings will count, and do not report a task as reviewed on the strength of your own reading of the diff. The only judgment you own over a finding is verifying its premise in the code, and then fixing it or accepting it under a Decision Gate with a written reason. A commit without a review record has not been reviewed, whatever its diff looks like.
- Before accepting or reverting a finding, verify its premise in the code (grep the real call sites, check where the data actually originates) — do not act on a suggestion by faith alone.
- Before planning or applying a slice, confirm that every changed path belongs to this task and that no other agent is modifying the assigned worktree. Never stash, reset, stage, restore, or otherwise move unrelated changes you do not own. Another agent's in-flight work is a blocker: stop and report it. Pre-existing changes that simply are not yours are not a blocker, but `slice plan` would sweep them into your commits, so commit only this task's paths with an explicit `git add <paths>` plus `sentinel check --staged`, and say so in the report.
- Never rewrite commit history to fix a cosmetic mismatch (e.g. a generic slice message on a real fix commit) without the user's explicit choice. When rewording/squashing via `git reset --soft <target>`, print `git log --oneline` first and confirm `<target>` is the exact intended ancestor — resetting one commit too far silently folds extra history into the rewrite.
- Never launch a long-running sentinel command with a bare shell `&` and pipe its output away (e.g. into `tail` inside the backgrounded job) — that output is unrecoverable. Run it in the foreground when you must wait for it, or use the harness's own background/notification mechanism, which preserves the full output for later reading. Never blindly re-run an already-completed command just to recover output you failed to capture.
- Never pipe a guardian/staged-volume check through another command before a `&&` chain (`check | tail && commit` masks the exit code). Run the check alone, read its verdict, then act.

## Command Surface (post-R11)

- Every command and subcommand answers `-h`/`--help` with dedicated English help, exit 0, empty stderr — prefer it over guessing flags: `sentinel gate --help`, `sentinel runs recover --help`, `sentinel pr create -h`.
- `help <command>` prints the same text as `<command> --help`; unknown topics exit 1.
- Flags are strict per subcommand: an undeclared flag exits 1 with usage (e.g. `--older-than` outside `runs prune`, `--repair` outside `runs recover`). Check the command's `--help` instead of assuming shared flags.
- Durable runs are the single execution authority (R0–R11 complete): review and gate route through admitted, evidence-verified controller runs. The old `review.durable_runs` / `gate.durable_runs` yaml keys no longer exist — yamls carrying them fail config load with an unknown-key error.
- Operational surface: `sentinel runs status|logs|verify|recover|start|respond|abort|retry|prune`. Recovery classes come from `runs recover` (read-only scan); repair is `runs recover --repair <id>`; prune is explicit and never automatic.
- Observation surface: `sentinel runs attach [--run <id>] [--follow]` uses daemon-preferred routing with in-process fallback. Agent execution may use configured CLI adapters or `kind: acpx`; trust recorded effective identity and enforcement evidence, not provider assumptions.
- Historical readability is guaranteed: pre-R11 ledgers and run streams remain fully inspectable without conversion.

## Review Mechanics

**Reading a verdict.** `sentinel review` prints one verdict per dimension, not the findings. Exit `0` for `ok`/`warn`, `1` when any dimension blocks, `4` when the review could not run. `unavailable` is never a pass: that dimension never executed and its coverage is zero, so treat it as missing evidence and re-run it, not as a clean result.

**Reading the findings.** They are not in `sentinel status`, which shows verdicts only. They live in `<git-dir>/vas-sentinel/<sha>.json` under `revisions[-1].dims[].findings[]`. In a linked worktree `<git-dir>` is `.git/worktrees/<name>/`, **not** the common dir, so a review run from a worktree writes its ledger there.

**Choosing the candidate.** Plain `sentinel review` audits `HEAD` alone. A slice that produced several commits therefore leaves the rest unaudited: name every commit without a record, `sentinel review <sha> [<sha>...]`. Avoid `--all`, which audits every recordless commit up to the target and on a real history means far more than this task. Accept the dimensions Sentinel selects — reach for `--dims` only to re-run one that came back `unavailable`, never to narrow a candidate's coverage.

**Clearing a block.** A blocked ficha is marked `FixedIn` by `registrarCorrecciones` when a later commit satisfies all three conditions: it is reviewed with exit `0`, its message starts with `fix(`, and it touches at least one file named in that ficha's findings. `slice` names commits through the agent, so a batch that fixes findings can come back as `test(...)` or `chore(...)` and will never clear the block. Read the planned message in `plan.json` before applying, and commit that batch manually when it will not start with `fix(`.

**Budget.** `--timeout <seconds>` replaces `review.timeout` for one invocation, on both `review` and `gate`. A large candidate needs it, and so does a reviewer whose search tools are unavailable: it falls back to reading whole files and exhausts the default 600s. When a dimension dies on the budget, the cause is at the front of its `reason`.

## Decision Gates


| Situation                                      | Action                                                              |
| ---------------------------------------------- | ------------------------------------------------------------------- |
| Delegate's auth expired mid-task               | Ask the user to re-authenticate, re-check readiness, retry          |
| Review verdict is `block`/CRITICAL             | Fix, commit, and re-review that fix before slicing further          |
| Verdict is `warn` and the premise is disproven | Document why it is inert; do not revert a correct fix to silence it |
| A test asserts the old, wrong premise          | Update the test's fixture/assertion to the real invariant           |
| Verdict is `warn`, only ADVISORY findings remain, and the code has no production consumer yet | Stop iterating; accept the remaining findings with a documented reason instead of chasing another fix round |
| Gate/review reports an `admission:` failure    | Treat it as evidence corruption or stale snapshot: inspect via `sentinel runs status --run <id>`, never retry blindly |


## Execution Steps

1. Read the version from `release.yml`, rebuild with the repository build script when source changes require a fresh CLI, and use `bin/&lt;version&gt;/sentinel`. Record the exact binary path. Account for the build script's source-mutating `gofmt -w .` step before review or candidate freezing.
2. Read the task definition in `docs/reingenieria/f{N}-*.md` and create an execution plan.
3. Delegate (per Hard Rules) to the implementation agent, model, and reasoning effort best suited to the task.
4. Independently confirm the implementer's execution reached a settled state and verify its durable evidence before reading its diff.
5. Verify with `go build ./...`, `go vet ./...`, and `go test -count=1 ./...`. Run `go test -count=1 -race &lt;explicit touched package list&gt;` and report that list. Run `sentinel check` as the advisory whole-worktree measurement.
6. Read the real diff before slicing.
7. Confirm the worktree contains only this task. Run `sentinel slice plan --json &gt; plan.json`. If no decisions are pending, write `answers.json` as `{"plan_id":"&lt;plan_id&gt;","respuestas":{}}`. If `decisiones_pendientes[]` is non-empty, present every decision verbatim, wait for the user, and record only the user's literal `bypass` or `abortar` answer. Run `sentinel slice apply --plan plan.json --answers answers.json`. Keep both transport files out of commits and remove them only when this agent created them.
8. Review every commit the slice produced, naming those without a record (see Review Mechanics), and accept the dimensions Sentinel determines. Capture every emitted root and child run ID, require terminal status, and run `sentinel runs verify --run &lt;id&gt;` for every run used as acceptance evidence. Re-run any `unavailable` dimension with a wider `--timeout` before treating the candidate as reviewed.
9. Per finding: verify the premise, apply the fix, and commit it under a `fix(` message so the blocked ficha can be marked corrected. Re-review the fix commit; a correction is only accepted once Sentinel says so, never on your own reading. Repeat until no block remains or the remaining findings are accepted under a Decision Gate with a written reason.
10. Run `sentinel gate --stage pre-push` for the final evidence. Capture every emitted root and child run ID, require terminal status, and verify each accepted run with `sentinel runs verify --run &lt;id&gt;`. Preserve `admission:` failures as evidence blockers and inspect their status/logs without blind retries.
11. After the final gate, review any remaining warnings, accepted findings, or deferred points. Check the later phase fichas to determine whether each is already planned; otherwise, assess whether it must be recorded as a follow-up with its target task and reason — record deferred pools in `docs/reingenieria/f0-deuda.md`.
12. Mark the task complete; report commits, findings, follow-ups, and the gate result.

When a manual commit is part of an explicitly authorized workflow, run `sentinel check --staged` against the exact staged candidate immediately before committing. This is the enforcing 400-line boundary; `sentinel check` remains advisory.

Write every new or modified artifact, commit message, finding disposition, and follow-up entry in English. Preserve legacy Spanish content unless translation is explicitly in scope. Use Conventional Commits in English and require `commit_language: en`.

## Output Contract

Report: task id; dedicated worktree path; commits created; exact Sentinel binary; effective agent identity; durable root and child run IDs; terminal states and `runs verify` results; admission or recovery blockers; implementer-verification evidence; exact build, test, and race commands with package scopes; findings with resolution (fixed / accepted-with-reason); final gate result; follow-ups with their planned target or recorded reason; and every unrelated file observed. State explicitly that unrelated files were not staged, stashed, modified, restored, or committed.

## References

- `docs/reingenieria/` — phase fichas (`F0`-`F9`, task ids `T{N}.{M}`); `f0-deuda.md` also hosts the deferred follow-up pool.
- `AGENTS.md` — the `slice plan`/`slice apply` flow and the volume guardian rule.
- `docs/runs-cli.md` — durable-run operator surface: stable JSON shapes, recovery classes, retention/migration policy.
- Every command's `-h/--help` — authoritative, drift-free flag documentation.
