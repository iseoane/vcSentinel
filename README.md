# vcSentinel — Worktree Guardian for AI agents

A deterministic local guardian in Go that prevents massive change accumulation in Git worktrees operated by AI assistants. A single binary that behaves identically on **Windows** and **Debian Linux**.

## Prerequisites

- Git, for repository setup and worktree measurements.
- Go 1.26.5 or newer, only when building from source or bootstrapping with `go install`.
- A configured agent binary on `PATH` for agent-backed review and generated commit messages.
- `gh` authenticated with GitHub for pull-request publication and releases.

## Quick path

1. Build with `build.bat` (Windows) or `./build.sh` (Debian/Linux), or install a published release.
2. Run `vcsentinel init` from the repository. It creates `.vcsentinel/vcsentinel.yml` and installs the repository-local `pre-commit` hook.
3. Configure a validation profile if you will use `vcsentinel gate`.
4. Run `vcsentinel check` before making changes. If it reports **CRITICAL** (>400 lines), use the non-interactive `slice plan` / `slice apply` flow for agent-driven work.

## Commands

| Command | What it does |
|---|---|
| `vcsentinel check` | Measures worktree line volume: `SMALL`, `OPTIMAL_POINT` (200–400), or `CRITICAL` (>400). It is advisory; `check --staged` enforces the 400-line limit for a staged candidate. |
| `vcsentinel slice` | Interactive flow for splitting pending changes into reviewable commits. |
| `vcsentinel slice plan` | Proposes selections without committing. With `--json`, exit 3 means that explicit human decisions are required. |
| `vcsentinel slice apply` | Applies an approved plan with `--plan plan.json --answers answers.json`. |
| `vcsentinel init` | Adds managed guidance, creates project configuration, and installs the repository-local `pre-commit` check. |
| `vcsentinel uninit` | Removes vcSentinel's managed guidance, project configuration, skill, and hook when they are still owned by vcSentinel. |
| `vcsentinel install` / `vcsentinel upgrade` | Installs or updates the binary from the latest GitHub release, falling back to `go install` when necessary. |
| `vcsentinel review` | Audits a commit (default `HEAD`) against its review dimensions and saves the record. Use `--gate` to fail on a critical finding. |
| `vcsentinel refute` | Records evidence that one reviewed finding is invalid and clears only that finding's block. |
| `vcsentinel accept` | Records human acceptance of one reviewed finding; it documents the decision but does not clear the block. |
| `vcsentinel reopen` | Reopens a previously cleared finding with new evidence. |
| `vcsentinel gate --stage pre-commit\|pre-push\|pr [--profile X]` | Runs the configured validation profile deterministically, without an agent. It does not review code quality; `review` owns semantic per-commit audits. |
| `vcsentinel lint` | Runs the configured `lint_commands`. |
| `vcsentinel rebase` | Fetches and rebases against the configured upstream after confirmation. |
| `vcsentinel status` | Shows volume, review records, and recent events; supports `--json` and `--prune`. |
| `vcsentinel metrics` | Prints deterministic local aggregates from the durable store; unknown measurements are `null`. |
| `vcsentinel doctor` | Advises whether configured agents and review tools are ready. It exits 0 and never installs or gates work. |
| `vcsentinel explain` | Explains the change profile, detected characteristics, risk, and cohesion of a commit range. |
| `vcsentinel consent-diff grant\|revoke\|status` | Manages per-user consent to expose diffs to configured external agents. |
| `vcsentinel pr review` | Analyzes the current branch and saves its judgement and evidence without publishing. |
| `vcsentinel pr create` | Publishes a previously saved branch judgement through `gh`; it audits nothing. |
| `vcsentinel runs <subcommand>` | Manages tracked durable tasks: `start`, `status`, `logs`, `respond`, `abort`, `retry`, `recover`, `verify`, `attach`, `daemon`, and `prune`. See [`docs/design/runs-cli.md`](docs/design/runs-cli.md). |
| `vcsentinel tui` | Opens the full-screen control center over registered repositories and tracked tasks. |
| `vcsentinel uninstall` | Removes the user-level installation and global configuration; it does not remove per-repository setup. |
| `vcsentinel version` / `vcsentinel --version` | Prints the installed version. |
| `vcsentinel help` / `vcsentinel --help` | Prints command help. |

## Slice: planned splitting

`vcsentinel slice` does not commit blindly. Full flow:

1. **Plan** — groups pending changes by layer (`config → backend → frontend → test`) into batches of ≤400 lines.
2. **Messages** — generates each batch's message with your configured agent. If the agent does not respond, choose among deterministic automatic messages, another available agent, or cancel.
3. **Approval** — shows the full plan and waits for your decision: **(A)pprove all**, **(R)egenerate** a message with another agent, **(E)dit** a message manually, or **(C)ancel**. Enter approves.
4. **Summary** — lists the created commits and verifies the worktree was left clean.

