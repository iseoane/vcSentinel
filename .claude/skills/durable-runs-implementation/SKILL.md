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
- Keep artifacts in English. Delegate implementation to the available `reingenieria-implementer` subagent, not merely to the repository's configured implementation agent. The delegating agent owns frontier and blocker checks, guardian execution, human slice-decision transport without answering on the user's behalf, deterministic verification, independent `code-review`, ticket evidence, and closure.
- Direct the implementer to make only the ticket's bounded code/test changes and run focused build/test checks. It must not self-review, independently accept findings, run `sentinel slice apply`, commit, update or close the ticket, or claim closure.
- Use the `code-review` skill for the independent review of every durable-runs ticket until the roadmap reaches R11 completion; stronger audits in the matrix are additional, not substitutes.
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

1. Confirm the frontier and blockers, then read the ticket, roadmap, matrix, and relevant skills.
2. Run the guardian, estimate the bounded patch, and present any pending human slice decisions before delegating.
3. Delegate one vertical slice to `reingenieria-implementer`, limiting it to bounded code/test changes and focused build/test checks.
4. Run deterministic verification and invoke `code-review` against the ticket's fixed base before recording findings.
5. Update the ticket with commits, evidence, findings, rollback, and follow-ups; close it only after the evidence is complete.

## Output Contract

Report the ticket, changed work unit, verification results, independent findings,
rollback boundary, accepted follow-ups, and any unrelated files preserved.

## References

- `docs/design/agent-execution-control-options.md`
- `docs/issues/` (open work and past decisions; the former run-control roadmap is consolidated there)
- `.scratch/durable-runs/issues/`
- `references/unit-skill-matrix.md`
- `AGENTS.md`
