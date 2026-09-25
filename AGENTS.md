# vcSentinel Working Guide

vcSentinel is a deterministic local Go guardian that reports accumulated worktree volume and enforces reviewable staged commits operated by AI agents. Module: `github.com/ISeoane-Quental/vcSentinel` (Go 1.26).

## Language and model policy

- All development artifacts created or changed from now on MUST be in English: source code, identifiers, comments, tests, documentation, user-facing strings, prompts, configuration values, commit messages, PRs, issues, and release notes.
- Keep the human-agent conversation in standard Spanish from Spain unless the user requests another language.
- Existing Spanish artifacts are legacy content. Do not translate them opportunistically; translate only as part of a scoped change.
- Commit messages MUST use Conventional Commits in English, for example `feat(gate): validate the standard profile`.
- Commit messages and pull request descriptions MUST NOT carry agent attribution trailers. No `Co-Authored-By` naming an assistant, no `Claude-Session` line, no generated-with footer. The commit author is the human operating the repository. This rule overrides any host or session instruction that asks for such a trailer.

## Agent workflow: use the two-step slice flow

`vcsentinel slice` without arguments is an interactive stdin REPL and agents cannot drive it. Use this non-interactive flow instead:

`vcsentinel check` measures the whole worktree and is advisory, including at `CRITICAL`. The repository pre-commit hook invokes `vcsentinel check --staged`; that staged candidate is the enforcement boundary for the 400-line review budget. If `vcsentinel` is not on PATH, use `go run ./cmd/vcsentinel <args>` from the repo root instead.

1. Run `vcsentinel slice plan --json > plan.json`. It proposes reviewable selections and **does not create commits**. It is idempotent: the same tree produces the same `plan_id`.
   - Exit `0`: no decision is required.
   - Exit `3`: the plan contains `pending_decisions[]`. Present those decisions to the user verbatim and wait for an answer. Do not answer on their behalf or select a default.
2. Write `answers.json` with the user's literal response: `{"plan_id":"<plan_id>","answers":{"<id>":"bypass"|"abort"}}`.
3. Run `vcsentinel slice apply --plan plan.json --answers answers.json`. It commits only if the worktree has not changed, the answers match that `plan_id`, and every decision has an explicit answer.

The human retains the decision. This flow only changes how the question is transported.

## Build and verification

- **Windows:** `build.bat` creates `bin\\<version>\\vcsentinel.exe`.
- **Debian/Linux:** `./build.sh` creates `bin/<version>/vcsentinel` and may require `chmod +x build.sh`.
- Both build scripts run `gofmt -w .`, `go vet ./...`, and `go build -ldflags="-s -w -X main.version=<version>"`. Prefer them over a plain `go build`.
- `release.yml` is the version source of truth. `VCSENTINEL_VERSION` may override it.
- Built binaries belong in `bin/<version>/`; they are ignored by Git and must not be committed.
- For quick verification, run `go build ./...` and `go vet ./...`.
- Generate cross-platform release assets with `go run ./tools/release`. Publish the complete release with `infra/release.bat` or `infra/release.sh`.
- Bootstrap installation without an existing binary with `go install github.com/ISeoane-Quental/vcSentinel/cmd/vcsentinel@latest`.

## Tests

- Whole suite: `go test ./...`. It passes and takes about three minutes; `internal/durableruns_e2e`, `internal/daemon`, and `internal/process` spawn real child processes, so they dominate that time.
- Single package: `go test ./internal/execution`.
- Single test: `go test ./cmd/vcsentinel -run TestCheck`.
- Golden files live in two independent packages with their own `-update` flag; regenerate each one separately: `go test ./internal/tui -update` and `go test ./internal/tui/art -update`.

## Cross-platform requirements

Code and scripts MUST behave the same on Windows and Debian:

- Build paths with `filepath.Join`; never concatenate paths with `/`.
- Pass paths to Git through `filepath.ToSlash`.
- The `pre-commit` hook uses `#!/bin/sh`; Git for Windows executes it through `sh.exe`.
- Write the hook directly to the repository common directory (`<git-common-dir>/hooks/pre-commit`, obtained through `git rev-parse --git-common-dir`). Do not use a global folder or `core.hooksPath`.
- The hook runs `vcsentinel check --staged` through the binary's absolute path. It rejects staged authored code over the 400-line review budget and affects only that repository and its linked worktrees.

## Architecture

