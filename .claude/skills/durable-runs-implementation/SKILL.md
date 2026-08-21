---
name: durable-runs-implementation
description: "Trigger: durable runs, sentinel runs, R0-R11, A units, D units, durable-run tickets. Execute one roadmap unit with bounded changes, independent verification, and truthful evidence."
license: Apache-2.0
metadata:
  author: iseoane
  version: "1.1"
---

## Activation Contract

Load for any implementation or verification of the `sentinel runs` roadmap. Read
the roadmap, the active local ticket, and the unit-skill matrix before changing
files.

## Hard Rules

- Work on exactly one ticket whose blockers are complete; keep the ticket status and evidence current.
- Run the guardian before a bounded write. Use the noninteractive slice plan/apply flow when the review budget requires it; never answer pending human decisions.
- Keep artifacts in English, use the repository's configured implementation agent, and do not let an implementer self-approve its own diff.
- Verify the real diff independently with build, vet, focused tests, full tests, guardian, and the applicable review path.
- Preserve unrelated worktree changes. Do not debug provider reliability as a substitute for making failed calls durable.

## Decision Gates

| Situation | Action |
| --- | --- |
| A blocker is incomplete | Stop and report the blocking ticket. |
| A check or review is unavailable | Record the exact evidence and continue only if the ticket contract permits it. |
| A slice plan has pending decisions | Present them verbatim and wait for the user. |
| A critical unit is reached | Load `judgment-day` from the matrix before implementation or review. |

## Execution Steps

1. Confirm the frontier and read the ticket, roadmap, matrix, and relevant skills.
2. Run the guardian, estimate the bounded patch, and implement one vertical slice.
3. Run deterministic verification and review the real diff independently.
4. Update the ticket with commits, evidence, findings, rollback, and follow-ups.

## Output Contract

Report the ticket, changed work unit, verification results, independent findings,
rollback boundary, accepted follow-ups, and any unrelated files preserved.

## References

- `docs/arquitectura/run-control-implementation-plan.md`
- `docs/arquitectura/agent-execution-control-options.md`
- `.scratch/durable-runs/issues/`
- `references/unit-skill-matrix.md`
- `AGENTS.md`
