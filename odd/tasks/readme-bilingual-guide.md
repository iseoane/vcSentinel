# Bilingual README guide

## Objective
Replace the deleted root README with a current English onboarding guide and an explicitly requested Castilian Spanish translation, linking them together and using the existing Archify workflow diagram.

## Scope and constraints
- Authorized paths: `README.md`, `README.es.md`, this task file.
- The Spanish translation is the user's explicit exception to the repository's English-only artifact policy; all other new artifact text stays in English.
- Use current repository implementation and configuration as authority. Never imply that CodeGraph is mandatory or that PR publication performs semantic review.
- Preserve the user's deletion of the prior README as an intentional replacement.
- Keep diagrams unchanged. No automatic push or PR creation.
- Route: delegated writer for the two non-trivial documentation files (multi-file write trigger); parent owns task state and commit.
- TDD: not applicable to documentation-only content; functional verification uses link/file checks and current CLI help or relevant source evidence.
- Delivery strategy: single reviewable documentation commit; HTML diagram remains unchanged.

## Tasks
- [x] R1: Write the English guide and Castilian Spanish translation, with truthful badges, prerequisites, installation/upgrade, optional CodeGraph, multi-commit-to-PR example, configuration and cross-links. Check: independent verifier passed; both docs exist and match current CLI behavior. Commit: pending with R2.
- [ ] R2: Verify links, commands and language consistency, then commit the complete documentation work unit on this feature branch. Check: focused checks and clean committed diff; record commit identity. Commit: pending.

## Acceptance
- README.md links to README.es.md and vice versa.
- Badges identify PolyForm Noncommercial 1.0.0, current vcSentinel release and Go requirement.
- Scenario distinguishes advisory worktree check, staged hook, human slice decisions, per-commit semantic review, deterministic gate, saved `pr review` evidence and `pr create` publication.
- Spanish documentation uses natural Spain Spanish without changing command tokens.

## Progress and evidence
- Existing README deleted by user before work; branch `docs/readme-bilingual-guide` created from synchronized main.
- Existing `docs/diagrams/vcsentinel-multi-commit-pr.html` is a standalone English workflow diagram; its visual-check was skipped because Chrome was unavailable.
- R1 complete: delegated writer replaced `README.md` and authored `README.es.md`. Writer ran `vcsentinel check` (0 code / 717 informational lines), `git diff --check` (pass).
- Independent read-only verifier passed badge, CLI, CodeGraph, configuration, PR behavior and Spanish fidelity; external badge availability not fetched.
- Native risk assessment was unassessable because untracked files require declaration; independent verifier ran per returned high-risk fallback plan.
- Parent spot check: local Markdown links in both documents resolve (9 each), `git diff --check` passes; no visual inspection (Chrome unavailable).
- R2 in progress.

## Next step
Commit the verified documentation work unit and record its identity.