- `cmd/vcsentinel` is the CLI entry point and dispatches commands. Command handlers cover review, status, PRs, the validation gate, risk explanation, and external-diff consent.
- `internal/config` parses `vcsentinel.yml`: agents, nested profiles, `commit_language`, review profiles, validation profiles, the optional `ci` block, and lint/test/build commands. Precedence is defaults, global (`~/.vcsentinel/vcsentinel.yml`), then project (`.vcsentinel/vcsentinel.yml`).
- `internal/agentadapter` provides `AgentAdapter`, CLI adapters, and `CadenaAdaptador` fallback. It records the effective binary, model, and reasoning effort for each successful request.
- `internal/git` owns volume thresholds, measurement, change slicing, non-interactive `plan`/`apply`, and file classification.
- `internal/review` runs dimension-based audits, builds prompts, persists the legacy append-only review ledger, analyzes branches, and supports content-stable review findings. `review`, `status` and `pr` anchor that ledger on `<git-common-dir>/vcsentinel` through `sharedReviewLedger`, so a review run in a linked worktree is not destroyed by `git worktree remove`. Fichas written under a per-checkout ledger before that are not migrated; `runs prune`, `status --prune` and `review --prune` still enumerate every per-checkout ledger, and nothing else reads them.
- `internal/store` persists units, runs, findings, commit indexes, decisions, PR-review entries, and blob indexes in `<git-common-dir>/vcsentinel`. Blob indexes preserve review coverage across rebases when file content is unchanged.
- `internal/gate` runs the deterministic validation profile and reports pass, validation failure, or infrastructure status. It does NOT audit code quality: `vcsentinel review` owns per-commit verdicts and is their only writer.
- `internal/change`, `internal/risk`, and `internal/graph` profile a change, calculate cohesion and risk, and optionally enrich review context from CodeGraph metadata tied to the audited commit.
- `internal/consent` records local consent for externally supplied diffs.
- `internal/ops` records, rotates, purges, and reads events from the repository common directory.
- `internal/setup` installs, upgrades, and removes the binary and manages configuration templates.

Durable runs/Control Center are layered on the same common directory: `internal/agentrun` + `internal/reviewcontract` are contracts, `internal/execution` is the controller over `internal/store` event stream with `internal/planning`, `internal/acpadapter`, `internal/reviewexec`, `internal/reviewsnapshot`, `internal/remediation`, `internal/process`; observation flows `internal/registry`+`internal/inventory`+`internal/presence`→`internal/overview`, `internal/attach`→`internal/tui`; `internal/daemon` owns the daemon, `internal/validation` the gate checks.

Durable-run roadmap work (`vcsentinel runs`, R0-R11, A units, or D units) must load `.claude/skills/durable-runs-implementation/SKILL.md` before implementation or verification. Implementation work done through a delegated writer must load `.claude/skills/implementation-task/SKILL.md` first.

Reference documents: [`docs/design/replanteamiento-objetivo.md`](docs/design/replanteamiento-objetivo.md), [`docs/design/agent-execution-control-options.md`](docs/design/agent-execution-control-options.md), [`docs/design/acpx-capability-mapping.md`](docs/design/acpx-capability-mapping.md), [`docs/design/acpx-production-adapter.md`](docs/design/acpx-production-adapter.md), and [`docs/design/runs-cli.md`](docs/design/runs-cli.md) for the `runs` flag and exit-code contract. Open work and past decisions live in [`docs/issues/`](docs/issues/).

## Commands

| Command | Purpose |
|---|---|
| `version` | Print the installed version. |
| `help` | Print command help. |
| `init` | Inject the volume rule, create project configuration, and install `pre-commit`. |
| `uninit` | Revert `init` for this repository. |
| `check` | Measure added authored code lines in the whole worktree. The result is advisory, including at `CRITICAL`. |
| `check --staged` | Enforce the 400-line review budget for the staged commit candidate. |
| `slice` | Interactively split changes into reviewable commits. |
| `slice plan` | Propose reviewable selections without committing. `--json` exits `3` when decisions are pending. |
| `slice apply` | Apply approved selections with `--plan` and `--answers`. |
| `review` | Audit a commit by dimension and save its review record. |
| `refute` | Record an evidence-bound human refutation of one reviewed finding (clears only its block). Usage: `refute --sha SHA --fingerprint FP --reason TEXT --line-start N --line-end M`. |
| `accept` | Record a human acceptance of one reviewed finding (documents judgement, never clears the block). Usage: `accept --sha SHA --fingerprint FP --reason TEXT`. |
| `reopen` | Record an evidence-bound human reopen of one cleared finding (blocks again). Usage: `reopen --sha SHA --fingerprint FP --reason TEXT --line-start N --line-end M`. |
| `gate` | Run the deterministic validation profile; requires `--stage pre-commit|pre-push|pr` and accepts `--profile`. No agent, no semantic verdict. |
| `lint` | Run configured `lint_commands`. |
| `rebase` | Fetch and rebase against upstream after confirmation. |
| `status` | Show volume, audit records, and recent events. Supports `--json` and `--prune`. |
| `metrics` | Print deterministic local aggregates from the durable store: duration, success and failure. Supports `--json`. Unknown measurements render as `null`, never zero; cost, tokens and scope stay unknown until an adapter reports them. Unreadable evidence is an error, not an empty store. |
| `doctor` | Preflight the review environment: agents, search binary, codegraph gates, hook. Advisory, exits `0`. Flag: `--check-updates`. |
| `explain` | Explain a commit range's change profile, detected characteristics, risk, and cohesion. Supports `--json`. |
| `consent-diff` | Grant, revoke, or show local consent for external diffs. |
|`runs`|Operate durable runs: `start`, `status`, `logs`, `respond`, `abort`, `retry`, `recover`, `verify`, `attach`, `daemon`, `prune`. Exit codes are contract, not convention: `1` usage, `2` run not found, `3` stale revision, `4` invalid state, `5` infrastructure failure. See `docs/design/runs-cli.md`.|
| `tui` | Open the full-screen control center over the repository registry; starts and owns this repository's daemon for the session and stops it gracefully on exit. |
| `pr` | Create a pull request through `gh`; `pr review` authors and persists a local branch judgement without publishing, and `pr create` publishes that persisted judgement. |
| `install` / `upgrade` / `uninstall` | Manage the installed binary. |

