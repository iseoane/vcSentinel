# vcSentinel

[![PolyForm Noncommercial License 1.0.0](https://img.shields.io/badge/license-PolyForm%20Noncommercial%201.0.0-6f42c1)](LICENSE)
[![vcSentinel 1.0.1](https://img.shields.io/badge/vcSentinel-1.0.1-2563eb)](release.yml)
[![Go 1.26.5+](https://img.shields.io/badge/Go-1.26.5%2B-00ADD8?logo=go&logoColor=white)](go.mod)

[English](README.md) · [Español](README.es.md)

vcSentinel is a deterministic local Go guardian for Git worktrees operated with
AI agents. It measures accumulated authored-code volume, helps split large
working trees into reviewable commits, installs a repository-local staged
volume check, records per-commit semantic reviews, and keeps deterministic
validation separate from pull-request publication.

It preserves human control: it never answers a pending `slice` decision for
you, `pr review` does not silently audit missing commits, and `pr create` does
not invent a review. The commands are useful without an agent for local
measurement and configured validation; agent-backed review and generated commit
messages need a configured agent.

## Prerequisites

- **Git**, for repository setup, worktree measurements, and commits.
- **Go 1.26.5 or newer**, only for source builds, bootstrapping with `go install`,
  installation fallbacks, or the Windows upgrade path. Downloaded release
  binaries do not need Go at runtime.
- A configured agent binary on `PATH` when using semantic review or asking
  `slice` to generate commit messages. Configure it in
  `.vcsentinel/vcsentinel.yml` or `~/.vcsentinel/vcsentinel.yml`.
- **GitHub CLI (`gh`) is optional.** It is used for automatic GitHub PR
  publication and configured GitHub Actions evidence. Local checks, reviews,
  and validation do not require it. When `gh` is unavailable, `pr create`
  attempts a clipboard fallback instead of claiming that a PR was created.
- **CodeGraph is optional** and only enriches semantic-review context; it is
  never required for validation.

## Quick path

If no vcSentinel binary is installed yet, bootstrap it with Go:

```bash
go install github.com/ISeoane-Quental/vcSentinel/cmd/vcsentinel@latest
vcsentinel --version
```

From the Git repository you want to protect:

```bash
vcsentinel init
vcsentinel check
```

`init` creates the project configuration, installs the repository-local
`pre-commit` check, and adds vcSentinel's managed guidance. The normal
`check` measures the whole worktree and is advisory, including when the result
is `CRITICAL`.

## Install and update

### Bootstrap or install a published release

The Go bootstrap above works without a previous binary and places the program
in `$(go env GOPATH)/bin`; put that directory on `PATH`.

If a vcSentinel binary is already available, this command downloads the latest
published release for the current platform and installs it globally for your
user:

```bash
vcsentinel install
```

It also creates `~/.vcsentinel/vcsentinel.yml` when needed. It does **not**
initialize the current repository; run `vcsentinel init` in every repository
that should use vcSentinel.

The release installer uses `/usr/local/bin/vcsentinel` on Debian/Linux and
`%USERPROFILE%\.vcsentinel\bin\vcsentinel.exe` on Windows, updating the user
`PATH` as needed. On Debian/Linux it may request `sudo` to write
`/usr/local/bin`. If a release download fails, `install` can fall back to
`go install`, which requires Go; `upgrade` does not use that fallback on
Windows. For private repositories, credentials are resolved from
`GITHUB_TOKEN`, an authenticated `gh` session, or an interactive token prompt.

### Upgrade

```bash
vcsentinel upgrade
```

On Debian/Linux this replaces the installed binary from the latest release,
with the same source fallback when the download is unavailable. On Windows the
running `.exe` is locked, so automatic upgrade exits non-zero **before
contacting GitHub or changing the installation**. It prints a copy-pastable
PowerShell command that sets `GOBIN` to the vcSentinel install directory and
runs:

```text
go install github.com/ISeoane-Quental/vcSentinel/cmd/vcsentinel@latest
```

That manual Windows path requires Go. `upgrade` changes only the user-level
installation, not any repository configuration, guidance, skill, or hook.

`vcsentinel uninstall` removes the global installation and global settings. It
does not remove repository setup; use `vcsentinel uninit` in each repository.

## Initialize or remove repository setup

`vcsentinel init` must run inside a Git worktree. When invoked below the
repository root, it redirects to that root and creates or preserves:

- `.vcsentinel/vcsentinel.yml`, the per-project configuration;
- the managed volume guidance in `AGENTS.md`, `CLAUDE.md`, or `.claudecode.md`;
- `.agents/skills/vcsentinel/SKILL.md`, when no foreign or modified skill is
  already there; and
- the repository common-directory `pre-commit` hook.

`vcsentinel uninit` removes only vcSentinel-owned setup and preserves foreign
or modified files. Commands that depend on the per-project configuration —
including `check`, `slice`, `review`, `gate`, and `pr` — require this
initialization first.

## Volume checks and reviewable commits

The volume limit is **400 authored lines** for a staged commit candidate:

| Command | Contract |
|---|---|
| `vcsentinel check` | Measures the whole worktree. `CRITICAL` is advisory and still exits successfully. |
| `vcsentinel check --staged` | Measures only staged changes and exits `1` above 400 authored lines. The installed hook runs this form. |
| `vcsentinel slice` | Interactive stdin flow for grouping pending changes into reviewable commits. |
| `vcsentinel slice plan --json` | Non-interactive proposal; it creates no commits and exits `3` when `pending_decisions` need a human answer. |
| `vcsentinel slice apply --plan plan.json --answers answers.json` | Applies a plan only when its tree and `plan_id` still match and every pending decision has an explicit answer. |

For an agent-driven session, use the plan/apply form rather than the
interactive REPL:

```bash
vcsentinel slice plan --json > plan.json
```

If the plan exits `3`, show its `pending_decisions` to a person. Do not choose
`bypass` or `abort` on the person's behalf. Write the person's literal answers
(the only decision values are `bypass` and `abort`) to `answers.json`:

```json
{
  "plan_id": "<plan_id from plan.json>",
  "answers": {
    "<decision id>": "bypass"
  }
}
```

When there are no pending decisions, use an empty answer map. Then apply the
unchanged plan:

```json
{
  "plan_id": "<plan_id from plan.json>",
  "answers": {}
}
```

```bash
vcsentinel slice apply --plan plan.json --answers answers.json
```

The plan/apply flow is the transport; the human retains the decision. The
apply step refuses stale content, mismatched plan IDs, or unanswered
`pending_decisions`. Oversized files can produce an explicit decision rather
than being silently forced into a commit.

## From several commits to a pull request

The following is a realistic multi-commit path. It keeps the responsibilities
separate: worktree volume, human-controlled slicing, per-commit semantic review,
deterministic validation, saved branch evidence, and publication.

```bash
# Before and during work: advisory whole-worktree measurement.
vcsentinel check

# If the result is CRITICAL, create and answer a plan as described above.
vcsentinel slice plan --json > plan.json
vcsentinel slice apply --plan plan.json --answers answers.json

# Audit each unreviewed commit up to HEAD; --gate exits 1 on a critical finding.
vcsentinel review --all --gate

# Run configured validation without an agent.
vcsentinel gate --stage pr

# Author and save the branch judgement/evidence; this does not publish.
vcsentinel pr review --base main --overview

# Publish only the saved review for the current branch and HEAD.
vcsentinel pr create --base main
```

A compact call tree is:

```text
check (advisory)
└─ slice plan → human answers → slice apply → reviewable commits
   └─ review --all → gate --stage pr
      └─ pr review → saved branch judgement/evidence
         └─ pr create → deterministic validation → gh publication or clipboard fallback
```

The complete English workflow diagram is available at
[`docs/diagrams/vcsentinel-multi-commit-pr.html`](docs/diagrams/vcsentinel-multi-commit-pr.html).

`vcsentinel pr review` saves a branch judgement locally and reports commits
that still need individual `vcsentinel review`; it never publishes. `pr create`
requires that saved entry, the current branch and HEAD, and committed review
evidence. It does not perform semantic review. It runs the configured
validation profile again: red deterministic validation blocks publication
unless a human explicitly supplies `--force --reason "..."`. A semantic
finding is not cleared or re-reviewed by `pr create`.

If `gh` is absent or its publication fails, vcSentinel attempts to copy the
composed body with `clip`, `wl-copy`, or `xclip`. No PR is created in that
fallback; create it manually with the copied body and the temporary template
file. If vcSentinel reports that review evidence is not committed, commit the
paths it names and run `vcsentinel pr review` again because the saved entry is
bound to HEAD.

For a stacked PR, give the parent explicitly when needed:

```bash
vcsentinel pr review --base main --parent feature-a --overview
vcsentinel pr create --base main --parent feature-a --chain-pr
```

## Semantic review, validation, and publication

These commands are intentionally different:

| Stage | Command | What it does not do |
|---|---|---|
| Per-commit semantic review | `vcsentinel review <sha> [--dims ...] [--gate]` | It does not run the deterministic validation profile unless a separate command is used. |
| Deterministic validation | `vcsentinel gate --stage pre-commit\|pre-push\|pr [--profile X]` | It does not audit code quality or use an agent. |
| Branch judgement | `vcsentinel pr review --base main` | It does not publish and does not audit missing commits on your behalf. |
| Publication | `vcsentinel pr create --base main` | It does not author or redo semantic review; it consumes the saved branch entry. |

`pr create` uses GitHub Actions only when an explicit `ci.workflow` is
configured. An absent or empty workflow disables CI evidence; vcSentinel never
guesses a workflow merely because `.github/workflows` exists.

## Configuration

Configuration is loaded with this precedence:

1. Built-in defaults.
2. Global `~/.vcsentinel/vcsentinel.yml`.
3. Per-project `.vcsentinel/vcsentinel.yml`.

The per-project file wins for fields it defines. Command lists such as
`lint_commands`, `test_commands`, and `build_commands` accumulate across the
files. `vcsentinel init` creates the project file; `vcsentinel install` creates
the global file when needed. Strict-loading commands reject unknown YAML keys
instead of ignoring them.

A small project configuration that enables the standard deterministic gate is:

```yaml
version: "1.0"
active_agent: "auto"
commit_language: "en"

review:
  timeout: 900
  parallel: 2
  codegraph_context: false

validation:
  capabilities:
    format:
      command: "gofmt -l ."
      fails_when: "output_not_empty"
    unit_test:
      command: "go test ./..."
    build:
      command: "go build ./..."
  profiles:
    standard: [format, unit_test, build]
  mode: worktree
```

Use a model and binary that your configured agent actually supports; the
example relies on vcSentinel's default agent recipes. The older
`lint_commands`, `test_commands`, and `build_commands` lists remain supported
and are translated into validation capabilities when no explicit capabilities
are declared.

To collect optional GitHub Actions evidence during `pr create`, add an
explicit workflow block:

```yaml
ci:
  workflow: "CI"
  wait_seconds: 900
  poll_seconds: 15
```

The workflow name must exist in GitHub Actions. A configured CI block requires
a GitHub-hosted remote and `gh`; its status is evidence in the PR body, not a
semantic review.

## Optional CodeGraph context

[CodeGraph](https://github.com/colbymchenry/codegraph) is an optional source of
untrusted dependency/path metadata for semantic review. It can improve the
reviewer's context, but it never authorizes validation scope: native Go
analysis remains the only validation-scope authority, and missing CodeGraph
context does not fail the gate.

Enable it only in the versioned per-project file, then grant local consent:

```yaml
request_external_agent_diff: true

review:
  codegraph_context: true
```

```bash
vcsentinel consent-diff grant
vcsentinel doctor
```

All of these gates must hold for context to be used:

1. `review.codegraph_context` is `true`.
2. The per-project file (not only the global file) sets
   `request_external_agent_diff: true`.
3. The current user has a local grant from `vcsentinel consent-diff grant`.
4. The `codegraph` executable is resolvable on `PATH`.
5. A `.codegraph` directory exists under the canonical worktree root.
6. Git resolves `HEAD`, and the worktree is clean.
7. `codegraph status --json` reports an initialized index.
8. Its `projectPath` matches this worktree, `pendingChanges` is zero, and
   `worktreeMismatch` is `null`.

`vcsentinel doctor` reports these checks as `binary`, `index_dir`, `head`,
`worktree_clean`, `index_initialized`, `project_path`, `pending_changes`, and
`worktree_match`. It is advisory, exits `0`, never installs anything, and
renders checks that did not run as `UNKNOWN`. If any CodeGraph gate fails,
review context is omitted or recorded as skipped; deterministic validation is
unchanged.

## Useful commands and caveats

| Command | Use |
|---|---|
| `vcsentinel doctor [--check-updates]` | Inspect configured agents, search/tools, CodeGraph, strict config, and the hook. `--check-updates` performs the optional network check. |
| `vcsentinel status --json` | Read volume, saved reviews, and recent activity in machine-readable form. |
| `vcsentinel metrics --json` | Read local review, remediation, and execution measurements; unknown values remain `null`. |
| `vcsentinel explain HEAD~3..HEAD --json` | Inspect change areas, risk, cohesion, and a suggested split. |
| `vcsentinel lint` | Run configured `lint_commands`; it is separate from `gate` profiles. |
| `vcsentinel runs status --json` | Inspect tracked agent tasks; see [`docs/design/runs-cli.md`](docs/design/runs-cli.md) for the lifecycle and exit codes. |
| `vcsentinel tui` | Open the interactive dashboard for registered repositories and tracked tasks. |
| `vcsentinel consent-diff status` | See the current local permission for sharing small diffs with configured agents. |

Validation, lint, test, and build commands come from your YAML and are run
through the system shell. Treat that configuration as trusted code. Review
records and durable-run data live in vcSentinel's Git common directory, so
linked worktrees share the repository's local records.

## License

vcSentinel is distributed under the [PolyForm Noncommercial License
1.0.0](LICENSE). Commercial use is not permitted by that license.
