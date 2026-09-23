# README accuracy refresh

## Goal

Review the root README against the current vcSentinel command surface and implementation, then update the documentation so the primary user path is accurate, scannable, and complete.

## Scope

- In scope: `README.md` and this task record.
- Out of scope: source behavior, linked design documents, generated release assets, and pull requests. Commit and push delivery is handled separately under explicit user authorization.

## Tasks

1. **Audit current README and repository contracts** — done
   - Compared public commands, installation behavior, configuration schema, and validation semantics with repository evidence.
2. **Update README structure and command/configuration guidance** — done
   - Added prerequisites and an agent-safe quick path, documented `gate` and `runs`, repaired the build section, corrected uninstall behavior, fixed configuration precedence, removed unsupported `review.dims`, and added a working validation profile example.
3. **Verify the documentation change** — done
   - Re-read the final README, checked referenced commands and paths, ran focused repository checks, and found no remaining discrepancy within scope.

## Evidence

- Initial whole-worktree check: `go run ./cmd/vcsentinel check` → `SMALL`, 0 added code lines.
- Root README selected because it is the repository's primary user-facing entry point.
- Read-only audit identified missing `gate`/`runs` coverage, stale gate semantics, an incomplete build heading, an incorrect uninstall claim, missing Go prerequisite and validation example, and overly broad configuration precedence wording.

## Verification evidence

- `git diff --check` → exit 0.
- `test -f docs/design/runs-cli.md` → exit 0.
- `go run ./cmd/vcsentinel help gate` → exit 0; confirms deterministic, agent-free validation.
- `go run ./cmd/vcsentinel help runs` → exit 0; confirms documented durable-run subcommands.
- `go test ./cmd/vcsentinel -run 'Test.*Help'` → exit 0.
- Stale-claim inspection found no `review.dims`, semantic gate review, invalid durable-run configuration guidance, or incorrect uninstall claim.

## Delivery

The user subsequently authorized commit and push delivery. This task record is included in the documentation delivery commit; no pull request is being created.
