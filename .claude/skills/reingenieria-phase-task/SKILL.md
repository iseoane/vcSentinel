---
name: reingenieria-phase-task
description: "Trigger: phase F0-F9, task TN.M, reingenieria, Codex delegation, sentinel review, sentinel gate. Implement and verify one phase task independently of its implementer."
license: Apache-2.0
metadata:
  author: iseoane
  version: "1.1"
---
## Activation Contract

Load when implementing or verifying a task from `docs/reingenieria/f{N}-*.md` (id `T{N}.{M}`), in Claude Code or opencode. Applies whether the calling agent implements the task or verifies another agent's (e.g. Codex's) implementation of it.

## Hard Rules

- When delegating implementation, instruct the delegate to build and test only, never to self-review. The calling agent always runs the semantic review itself — never trust the implementer's own verdict on its own diff. Match the delegate to the environment: in opencode, delegate to its configured user agent (GPT-5.6 Terra, high reasoning effort); in Claude Code, delegate to your own subagent via the Agent tool (model: sonnet — the tool has no explicit reasoning-effort parameter, so pass none and accept the harness default).
- Never treat a "completed" notification as proof. Confirm the real process ended (e.g. `ps -ef | grep <tool>`, the job's own status command) before reading its diff.
- `sentinel gate` only reviews `HEAD`, and dimension depth depends on the changed layer (a test-only `HEAD` gets a shallow spec/tests-only pass). Always also run `sentinel review <sha>` with full dimensions on the backend/logic commit itself.
- Before accepting or reverting a finding, verify its premise in the code (grep the real call sites, check where the data actually originates) — do not act on a suggestion by faith alone.
- Stash unrelated pending changes (e.g. an auto-migrated `.vas_sentinel/vassentinel.yml`) before `sentinel slice`, and restore them after.
- Do not use worktree isolation when delegating to Codex: its jobs are tracked per `workspaceRoot`, so a status check from the main repo won't see them.
- Never rewrite commit history to fix a cosmetic mismatch (e.g. a generic slice message on a real fix commit) without the user's explicit choice. When rewording/squashing via `git reset --soft <target>`, print `git log --oneline` first and confirm `<target>` is the exact intended ancestor — resetting one commit too far silently folds extra history into the rewrite.
- Never launch a long-running sentinel command with a bare shell `&` and pipe its output away (e.g. into `tail` inside the backgrounded job) — that output is unrecoverable. Run it in the foreground when you must wait for it, or use the harness's own background/notification mechanism, which preserves the full output for later reading. Never blindly re-run an already-completed command just to recover output you failed to capture.
- Never pipe a guardian/staged-volume check through another command before a `&&` chain (`check | tail && commit` masks the exit code). Run the check alone, read its verdict, then act.

## Command Surface (post-R11)

- Every command and subcommand answers `-h`/`--help` with dedicated English help, exit 0, empty stderr — prefer it over guessing flags: `sentinel gate --help`, `sentinel runs recover --help`, `sentinel pr create -h`.
- `help <command>` prints the same text as `<command> --help`; unknown topics exit 1.
- Flags are strict per subcommand: an undeclared flag exits 1 with usage (e.g. `--older-than` outside `runs prune`, `--repair` outside `runs recover`). Check the command's `--help` instead of assuming shared flags.
- Durable runs are the single execution authority (R0–R11 complete): review and gate route through admitted, evidence-verified controller runs. The old `review.durable_runs` / `gate.durable_runs` yaml keys no longer exist — yamls carrying them fail config load with an unknown-key error.
- Operational surface: `sentinel runs status|logs|verify|recover|start|respond|abort|retry|prune`. Recovery classes come from `runs recover` (read-only scan); repair is `runs recover --repair <id>`; prune is explicit and never automatic.
- Historical readability is guaranteed: pre-R11 ledgers and run streams remain fully inspectable without conversion.

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

1. Compile sentinel and use bin/&lt;version&gt;/sentinel
2. Read the task's ficha in `docs/reingenieria/f{N}-*.md,`and create a execution plan.
3. Delegate (per Hard Rules) to the configured implementation agent.
4. Independently confirm the implementer's process finished before reading its diff.
5. Verify: `go build ./... && go vet ./... && go test -count=1 ./...`, plus `go test -count=1 -race` on every package the diff touches; `sentinel check`.
6. Read the real diff before slicing.
7. Stash unrelated pending files -&gt; `sentinel slice plan --json` -&gt; `sentinel slice apply` (empty `answers.json` unless `decisiones_pendientes` is non-empty) -&gt; restore the stash.
8. `sentinel review <backend-sha>` — the backend/logic commit, never just `HEAD`.
9. Per finding: verify the premise, apply fix via the implementation agent, slice it as its own commit if needed, re-review until clean or accepted with a documented reason.
10. `sentinel gate --stage pre-push` for the final PASS evidence.
11. After the final gate, review any remaining warnings, accepted findings, or deferred points. Check the later phase fichas to determine whether each is already planned; otherwise, assess whether it must be recorded as a follow-up with its target task and reason — record deferred pools in `docs/reingenieria/f0-deuda.md`.
12. Mark the task complete; report commits, findings, follow-ups, and the gate result.

## Output Contract

Report: task id, commits created, implementer-verification evidence, findings with resolution (fixed / accepted-with-reason), final gate result, follow-ups with their planned target or recorded reason, and any unrelated file kept out of the task's commits.

## References

- `docs/reingenieria/` — phase fichas (`F0`-`F9`, task ids `T{N}.{M}`); `f0-deuda.md` also hosts the deferred follow-up pool.
- `AGENTS.md` — the `slice plan`/`slice apply` flow and the volume guardian rule.
- `docs/runs-cli.md` — durable-run operator surface: stable JSON shapes, recovery classes, retention/migration policy.
- Every command's `-h/--help` — authoritative, drift-free flag documentation.
