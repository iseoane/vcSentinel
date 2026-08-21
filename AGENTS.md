# VAS Sentinel Working Guide

VAS Sentinel is a deterministic local Go guardian that reports accumulated worktree volume and enforces reviewable staged commits operated by AI agents. Module: `github.com/ISeoane-Quental/vas.sentinel` (Go 1.26).

## Language and model policy

- All development artifacts created or changed from now on MUST be in English: source code, identifiers, comments, tests, documentation, user-facing strings, prompts, configuration values, commit messages, PRs, issues, and release notes.
- Keep the human-agent conversation in standard Spanish from Spain unless the user requests another language.
- Use GPT-5.6 Terra with high reasoning effort for implementation unless the user explicitly requests a different model or effort.
- Existing Spanish artifacts are legacy content. Do not translate them opportunistically; translate only as part of a scoped change.
- Commit messages MUST use Conventional Commits in English, for example `feat(gate): validate the standard profile`.

## Agent workflow: use the two-step slice flow

`sentinel slice` without arguments is an interactive stdin REPL and agents cannot drive it. Use this non-interactive flow instead:

`sentinel check` measures the whole worktree and is advisory, including at `CRITICO`. The repository pre-commit hook invokes `sentinel check --staged`; that staged candidate is the enforcement boundary for the 400-line review budget.

1. Run `sentinel slice plan --json > plan.json`. It proposes reviewable selections and **does not create commits**. It is idempotent: the same tree produces the same `plan_id`.
   - Exit `0`: no decision is required.
   - Exit `3`: the plan contains `decisiones_pendientes[]`. Present those decisions to the user verbatim and wait for an answer. Do not answer on their behalf or select a default.
2. Write `answers.json` with the user's literal response: `{"plan_id":"<plan_id>","respuestas":{"<id>":"bypass"|"abortar"}}`.
3. Run `sentinel slice apply --plan plan.json --answers answers.json`. It commits only if the worktree has not changed, the answers match that `plan_id`, and every decision has an explicit answer.

The human retains the decision. This flow only changes how the question is transported.

## Build and verification

- **Windows:** `build.bat` creates `bin\\<version>\\sentinel.exe`.
- **Debian/Linux:** `./build.sh` creates `bin/<version>/sentinel` and may require `chmod +x build.sh`.
- Both build scripts run `gofmt -w .`, `go vet ./...`, and `go build -ldflags="-s -w -X main.version=<version>"`. Prefer them over a plain `go build`.
- `release.yml` is the version source of truth. `SENTINEL_VERSION` may override it.
- Built binaries belong in `bin/<version>/`; they are ignored by Git and must not be committed.
- For quick verification, run `go build ./...` and `go vet ./...`.
- Generate cross-platform release assets with `go run ./tools/release`. Publish the complete release with `infra/release.bat` or `infra/release.sh`.
- Bootstrap installation without an existing binary with `go install github.com/ISeoane-Quental/vas.sentinel/cmd/sentinel@latest`.

## Cross-platform requirements

Code and scripts MUST behave the same on Windows and Debian:

- Build paths with `filepath.Join`; never concatenate paths with `/`.
- Pass paths to Git through `filepath.ToSlash`.
- The `pre-commit` hook uses `#!/bin/sh`; Git for Windows executes it through `sh.exe`.
- Write the hook directly to the repository common directory (`<git-common-dir>/hooks/pre-commit`, obtained through `git rev-parse --git-common-dir`). Do not use a global folder or `core.hooksPath`.
- The hook runs `sentinel check --staged` through the binary's absolute path. It rejects staged authored code over the 400-line review budget and affects only that repository and its linked worktrees.

## Architecture

- `cmd/sentinel` is the CLI entry point and dispatches commands. Command handlers cover review, status, PRs, the validation gate, risk explanation, and external-diff consent.
- `internal/config` parses `vassentinel.yml`: agents, nested profiles, `commit_language`, review profiles, validation profiles, and lint/test/build commands. Precedence is defaults, global (`~/.vas_sentinel/vassentinel.yml`), then project (`.vas_sentinel/vassentinel.yml`).
- `internal/agentadapter` provides `AgentAdapter`, CLI adapters, and `CadenaAdaptador` fallback. It records the effective binary, model, and reasoning effort for each successful request.
- `internal/git` owns volume thresholds, measurement, change slicing, non-interactive `plan`/`apply`, and file classification.
- `internal/review` runs dimension-based audits, builds prompts, persists the legacy append-only review ledger, analyzes branches, and supports content-stable review findings.
- `internal/store` persists units, runs, findings, commit indexes, decisions, and blob indexes in `<git-common-dir>/vas-sentinel`. Blob indexes preserve review coverage across rebases when file content is unchanged.
- `internal/gate` runs deterministic validation followed by semantic review of `HEAD`; it reports validation, review, or infrastructure status.
- `internal/change`, `internal/risk`, and `internal/graph` profile a change, calculate cohesion and risk, and optionally enrich review context from CodeGraph metadata tied to the audited commit.
- `internal/consent` records local consent for externally supplied diffs.
- `internal/ops` records, rotates, purges, and reads events from the repository common directory.
- `internal/setup` installs, upgrades, and removes the binary and manages configuration templates.

