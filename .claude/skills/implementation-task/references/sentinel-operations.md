# vcSentinel operations and evidence

Use this reference for the task vcSentinel lifecycle. [AGENTS.md](../../../../AGENTS.md), [CLAUDE.md](../../../../CLAUDE.md), [durable-run docs](../../../../docs/design/runs-cli.md), and each command's `-h`/`--help` output remain authoritative. Do not guess flags.

## Command surface (post-R11)

- Every command and subcommand answers `-h`/`--help` with dedicated English help, exit `0`, and empty stderr. `help <command>` prints the same text; an unknown topic exits `1`.
- Flags are strict per subcommand. For example, `--older-than` belongs to `runs prune` and `--repair` belongs to `runs recover`; inspect help instead of assuming shared flags.
- Durable runs are the single execution authority: review and gate use admitted, evidence-verified controller runs. The obsolete `review.durable_runs` and `gate.durable_runs` YAML keys are invalid.
- The operational surface is `vcsentinel runs status|logs|verify|recover|start|respond|abort|retry|prune`. `runs recover` is a read-only recovery-class scan; `runs recover --repair <id>` is explicit repair. Pruning is explicit and never automatic.
- `vcsentinel runs attach [--run <id>] [--follow]` uses daemon-preferred routing with in-process fallback. Configured CLI adapters and `kind: acpx` are allowed; trust recorded effective identity and enforcement evidence, not provider assumptions.
- Pre-R11 ledgers and run streams remain readable without conversion.

## Candidate preparation and ownership

1. Read the version from `release.yml`. When source changes require a fresh CLI, rebuild with the repository build script and record the exact `bin/<version>/vcsentinel` path. The build script's source-mutating `gofmt -w .` step must finish before review or candidate freezing; re-snapshot the candidate after any source-mutating normalization.
2. Read the task definition (issue item, brief, or acceptance list) and identify the task-owned paths. Before planning or applying a slice, confirm no other agent is modifying the assigned worktree. Never stash, reset, stage, restore, or otherwise move unrelated changes. Pre-existing unrelated changes may remain, but stage task paths explicitly and run `vcsentinel check --staged` immediately before an authorized manual commit.
3. Do not launch a long-running vcSentinel command with bare shell `&` or pipe its output away. Run it in the foreground or through a harness that preserves full output. Run guardian or staged-volume checks alone before acting; never pipe them into a command chain that can mask their exit status.
4. Never rewrite history for a cosmetic mismatch without the user's explicit choice. Before `git reset --soft <target>`, print `git log --oneline` and confirm that `<target>` is the exact intended ancestor; an incorrect target can fold unrelated history into the rewrite.

## Review mechanics

vcSentinel determines the applicable candidate scope and dimensions. Do not require a presumed backend/logic review or select dimensions in advance. vcSentinel produces the semantic verdict; the calling agent does not. A small, obvious, or self-authored diff is not exempt, and a commit without a review record is not reviewed.

`vcsentinel review` prints one verdict per dimension, not the findings: exit `0` means `ok`/`warn`, exit `1` means at least one dimension blocks, and exit `4` means the review could not run. `unavailable` is not a pass: its dimension did not execute and has zero coverage. Treat it as missing evidence and re-run that dimension with a wider timeout; never use it to claim clean coverage.

Findings are absent from `vcsentinel status`, which reports verdicts only. Read them from `<git-common-dir>/vcsentinel/<sha>.json`, under `revisions[-1].dims[].findings[]`. Fichas written under a per-checkout ledger before the common-directory anchoring stay where they are and are no longer read by review/status/pr.

A plain `vcsentinel review` audits `HEAD` only. If a slice created several commits, name every commit lacking a record: `vcsentinel review <sha> [<sha>...]`. Do not use `--all`, which audits every recordless commit up to the target and can include unrelated history. Accept the dimensions vcSentinel selects; use `--dims` only to re-run a dimension that returned `unavailable`, never to narrow coverage.