### Agent-driven flow

The dialogue above is a REPL over `stdin`: an agent cannot drive it. That is what the two-step path is for, and it **does not change who decides** — the decision remains yours; only the transport of the question changes:

```bash
vcsentinel slice plan --json > plan.json   # proposes; commits nothing
# exit 0 → there is nothing to ask
# exit 3 → the plan carries pending_decisions that you must answer
vcsentinel slice apply --plan plan.json --answers answers.json
```

`answers.json` binds the approval to a concrete plan:

```json
{ "plan_id": "<the plan_id of the emitted plan>", "answers": { "<decision id>": "bypass" } }
```

Three bindings that `apply` verifies before creating a single commit:

1. **To the tree** — if the content of any path in the plan changed, it refuses and a replan is required.
2. **To the plan** — the answers carry the `plan_id`; an approval of a previous plan is not valid.
3. **No defaults** — every pending decision demands an explicit answer (`bypass` or `abort`). Not even "approve all" is implicit.

This eliminates the accident of confusing "nobody at the keyboard" with "the human approved". What it does **not** promise is to stop a deliberate agent from calling `git commit` on its own: that is outside the threat model.

Special cases:

- **Giant files:** config over 400 lines is isolated automatically (`chore(deps): track lock and auto-generated files`); code over 500 lines asks for confirmation and makes an explicit bypass (`chore(slice): bypass IA for massive file …`) or aborts without committing anything.
- **Hook and slice:** slice commits skip the hook (`--no-verify`). Slice is the guardian's unlocking mechanism and every batch is already validated; the hook keeps protecting manual commits.

## Build

