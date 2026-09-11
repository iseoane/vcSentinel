# VAS Sentinel — Worktree Guardian for AI agents

A deterministic local guardian in Go that prevents massive change accumulation in Git worktrees operated by AI assistants. A single binary that behaves identically on **Windows** and **Debian Linux**.

## Quick path

1. Build: `build.bat` (Windows) or `./build.sh` (Debian) → `bin/<version>/sentinel(.exe)`
2. Set up: `sentinel init` → creates `.vas_sentinel/vassentinel.yml` and installs the `pre-commit` hook in this repository (not global: it does not affect your other repos)
3. Work: run `sentinel check` before any change. If it says **CRITICAL** (>400 lines), stop and run `sentinel slice`.

## Commands

| Command | What it does |
|---|---|
| `sentinel check` | Audits the worktree's line volume: `SMALL`, `OPTIMAL_POINT` (200–400) or `CRITICAL` (>400). It is advisory and exits 0; the limit is enforced by `sentinel check --staged`, which rejects more than 400 authored lines in the stage with exit 1. |
| `sentinel slice` | Splits pending changes into layered micro-commits with a plan you must approve before committing. |
| `sentinel slice plan` | Proposes the plan **without committing anything**. With `--json` it emits the full plan; exit 3 if there are decisions only you can answer. |
| `sentinel slice apply` | Executes an already-approved plan: `--plan plan.json --answers answers.json`. |
| `sentinel init` | Injects the volume rule into your agents' prompts, creates the per-project configuration, and installs the `pre-commit` hook **in this repository** (in its common-dir, not a global folder: it does not affect your other repos). |
| `sentinel uninit` | Reverts `init` in this repository: removes the volume rule, deletes the per-project config, and removes the hook (only if it is still the one VAS Sentinel installed). |
| `sentinel install` / `sentinel upgrade` | Installs or updates the binary from the latest GitHub release, falling back to `go install` if the release is not available. |
| `sentinel review` | Audits a commit (default HEAD) against the dimensions of its bundle and saves the record to the ledger. Flags: `<sha\|HEAD~n>` `--dims a,b` `--all` `--chain` `--gate` `--profile X` `--answer "..."` `--timeout N` `--prune` `--json`. `--timeout` overrides `review.timeout` only for that invocation (seconds). |
| `sentinel refute` | Record an evidence-bound human refutation of one reviewed finding (clears only its block). Usage: `--sha SHA --fingerprint FP --reason TEXT --line-start N --line-end M`. |
| `sentinel accept` | Record a human acceptance of one reviewed finding (documents judgement, never clears the block). Usage: `--sha SHA --fingerprint FP --reason TEXT`. |
| `sentinel reopen` | Record an evidence-bound human reopen of one cleared finding (blocks again). Usage: `--sha SHA --fingerprint FP --reason TEXT --line-start N --line-end M`. |
| `sentinel lint` | Runs the commands defined in `lint_commands` of the configuration. |
| `sentinel rebase` | Updates the branch with `fetch` + `rebase` against its upstream (asks for confirmation). |
| `sentinel status` | Guardian summary: volume, audit records, and recent events. With `--json` it emits JSON; with `--prune` it deletes orphaned records. |
| `sentinel metrics` | Prints deterministic local aggregates from the durable store (duration, success and failure). Reads only the local Git common directory. Unknown measurements render as `null`, never as zero, and unreadable evidence is an error rather than an empty store. Cost, tokens and scope stay unknown until an adapter reports them. Flag: `--json`. |
| `sentinel doctor` | Preflight the local environment a review depends on (agents, search binary, codegraph gates, hook). Advisory, exits 0 like `check`. Flag: `--check-updates`. |
| `sentinel pr` | Pull-request operations: `sentinel pr review` analyzes an unpublished branch, while `sentinel pr create` publishes through `gh`. The legacy `sentinel pr [gh arguments]` passthrough is removed. |
| `sentinel pr review` | Analyzes the unpublished branch: net audit, existing record matrix, unaudited-commit report, and single/chain decision. Flags: `--base X` `--parent X` `--overview` `--json`. |
| `sentinel explain` | Explain the change profile, detected characteristics, risk, and cohesion of a commit range. Usage: `[<base>..<head>] [--json]`. |
| `sentinel consent-diff` | Manage the local per-user consent to expose diffs to external agents (required before slice can generate commit messages through an agent). Usage: `grant\|revoke\|status`. |
| `sentinel tui` | Open the full-screen control center over the repository registry snapshot, refreshed live while the session is open. |
| `sentinel uninstall` | Removes the binary and the global configuration (`~/.vas_sentinel/`). |
| `sentinel version` / `sentinel --version` | Prints the installed version. |
| `sentinel help` / `sentinel --help` | Prints the full help. |

