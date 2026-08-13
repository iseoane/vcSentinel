---
name: reingenieria-phase-task
description: "Trigger: phase F0-F9, task TN.M, reingenieria, Codex delegation, sentinel review, sentinel gate. Implement and verify one phase task independently of its implementer."
license: Apache-2.0
metadata:
  author: iseoane
  version: "1.0"
---
## Activation Contract

Load when implementing or verifying a task from `docs/reingenieria/f{N}-*.md` (id `T{N}.{M}`), in Claude Code or opencode. Applies whether the calling agent implements the task or verifies another agent's (e.g. Codex's) implementation of it.

## Hard Rules

- When delegating implementation  instruct the delegate to build (in opencode user agent in GTP-5.6 Terra High) and test only, never to self-review. The calling agent always runs the semantic review itself — never trust the implementer's own verdict on its own diff.
- Never treat a "completed" notification as proof. Confirm the real process ended (e.g. `ps -ef | grep <tool>`, the job's own status command) before reading its diff.
- `sentinel gate` only reviews `HEAD`, and dimension depth depends on the changed layer (a test-only `HEAD` gets a shallow spec/tests-only pass). Always also run `sentinel review <sha>` with full dimensions on the backend/logic commit itself.
- Before accepting or reverting a finding, verify its premise in the code (grep the real call sites, check where the data actually originates) — do not act on a suggestion by faith alone.
- Stash unrelated pending changes (e.g. an auto-migrated `.vas_sentinel/vassentinel.yml`) before `sentinel slice`, and restore them after.
- Do not use worktree isolation when delegating to Codex: its jobs are tracked per `workspaceRoot`, so a status check from the main repo won't see them.
- Never rewrite commit history to fix a cosmetic mismatch (e.g. a generic slice message on a real fix commit) without the user's explicit choice.

## Decision Gates


| Situation                                      | Action                                                              |
| ---------------------------------------------- | ------------------------------------------------------------------- |
| Delegate's auth expired mid-task               | Ask the user to re-authenticate, re-check readiness, retry          |
| Review verdict is `block`/CRITICAL             | Fix, commit, and re-review that fix before slicing further          |
| Verdict is `warn` and the premise is disproven | Document why it is inert; do not revert a correct fix to silence it |
| A test asserts the old, wrong premise          | Update the test's fixture/assertion to the real invariant           |


## Execution Steps

1. Compile sentinel and use bin/&lt;version&gt;/sentinel
2. Read the task's ficha in `docs/reingenieria/f{N}-*.md,`and create a execution plan.
3. Delegate (per Hard Rules) to subagent in GTP-5.6 Terra High.
4. Independently confirm the implementer's process finished in GPT-5.6 luna medium.
5. Verify: `go build ./... && go vet ./... && go test ./...`, `sentinel check`.
6. Read the real diff before slicing.
7. Stash unrelated pending files -&gt; `sentinel slice plan --json` -&gt; `sentinel slice apply` (empty `answers.json` unless `decisiones_pendientes` is non-empty) -&gt; restore the stash.
8. `sentinel review <backend-sha>` with full dimensions — the backend/logic commit, never just `HEAD`.
9. Per finding: verify the premise, apply fix with subagent GTP-5.6 Terra High, slice it as its own commit if needed, re-review until clean or accepted with a documented reason.
10. `sentinel gate --stage pre-push` for the final PASS evidence.
11. After the final gate, review any remaining warnings, accepted findings, or deferred points. Check the later phase fichas to determine whether each is already planned; otherwise, assess whether it must be recorded as a follow-up with its target task and reason.
12. Mark the task complete; report commits, findings, follow-ups, and the gate result.

## Output Contract

Report: task id, commits created, implementer-verification evidence, findings with resolution (fixed / accepted-with-reason), final gate result, follow-ups with their planned target or recorded reason, and any unrelated file kept out of the task's commits.

## References

- `docs/reingenieria/` — phase fichas (`F0`-`F9`, task ids `T{N}.{M}`).
- `AGENTS.md` — the `slice plan`/`slice apply` flow and the volume guardian rule.

