# Durable Runs Unit Skill Matrix

This matrix routes each roadmap family to the smallest useful supporting skill.
The project skill remains the workflow source of truth; this file only selects
specialist context and the stronger audit points.

Every family also requires the `code-review` skill for independent diff review
while the durable-runs roadmap is active through R11. A `judgment-day` entry in
the Strong audit column adds an adversarial gate; it never replaces `code-review`.

| Unit family | Scope | Supporting skills | Strong audit |
| --- | --- | --- | --- |
| R0 | Gate evidence | `go-testing`, `code-review` | No |
| R1 | Pure durable contracts | `codebase-design`, `go-testing` | No |
| R2 | Durable persistence | `codebase-design`, `go-testing`, `diagnosing-bugs` | No |
| R3 | Per-run controller | `codebase-design`, `go-testing`, `diagnosing-bugs` | No |
| R4 | Control-path migration | `codebase-design`, `go-testing` | Yes: `judgment-day` |
| R5 | Compatibility and recovery | `codebase-design`, `go-testing` | No |
| R6 | Evidence and admission cutover | `codebase-design`, `go-testing` | Yes: `judgment-day` |
| R7 | Cancellation and process ownership | `codebase-design`, `go-testing`, `diagnosing-bugs` | Yes: `judgment-day` |
| R8-R9 | Remaining core control/migration | `codebase-design`, `go-testing` | No |
| R10 | Core durable-run completion | `codebase-design`, `go-testing` | Yes: `judgment-day` |
| R11 | Core follow-up and cleanup | `codebase-design`, `go-testing` | No |
| A1 | ACP/acpx experiment | `research`, `codebase-design` | No |
| A2 | ACP/acpx production adapter | `research`, `codebase-design`, `go-testing` | Yes: `judgment-day` |
| D1 | Repository daemon seam | `codebase-design` | No |
| D2 | Daemon lifecycle | `codebase-design`, `go-testing` | Yes: `judgment-day` |
| D3 | TUI/live observation | `codebase-design`, `go-testing` | Yes: `judgment-day` |
| D4 | Remote-host expansion | `codebase-design`, `research` | No |
| D5 | Remote control completion | `codebase-design`, `go-testing` | Yes: `judgment-day` |

R units are the current implementation frontier. D1 uses `codebase-design` but is
not itself a strong-audit milestone. Update this matrix before starting a unit
whose scope or risk has changed.