## Slice: planned splitting

`sentinel slice` does not commit blindly. Full flow:

1. **Plan** — groups pending changes by layer (`config → backend → frontend → test`) into batches of ≤400 lines.
2. **Messages** — generates each batch's message with your configured agent. If the agent does not respond, choose among deterministic automatic messages, another available agent, or cancel.
3. **Approval** — shows the full plan and waits for your decision: **(A)pprove all**, **(R)egenerate** a message with another agent, **(E)dit** a message manually, or **(C)ancel**. Enter approves.
4. **Summary** — lists the created commits and verifies the worktree was left clean.

### Agent-driven flow

The dialogue above is a REPL over `stdin`: an agent cannot drive it. That is what the two-step path is for, and it **does not change who decides** — the decision remains yours; only the transport of the question changes:

```bash
sentinel slice plan --json > plan.json   # proposes; commits nothing
# exit 0 → there is nothing to ask
# exit 3 → the plan carries pending_decisions that you must answer
sentinel slice apply --plan plan.json --answers answers.json
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

## Optional context with CodeGraph

With `review.codegraph_context: true`, local consent for external diff,
a clean `.codegraph/` index and the upstream CLI on `PATH`, Sentinel can
add optional metadata about affected test paths to the semantic review.
This is untrusted advisory data: it may inform the review, but it never
authorizes validation scope and is omitted under any uncertainty. No
source output from CodeGraph is sent. Sentinel neither indexes nor
administers CodeGraph: [CodeGraph](https://github.com/colbymchenry/codegraph).

Binaries are generated in `bin/<version>/` (never committed; they are in `.gitignore`). The version is read from `release.yml` (the project's source of truth) and you can force it with the `SENTINEL_VERSION` variable:

```bash
# Windows: produces bin\0.1.0\sentinel.exe
build.bat

# Debian/Linux: produces bin/0.1.0/sentinel
chmod +x build.sh
./build.sh

# Optional version override (ignores the one in release.yml)
export SENTINEL_VERSION=1.2.0
./build.sh
```

Both scripts run `gofmt -w .` → `go vet ./...` → `go build -ldflags="-s -w -X main.version=<version>"`.

Quick verification of a change: `go build ./... && go vet ./...`

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
go install github.com/ISeoane-Quental/vas.sentinel/cmd/sentinel@latest
```

This builds the binary into `$(go env GOPATH)/bin`. Make sure that folder is on your `PATH` and verify with `sentinel --version`.

> **Private repo:** configure `GOPRIVATE` and git credentials first:
> `go env -w GOPRIVATE=github.com/ISeoane-Quental/*`

### Option B — With sentinel itself

`sentinel install` downloads the latest published release and installs it globally:

- **Windows:** copies the binary to `~/.vas_sentinel/bin/` and adds it to the user PATH.
- **Debian/Linux:** installs to `/usr/local/bin/sentinel` (retries with `sudo`) and leaves the path in `~/.zshrc` / `~/.bashrc`.

> **Private repos:** `sentinel install` / `upgrade` resolve the token in this order: the `GITHUB_TOKEN` variable, the `gh` session token (`gh auth token`), or an interactive prompt. You do not need to export anything if you already have `gh` authenticated.