Binaries are generated in `bin/<version>/` (never committed; they are in `.gitignore`). The version is read from `release.yml` (the project's source of truth), and you can override it with `VCSENTINEL_VERSION`:

```bash
# Windows: produces bin\0.1.0\vcsentinel.exe
build.bat

# Debian/Linux: produces bin/0.1.0/vcsentinel
chmod +x build.sh
./build.sh

# Optional version override (ignores the one in release.yml)
export VCSENTINEL_VERSION=1.2.0
./build.sh
```

Both scripts run `gofmt -w .`, `go vet ./...`, and `go build -ldflags="-s -w -X main.version=<version>"`.

Quick verification of a change: `go build ./... && go vet ./...`

## Optional context with CodeGraph

With `review.codegraph_context: true`, a repository request for external
diff context, local consent, a clean `.codegraph/` index, and the upstream CLI
on `PATH`, vcSentinel can add optional metadata about affected test paths to
semantic review. This is untrusted advisory data: it may inform review, but it
never authorizes validation scope and is omitted under uncertainty. No source
output from CodeGraph is sent. vcSentinel neither indexes nor administers
CodeGraph: [CodeGraph](https://github.com/colbymchenry/codegraph).

## Publishing a release

1. Update `version` in `release.yml` with a version **higher** than the last published one.
2. Publish with the scripts in `infra/` (they run vet, generate the multi-platform assets, and create the release):

```bash
# Windows
infra\release.bat

# Debian/Linux
chmod +x infra/release.sh
./infra/release.sh
```

> `tools/release` queries the latest published release with `gh` and **aborts if the version in `release.yml` is equal or lower**: you must increment it before publishing.

You can also do it manually: `go run ./tools/release` to generate the assets in `bin/<version>/` and then `gh release create v<version> bin/<version>/*`.

## Installation

### Option A — Bootstrap with Go (you do not need the prior binary)

```bash
go install github.com/ISeoane-Quental/vcSentinel/cmd/vcsentinel@latest
```

This builds the binary into `$(go env GOPATH)/bin`. Make sure that folder is on your `PATH` and verify with `vcsentinel --version`.

> **Private repo:** configure `GOPRIVATE` and git credentials first:
> `go env -w GOPRIVATE=github.com/ISeoane-Quental/*`

### Option B — With vcsentinel itself

`vcsentinel install` downloads the latest published release and installs it globally:

- **Windows:** copies the binary to `~/.vcsentinel/bin/` and adds it to the user PATH.
- **Debian/Linux:** installs to `/usr/local/bin/vcsentinel` (retries with `sudo`) and leaves the path in `~/.zshrc` / `~/.bashrc`.

> **Private repos:** `vcsentinel install` / `upgrade` resolve the token in this order: the `GITHUB_TOKEN` variable, the `gh` session token (`gh auth token`), or an interactive prompt. You do not need to export anything if you already have `gh` authenticated.

> **Fallback to go install:** if the release is not available (network, token, or missing asset), `install`/`upgrade` automatically retry with `go install github.com/ISeoane-Quental/vcSentinel/cmd/vcsentinel@latest` and leave the binary in the same destination.

## Automatic updates

`vcsentinel upgrade` downloads the latest release and replaces the current binary. On Windows the running binary is locked, so the current one is renamed to `.old` as a backup during the operation.

## Uninstall

`vcsentinel uninstall` removes the binary installed by vcSentinel (`~/.vcsentinel/bin/vcsentinel.exe` on Windows or `/usr/local/bin/vcsentinel` on Debian/Linux) and deletes the global configuration `~/.vcsentinel/`. It does not remove a separate binary installed directly with `go install`; remove that copy from `$(go env GOPATH)/bin` yourself if needed.

## Asset naming convention

Each release must publish assets with this convention so that `install`/`upgrade` find the right binary:

- `vcsentinel-windows-amd64.exe`
- `vcsentinel-linux-amd64`
- `vcsentinel-linux-arm64` (if published)

## Configuration

Configuration is loaded in this order:

1. Built-in defaults.
2. **Global:** `~/.vcsentinel/vcsentinel.yml` — created by `vcsentinel install`.
3. **Per-project:** `.vcsentinel/vcsentinel.yml` — created by `vcsentinel init`.

Later files override earlier scalar and nested fields. Command lists such as
`lint_commands`, `test_commands`, and `build_commands` append across files;
validation capabilities merge by name and a project profile can replace the
list of capabilities for that profile. Unknown keys are rejected by the
strict parser instead of being silently ignored.

`active_agent` (default `auto`) selects the agent. In `auto`, vcSentinel uses
the declaration order in the most specific configuration and chooses the first
agent whose binary is on `PATH`; failed requests fall back to the next agent
in that order for that request. Set `active_agent` to a concrete agent name to
avoid the chain. `MY_SUB_AGENT` remains a temporary terminal override, but it
is not the primary configuration mechanism.

Each agent defines a model and base reasoning effort, plus nested profiles such
as `cheap`, `normal`, and `deep`. Model identifiers in the example below are
illustrative; replace them with identifiers supported by your configured
adapter.

`vcsentinel gate` runs `validation.profiles.standard` by default. Define that
profile, or pass `--profile` with an existing profile. The gate is deterministic
and does not use an agent; `vcsentinel review` owns semantic per-commit audits.

Example of `.vcsentinel/vcsentinel.yml`:

```yaml
version: "2.0"
active_agent: "auto"
commit_language: "en"
request_external_agent_diff: false
agents:
  claude:
    model: "claude-5-sonnet"
    reasoning_effort: "high"
    profiles:
      cheap:  { model: "claude-5-sonnet", reasoning_effort: "low" }
      normal: { model: "claude-5-sonnet", reasoning_effort: "high" }
      deep:   { model: "claude-opus",     reasoning_effort: "high" }
  opencode:
    model: "deepseek-v4-flash"
    reasoning_effort: "max"
    profiles:
      cheap:  { model: "deepseek-v4-flash", reasoning_effort: "low" }
      normal: { model: "deepseek-v4-flash", reasoning_effort: "high" }
      deep:   { model: "deepseek-v4-flash", reasoning_effort: "max" }
review:
  timeout: 900
  parallel: 2
  codegraph_context: false
  evidence_admission: true
  cancellation_escalation: true
validation:
  capabilities:
    format:
      command: "gofmt -l ."
      fails_when: "output_not_empty"
    lint:
      command: "go vet ./..."
    build:
      command: "go build ./..."
    unit_test:
      command: "go test ./..."
      supports_scope: true
      scoped_command: "go test {packages}"
      timeout: 120
  profiles:
    standard: [format, lint, build, unit_test]
  mode: worktree
# Optional GitHub Actions evidence for `vcsentinel pr create`. An empty or
# absent workflow disables CI evidence; vcSentinel never guesses a workflow.
ci:
  workflow: "verify.yml"   # required to enable CI; empty or absent disables it
  wait_seconds: 900        # optional; bound of the non-interactive wait
  poll_seconds: 15         # optional; interval between polls
```

Older configurations using only `lint_commands`, `test_commands`, or
`build_commands` remain supported: when no explicit validation capabilities
exist, vcSentinel translates those lists into validation capabilities.

The removed `gate.durable_runs` and `review.durable_runs` keys are invalid.
A configuration containing them fails with an explicit unknown-key error.
Durable tasks are managed through `vcsentinel runs`; `gate` only runs
validation, while `review` records semantic per-commit findings.