A blocked ficha is credited with a `FixedIn` only when a later commit has a review exit of `0`, a message starting with `fix(`, and touches at least one file named in that ficha's findings. That credit is provenance, not a release: the ficha keeps blocking while its current findings block, so a `fix(` commit clears nothing by itself. To clear a block, re-audit the blocked commit (`vcsentinel review <sha>`) so its current findings are recorded again, or record a human `refute` for the finding.

`--timeout <seconds>` replaces `review.timeout` for one invocation on both `review` and `gate`. Use it for a large candidate and when reviewer search tools are unavailable and whole-file reading may exhaust the default `600s`. If a dimension dies on budget, its `reason` begins with the cause.

## Slicing and durable evidence

After reading the real diff and confirming ownership:

1. Run `vcsentinel slice plan --json > plan.json`.
2. If `pending_decisions[]` is empty, create `answers.json` as `{"plan_id":"<plan_id>","answers":{}}`. If decisions exist, present every decision verbatim, wait for the user, and record only the user's literal `bypass` or `abort` answer.
3. Run `vcsentinel slice apply --plan plan.json --answers answers.json`. Keep both transport files out of commits and remove them only when this agent created them.

Review every commit the slice produces. Capture every emitted durable root and child run ID, require a terminal status with `vcsentinel runs status --run <id>`, and run `vcsentinel runs verify --run <id>` for every run used as acceptance evidence. Use `vcsentinel runs logs` or `vcsentinel runs attach` for observation, and `vcsentinel runs recover` only according to its reported recovery class. Never infer settlement from a notification, transcript, process listing, or exit text alone. An `admission:` failure is evidence corruption or a stale snapshot: inspect the exact run status/logs and do not blindly retry.

Run the repository's focused and required checks with exact scopes: `go build ./...`, `go vet ./...`, `go test -count=1 ./...`, `go test -count=1 -race <explicit touched package list>`, and advisory `vcsentinel check`. Report the commands and outcomes; the race command must name the touched packages.

## Findings, correction, and gate

For each finding, verify the premise in the actual code before disposition. A confirmed behavioral or multi-file finding returns to the same writer, or to a replacement writer if the original cannot continue. The coordinator may apply only an already-understood mechanical one-file correction. Create a new `fix(` commit for every correction and re-review it with vcSentinel. Do not stop while a vcSentinel block remains; never mark it resolved from your own reading.

A warning whose premise is disproven is documented as inert; do not revert a correct fix to silence it. If only advisory findings remain and there is no production consumer, stop local correction and accept them only with a written reason recorded with the vcSentinel result. After the final review, inspect remaining warnings, accepted findings, and deferred points against `docs/issues/`; if no open item already plans the work, record the follow-up there with target and reason.

For final evidence, run `vcsentinel gate --stage pre-push`. Capture and settle every root/child run it emits and verify each accepted run with `vcsentinel runs verify --run <id>`. Preserve an `admission:` failure as a blocker and inspect its status/logs without a blind retry. A manual commit in an explicitly authorized workflow requires `vcsentinel check --staged` against the exact staged candidate immediately before commit; this is the enforcing 400-line boundary, while `vcsentinel check` remains advisory.

## Final report contract

Write every new or modified artifact, commit message, finding disposition, and follow-up in English; preserve legacy Spanish unless translation is explicitly in scope; use English Conventional Commits and require `commit_language: en`. Report all of the following, with exact commands and outcomes:

- task ID, dedicated worktree path, commits created, and exact vcsentinel binary;
- effective agent identity, model, and reasoning effort from execution evidence;
- every durable root and child run ID, terminal state, `runs verify` result, and any admission or recovery blocker;
- implementer/handoff settlement evidence and the exact build, test, race, and package scopes;
- each finding and its resolution (`fixed` or `accepted-with-reason`), including contradictory or deferred points;
- final gate command and result;
- each follow-up's planned target or recorded reason; and
- every unrelated file observed, plus an explicit statement that unrelated files were not staged, stashed, modified, restored, or committed.

Do not mark the task complete until the delegation reference's pre-close checklist has evidence for every row.