Durable-run roadmap work (`sentinel runs`, R0-R11, A units, or D units) must load `.claude/skills/durable-runs-implementation/SKILL.md` before implementation or verification.

Reference documents: [`docs/arquitectura/replanteamiento-objetivo.md`](docs/arquitectura/replanteamiento-objetivo.md) and [`docs/reingenieria/`](docs/reingenieria/).

## Commands

| Command | Purpose |
|---|---|
| `version` | Print the installed version. |
| `help` | Print command help. |
| `init` | Inject the volume rule, create project configuration, and install `pre-commit`. |
| `uninit` | Revert `init` for this repository. |
| `check` | Measure added authored code lines in the whole worktree. The result is advisory, including at `CRITICO`. |
| `check --staged` | Enforce the 400-line review budget for the staged commit candidate. |
| `slice` | Interactively split changes into reviewable commits. |
| `slice plan` | Propose reviewable selections without committing. `--json` exits `3` when decisions are pending. |
| `slice apply` | Apply approved selections with `--plan` and `--answers`. |
| `review` | Audit a commit by dimension and save its review record. |
| `gate` | Validate then semantically review `HEAD`; requires `--stage pre-commit|pre-push|pr` and accepts `--profile`. |
| `lint` | Run configured `lint_commands`. |
| `rebase` | Fetch and rebase against upstream after confirmation. |
| `status` | Show volume, audit records, and recent events. Supports `--json` and `--prune`. |
| `explain` | Explain a commit range's change profile, detected characteristics, risk, and cohesion. Supports `--json`. |
| `consentimiento-diff` | Grant, revoke, or show local consent for external diffs. |
| `pr` | Create a pull request through `gh`; `pr review` analyzes the unpublished branch. |
| `install` / `upgrade` / `uninstall` | Manage the installed binary. |

Commands that accept no flags reject extra arguments with exit code `1`.

## Key business rules

- `check`: measures the whole worktree. 200 to 400 authored code lines is `PUNTO_OPTIMO`; more than 400 is `CRITICO`, but a successful measurement remains advisory and exits with `0`. Measurement failures exit with `1`. The exact state string is `"CRITICO"` without an accent because `main.go` compares it literally.
- `check --staged`: measures only the staged commit candidate and rejects authored code over 400 lines with exit `1`; cohesion warnings are read-only.
- `slice`: produces reviewable selections and commits them only through the explicit plan/apply flow. It generates commit messages through the configured adapter and has deterministic fallback messages if the adapter is unavailable.
- Oversized files: configuration files over 400 lines are isolated with `chore(deps): track lock and auto-generated files`. Code files over 500 lines require an explicit bypass decision; rejection aborts without creating commits.
- Slice commits use `--no-verify`. The slice flow is the guardian's controlled exemption: approved selections are limited to 400 authored lines unless the user explicitly approves a massive-file bypass.
- `review`: audits `logic`, `style`, `design`, `tests`, `security`, and `spec` independently. Records append-only revisions and the effective responding agent, rather than only the requested profile.
- Findings v2 use stable fingerprints and content blobs. The store can recognize content already reviewed under a different commit SHA after a rebase.
- `gate`: loads project configuration strictly, validates the selected `validation.profiles` profile, then audits `HEAD`. `--stage` identifies lifecycle context; `--profile` selects validation, not review, configuration.
- `explain`: analyzes a `<base>..<head>` range, detects change characteristics, evaluates risk, and suggests a split when cohesion warrants it.
- `pr review`: chooses single versus chained review using `review.LimiteDecisionChain`, which equals the guardian limit of 400 lines. Configured lint, test, and build commands run deterministically without consulting an agent.
- `init`: runs only from a Git worktree root, redirects there when invoked from a subdirectory, writes the project configuration, injects the marked guardian rule into agent instruction files, and installs the repository-local common-dir hook that enforces staged volume.

## Configuration

- `active_agent` defaults to `auto`. `agents` defines model, reasoning effort, and nested profiles. `auto` tries configured agents in PATH order.
- `commit_language` controls messages generated by `slice`. Set it to `en` for new and migrated configurations.
- `review` sets timeout, parallelism, and dimension profiles. `lint_commands`, `test_commands`, and `build_commands` enable deterministic verification in `pr review`.
- `validation.profiles` defines gate validation profiles. `gate --profile` defaults to `standard` and fails explicitly if it is absent.
- `internal/git/umbrales.go` is the sole threshold source: `LimiteLineasRevisables` is 400 and governs the guardian, slicing, and `review.LimiteDecisionChain`; `LimiteCodigoGigante` is 500.
- `MY_SUB_AGENT` remains an optional override, not the primary configuration path.

<!-- vas-sentinel:begin -->
## CRITICAL VOLUME RULE (THE GUARDIAN)
- Before making changes or proposing a plan, run `sentinel check`. It measures the whole worktree and is advisory, including when the state is `CRITICO`.
- The repository's `pre-commit` hook runs `sentinel check --staged`. This is the enforcement boundary: it rejects staged authored code over the 400-line review budget.
- When the worktree check is `CRITICO`, run `sentinel slice plan --json` to produce reviewable selections without committing.
- After the user answers every pending decision, apply the approved selections with `sentinel slice apply --plan plan.json --answers answers.json`. Never answer those decisions on the user's behalf.
<!-- vas-sentinel:end -->