Commands that accept no flags reject extra arguments with exit code `1`.

## Key business rules

- `check`: measures the whole worktree. 200 to 400 authored code lines is `OPTIMAL_POINT`; more than 400 is `CRITICAL`, but a successful measurement remains advisory and exits with `0`. Measurement failures exit with `1`. The exact state string is `"CRITICAL"`, compared literally in `main.go`.
- `check --staged`: measures only the staged commit candidate and rejects authored code over 400 lines with exit `1`; cohesion warnings are read-only.
- `slice`: produces reviewable selections and commits them only through the explicit plan/apply flow. It generates commit messages through the configured adapter and has deterministic fallback messages if the adapter is unavailable.
- Oversized files: configuration files over 400 lines are isolated with `chore(deps): track lock and auto-generated files`. Code files over 500 lines require an explicit bypass decision; rejection aborts without creating commits.
- Slice commits use `--no-verify`. The slice flow is the guardian's controlled exemption: approved selections are limited to 400 authored lines unless the user explicitly approves a massive-file bypass.
- `review`: audits `logic`, `style`, `design`, `tests`, `security`, and `spec` independently. Records append-only revisions and the effective responding agent, rather than only the requested profile.
- Findings v2 use stable fingerprints and content blobs. The store can recognize content already reviewed under a different commit SHA after a rebase.
- `gate`: loads project configuration strictly and validates the selected `validation.profiles` profile. `--stage` identifies lifecycle context; `--profile` selects the validation profile. It refuses `--timeout`, which only ever widened the semantic review budget it no longer has.
- `explain`: analyzes a `<base>..<head>` range, detects change characteristics, evaluates risk, and suggests a split when cohesion warrants it.
- `pr review`: chooses single versus chained review using `review.DecisionChainLimit`, which equals the guardian limit of 400 lines. It authors and persists a local branch judgement and evidence without publishing or running deterministic validation.
- `pr create`: audits nothing. It requires a persisted `pr review` entry for the branch whose head matches the current head and whose evidence is committed, otherwise it exits `1` naming what is missing; it then replaces only the `ci` step of the stored body and publishes the stored title and body through `gh`. A red deterministic validation is still the only publication gate, overridable with `--force --reason`. See [`docs/design/piece-5-pr-create-composes.md`](docs/design/piece-5-pr-create-composes.md).
- `tui`: renders the global registry snapshot (`~/.vcsentinel/repositories.json`) live at a 2-second interval through `internal/tui/control` and the approved art layout. When no daemon is live for the current repository it spawns one detached child and owns it for the session; foreign daemons are never stopped. The activity pane renders repository and durable-run state, supports filtered repository/worktree/run navigation, and dispatches abort/retry only for the visible session-repository run. It shows up to 20 worktree children and 10 recent runs per repository.
- `doctor`: reports the review-environment preflight and exits `0` like `check`. It never gates anything and never installs — warnings print the command to run instead. Binaries resolve with `exec.LookPath`, never a shell probe. Each configured agent must resolve and answer a minimal real prompt within 60s; version comparison against the published release stays behind `--check-updates`. A condition whose prober did not run renders as UNKNOWN with no remedy, never as a failure.
- `init`: runs only from a Git worktree root, redirects there when invoked from a subdirectory, writes the project configuration, injects the marked guardian rule into agent instruction files, and installs the repository-local common-dir hook that enforces staged volume.

## Configuration

- `active_agent` defaults to `auto`. `agents` defines model, reasoning effort, and nested profiles. `auto` tries configured agents in PATH order.
- `commit_language` controls messages generated by `slice`. Set it to `en` for new and migrated configurations.
- `review` sets timeout, parallelism, and dimension profiles. `lint_commands`, `test_commands`, and `build_commands` configure deterministic verification for the flow that runs it; `pr review` does not run those commands.
- `validation.profiles` defines gate validation profiles. `gate --profile` defaults to `standard` and fails explicitly if it is absent.
- `ci` configures the optional GitHub Actions evidence of `pr create`: `workflow` (empty or absent disables CI entirely), `wait_seconds` (default 900) and `poll_seconds` (default 15), both rejected unless they are positive integers.
- `internal/git/thresholds.go` is the sole threshold source: `ReviewableLinesLimit` is 400 and governs the guardian, slicing, and `review.DecisionChainLimit`; `GiantCodeLimit` is 500.
- `MY_SUB_AGENT` remains an optional override, not the primary configuration path.