> **Fallback to go install:** if the release is not available (network, token, or missing asset), `install`/`upgrade` automatically retry with `go install github.com/ISeoane-Quental/vas.sentinel/cmd/sentinel@latest` and leave the binary in the same destination.

## Automatic updates

`sentinel upgrade` downloads the latest release and replaces the current binary. On Windows the running binary is locked, so the current one is renamed to `.old` as a backup during the operation.

## Uninstall

`sentinel uninstall` removes the installed binary (both `~/.vas_sentinel/bin/sentinel` on Windows and `/usr/local/bin/sentinel` on Linux, plus any copy inside `GOPATH/bin`) and deletes the global configuration `~/.vas_sentinel/`.

## Asset naming convention

Each release must publish assets with this convention so that `install`/`upgrade` find the right binary:

- `sentinel-windows-amd64.exe`
- `sentinel-linux-amd64`
- `sentinel-linux-arm64` (if published)

## Configuration (Adapter Control)

Configuration is looked up in this order (the first one defining a field wins):

1. **Per-project:** `.vas_sentinel/vassentinel.yml` — created by `sentinel init`
2. **Global:** `~/.vas_sentinel/vassentinel.yml` — created by `sentinel install`
3. Built-in **defaults**

`active_agent` (default `auto`) selects the agent. In `auto`, the preference
follows the **order in which the agents are declared in the yml** (not
alphabetical): the first one whose binary is on the PATH is used first and,
if it fails on a request, the next one is tried in that same order
(per-request chain fallback, never cached). You can also set
`active_agent: "claude"` or `"opencode"` for a concrete agent.
`MY_SUB_AGENT` still exists as a temporary terminal override, but it is no
longer the primary mechanism.

Each agent defines its model and base effort, plus nested **profiles**
(`cheap`, `normal`, `deep`…) that override model and/or effort. The audit
dimensions (`review.dims`) map each dimension to a profile with two
syntaxes: `agent.profile` (explicit agent) or just `profile` (applies to
the `active_agent`: that agent's profile if concrete, or the auto chain in
yml order with fallback).

Example of `.vas_sentinel/vassentinel.yml`:

```yaml
version: "2.0"
active_agent: "auto"
agents:
  claude:
    model: "claude-5-sonnet"
    reasoning_effort: "high"
    profiles:
      cheap:  { model: "claude-5-sonnet", reasoning_effort: "low" }
      normal: { model: "claude-5-sonnet", reasoning_effort: "high" }
      deep:   { model: "claude-opus",     reasoning_effort: "high" }
  opencode:
    model: "deepseek-v4-flash-free"
    reasoning_effort: "max"
    profiles:
      cheap:  { model: "deepseek-v4-flash-free", reasoning_effort: "default" }
      normal: { model: "deepseek-v4-flash-free", reasoning_effort: "high" }
      deep:   { model: "deepseek-v4-flash-free", reasoning_effort: "max" }
review:
  timeout: 600
  parallel: 2
  # Evidence admission over durable runs (default true). Set false only to
  # roll back to unverified acceptance; runs stay inspectable via
  # `sentinel runs`.
  evidence_admission: true
  # Bounded cancellation escalation for owned provider trees (default true).
  # Set false to keep cooperative cancellation while never signaling beyond
  # the direct child.
  cancellation_escalation: true
  dims:
    spec: "opencode.cheap"
    style: "opencode.cheap"
    tests: "opencode.normal"
    logic: "opencode.normal"
    design: "deep"
    security: "deep"
```

Note: the `gate.durable_runs` and `review.durable_runs` keys were removed
(R11). `sentinel gate` always executes as ONE root durable run with validation
and review logical jobs under it, and every reviewer call routes through the
durable transport. A yaml still carrying the removed keys fails to load with
an explicit unknown-key error; delete those keys when migrating.
