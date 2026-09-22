# Feature: Install the vcSentinel agent skill

## Goal
Install a repository-local `.agents/skills/vcsentinel/SKILL.md` during `vcsentinel init` so coding agents know when and how to use vcSentinel.

## Decisions
- Target path: `.agents/skills/vcsentinel/SKILL.md`.
- The skill is repository-local and versionable.
- `uninit` removes it only when it is still the vcSentinel-owned file.
- The skill covers the complete operational workflow, not only volume checks.
- New artifacts are written in English, per repository policy.

## Tasks

- [x] Define the canonical skill content and ownership marker.
- [x] Install the skill idempotently from `init` and report failures.
- [x] Remove the owned skill safely from `uninit`.
- [x] Add tests for creation, idempotency, preservation, and cleanup.
- [x] Run focused verification and review the resulting diff.

## Non-goals
- Installing global agent skills.
- Changing existing agent instruction files beyond the current volume-rule block.
- Automatically committing or publishing the change.
