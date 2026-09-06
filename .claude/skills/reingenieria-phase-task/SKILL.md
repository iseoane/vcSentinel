---
name: reingenieria-phase-task
description: "Trigger: phase F0-F9, task TN.M, reingenieria, Codex delegation, sentinel review, sentinel gate. Implement and verify one phase task independently of its implementer."
license: Apache-2.0
metadata:
  author: iseoane
  version: "1.4"
---
## Activation Contract

Load for implementation or verification of `docs/reingenieria/f{N}-*.md` task `T{N}.{M}`, including work delegated to another agent, in Claude Code or opencode. Preserve the existing trigger words and scope.

## Hard Rules

- Apply repository instructions in `AGENTS.md` and `CLAUDE.md`; this skill adds only task-specific checkpoints.
- Complete the acceptance matrix before delegation. Use the exact writer checkpoint in [delegation-and-acceptance.md](references/delegation-and-acceptance.md): English artifacts, focused RED before production code, GREEN evidence, and no self-review.
- Give the writer a dedicated worktree. Do not accept a completion message as proof: require exact command/outcome evidence and authoritative durable settlement before accepting its handoff.
- Sentinel review owns the semantic verdict and selected scope/dimensions. Never skip review or choose dimensions; do not stop while a Sentinel block remains.
- Verify every finding premise in the real code. Route a confirmed behavioral or multi-file finding to the same writer, or to a replacement writer when that writer cannot continue. The coordinator may correct only an already-understood mechanical one-file issue.
- Keep task paths and unrelated changes separate. Do not close while any blocker, unknown evidence, unavailable check, or contradictory finding lacks a recorded Sentinel resolution.

## Decision Gates

| Situation | Route |
|---|---|
| Writer authentication expires | Pause, request re-authentication, re-check readiness, then retry. |
| Confirmed behavioral/multi-file finding | Return it to the same writer; use a replacement writer only when necessary; create a new `fix(` commit and re-review it with Sentinel. |
| Confirmed understood mechanical one-file finding | Coordinator may apply the correction, then obtain a Sentinel review result. |
| Warning premise is disproven | Record why it is inert; never revert a correct change merely to silence it. |
| Admission, contradictory, or missing-evidence result | Inspect authoritative status/evidence; do not blind-retry or close. |

## Execution Steps

1. Read the task and repository instructions; fill the acceptance matrix and identify owned paths.
2. Delegate through the exact writer checkpoint; retain its dedicated worktree, effective identity/model/effort, and handoff evidence.
3. Independently settle and verify durable execution before accepting or reviewing the diff.
4. Inspect the task-only diff, then follow the command, slice, review, correction, and gate mechanics in [sentinel-operations.md](references/sentinel-operations.md).
5. Run the language scan and every pre-close check; report only evidenced outcomes.

## Output Contract

Return the task id, worktree, commits, exact Sentinel binary, writer identity/model/effort, exact commands and outcomes, durable root/child IDs with terminal and verification results, findings and resolutions, final gate, follow-ups, unrelated files observed and untouched, and pre-close evidence. Use the detailed report shape in [sentinel-operations.md](references/sentinel-operations.md).

## References

- [Delegation and acceptance checkpoints](references/delegation-and-acceptance.md)
- [Sentinel operations and report details](references/sentinel-operations.md)
- [Phase fichas and deferred follow-ups](../../../docs/reingenieria/)
- [Repository task/slice rules](../../../AGENTS.md) and [repository instructions](../../../CLAUDE.md)
- [Durable-run operator surface](../../../docs/design/runs-cli.md)
- Every command's `-h`/`--help` output is authoritative.
