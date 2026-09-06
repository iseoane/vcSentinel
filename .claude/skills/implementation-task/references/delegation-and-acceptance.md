# Delegation and acceptance checkpoints

Use this reference with `SKILL.md`. Repository-wide delegation, testing, and language policy remains authoritative in [AGENTS.md](../../../../AGENTS.md) and [CLAUDE.md](../../../../CLAUDE.md); these checkpoints add only task evidence requirements.

## Contract matrix

Complete this matrix before invoking a writer. Add one row for every named acceptance criterion and every operational obligation; do not begin delegation with an unowned or unmeasurable row.

| ID | Criterion or obligation | Source / observable check | Owner | Exact evidence (command and outcome) | Disposition |
|---|---|---|---|---|---|
| C-01 | `<named criterion>` | `<task line or behavior>` | `<writer/coordinator/Sentinel>` | `<command; exit/status; result>` | `<met / blocked / follow-up>` |
| C-02 | Focused RED before production code | `<behavioral check>` | Writer | `<exact command and failing outcome>` | `<met / blocked>` |
| C-03 | GREEN after production code | `<focused check>` | Writer | `<exact command and successful outcome>` | `<met / blocked>` |
| C-04 | Final review, run verification, and gate | `<Sentinel record>` | Sentinel | `<IDs, terminal states, verify, gate result>` | `<met / blocked>` |

Minimum rows cover: every task criterion; owned paths and worktree; writer RED/GREEN evidence; every task commit's final review; every durable root and child run's settlement and verification; the final gate; worktree cleanliness; the language scan; contradictory-finding disposition; and each follow-up. Empty, pending, unknown, unavailable, or merely asserted evidence is not acceptance.

## Exact writer-prompt checkpoint

Copy this checkpoint into the writer prompt and fill placeholders only. Do not weaken, reorder, or omit the required lines.

```text
SENTINEL_WRITER_CHECKPOINT
Scope: <task-owned paths and acceptance criteria>
Worktree: <dedicated worktree path>
LANGUAGE: Write every new or modified technical artifact, commit message, finding disposition, follow-up, and handoff in English. Preserve legacy Spanish unless translation is explicitly in scope.
RED (before production code): Run one focused, behavior-level check that fails for the requested behavior before writing production code. Return the exact command, exit status, and observed failure.
IMPLEMENTATION: Change only the assigned task paths. Do not stage, stash, reset, restore, or modify unrelated work.
GREEN: After writing production code, run focused checks for the requested behavior. Return every exact command, exit status, and observed success.
REVIEW: Do not self-review, assign a semantic verdict, select review dimensions, or claim acceptance. Sentinel review performs the review.
HANDOFF: Return changed paths, commits, exact commands and outcomes, effective agent identity/model/reasoning effort, and every durable run ID with authoritative terminal status and `runs verify` result. Name unrelated paths observed but untouched.
```

A docs-only task still requires the checkpoint's language and review-boundary lines; if a RED or GREEN check cannot exercise the named behavior, record the exact reason and obtain a task-specific decision rather than asserting success.

## Delegation and handoff

1. Confirm the matrix, task paths, and ownership before delegation. Give each writer a dedicated Git worktree; all writer, job-status, and durable-run commands use that same worktree. Never delegate into a shared worktree.
2. Let repository instructions select the execution route; let the delegating agent choose the model and reasoning effort best suited to the task unless the user explicitly selects them. Record the effective agent, model, and reasoning effort from execution evidence. Never hard-code those values or treat a provider assumption as evidence.
3. The writer implements and reports focused RED/GREEN evidence only; it never supplies semantic review or its own acceptance verdict. An authentication-expired writer is paused until re-authentication and readiness are confirmed.
4. A completion notification is not proof. Reject a handoff lacking exact command lines, exit/status and observed outcomes, changed paths, and authoritative durable settlement/verification. For external harnesses, use that harness's authoritative job status; never infer completion from a process listing.
5. Before slicing or accepting a diff, confirm that no other agent is modifying the assigned worktree and that every changed path belongs to this task. Preserve unrelated work: do not stash, reset, stage, restore, or otherwise move it. Pre-existing unrelated changes may be recorded, but task commits use an explicit `git add <paths>` plus the enforcing staged check required by repository policy.

## Confirmed-finding routing

First verify each finding's premise in the real code, including the actual call sites and data origin. Never accept or revert a suggestion by faith alone.

- A confirmed behavioral finding or a confirmed finding spanning multiple files returns to the same writer with the finding IDs, evidence, candidate revision, and exact affected paths. If that writer cannot continue, assign a replacement writer with the same task context; do not silently transfer a semantic verdict to the coordinator.
- The coordinator may apply a correction only when it is an already-understood mechanical change in one file. The coordinator must otherwise use the same writer or a replacement writer.
- Each confirmed correction follows the concrete Sentinel flow: verify the premise, send it to the same writer or a replacement writer, create a new `fix(` commit, and re-review it. Do not stop while a Sentinel block remains.
- Re-review every new `fix(` commit with Sentinel. A blocked, unknown, unavailable, contradictory, or unverified result stays open until Sentinel records a resolution; blockers never silently resolve.

- A `block` or `CRITICAL` verdict must be fixed, committed, and re-reviewed before any further slicing; never carry it forward as an unresolved candidate.
- If a warning's premise is disproven, record why it is inert and do not revert a correct change. If only advisory findings remain and there is no production consumer, stop local correction and accept them only with a written reason recorded with the Sentinel result.

- If a test asserts the old or wrong premise, update its fixture or assertion to the actual invariant rather than preserving a false expectation.

## Language handoff scan

Before close, inspect every changed or newly created artifact, commit subject/body, finding disposition, follow-up, matrix row, and writer handoff. Confirm that generated material is English and that legacy Spanish was preserved unless translation was explicitly requested. Record the scan scope and exact outcome. Any newly generated non-English material, unexplained translation, or missing scan evidence blocks close.

## Pre-close acceptance checklist
Do not mark the task complete until every item has an evidence pointer and no item is `pending`, `unknown`, or `blocked`:

- [ ] Every named acceptance criterion is mapped in the completed matrix with objective evidence and an explicit disposition.
- [ ] The final task candidate and every task commit have a Sentinel review record; no review result is inferred from self-review.
- [ ] Every durable root and child run used as evidence is terminal, and every one has a matching `runs verify` result.
- [ ] The final gate result is recorded with its exact command/outcome; admission, recovery, unavailable, or other gate blockers are settled, not waived by silence.
- [ ] Task-owned paths are clean after delivery; unrelated pre-existing files are listed and demonstrably untouched; transport files are handled according to repository policy.
- [ ] The language handoff scan is complete and has no unresolved violation.
- [ ] Every contradictory finding has an authoritative resolution or an explicit permitted follow-up; none is ignored or hand-waved.
- [ ] Findings, accepted warnings, and deferred work have written reasons and planned targets where required.
- [ ] The final report contains exact commands, outcomes, identities, commits, run evidence, gate evidence, and all unrelated files observed.

Any unchecked item forbids completion. Do not stop while a Sentinel block remains; self-review or prose cannot convert missing evidence into acceptance.
