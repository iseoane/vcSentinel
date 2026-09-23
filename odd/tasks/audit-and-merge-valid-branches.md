# Feature: Audit and selectively merge remaining branches

## Goal
Review local branches outside `main`, identify fixes still applicable to the rebranded tree, merge only valid changes into `main`, and verify the result.

## Decisions
- `main` is the rebranded source of truth at `427d749`.
- Do not merge old agent or backup branches blindly.
- Preserve branches that are not applicable or whose changes are already represented elsewhere.
- New commits and user-facing artifacts remain in English.

## Tasks

- [x] Audit every local branch outside `main` for applicability and duplication.
- [x] Select and merge only valid fixes into `main`.
- [x] Verify the resulting repository with build, vet, tests, and status checks.
- [ ] Clean only branches/worktrees confirmed obsolete after the audit.

## Candidates
- `agent/pr-review-authors`
- `agent/slice-intent-corrections`
- `agent/slice-intent-legacy-plan-fix`
- `agent/slice-intent-retry-quote`
- `agent/slice-intent-review-fix`
- `backup/pr-review-authors-defdabb`
- `fix/orphan-run-settlement`

## Non-goals
- Blindly merging historical agent or backup branches.
- Reintroducing pre-rebranding `cmd/sentinel` paths.
- Deleting unmerged work before its applicability is understood.
