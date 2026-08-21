# Durable Runs Local Tracker

This directory is the local tracker for the initial `sentinel runs` delivery
chain. It is intentionally separate from the global reengineering roadmap in
`docs/reingenieria/`.

## Workflow

1. Work only on the first ticket whose blockers are complete.
2. Read the roadmap and the ticket before writing code.
3. Keep implementation, tests, and ticket evidence within one work unit.
4. Update the ticket with exact commits, checks, review findings, and follow-ups.
5. Keep Engram as the persistent session memory; these files are the human and
   agent-facing acceptance record.

## Frontier

| Ticket | Outcome | Status |
| --- | --- | --- |
| 01 | Expose gate and review evidence. | Complete |
| 02 | Define durable execution contracts. | Complete |
| 03 | Add the durable run store. | Complete |
| 04 | Add the execution controller. | Ready; blocked by 03 |

The dependency chain is strictly `01 -> 02 -> 03 -> 04`.
