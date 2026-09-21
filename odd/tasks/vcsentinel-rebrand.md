# vcSentinel Rebrand

## Goal

Rename the entire product from VAS Sentinel and its technical variants to vcSentinel, using `vcSentinel` for the brand, `vcsentinel` for technical names, and `github.com/ISeoane-Quental/vcSentinel` as the canonical Go module and repository URL.

## Decisions

- Scope: complete product rename.
- Compatibility: clean cut; legacy names, paths, variables, binaries, and configuration are not supported.
- Generated and historical artifacts: regenerate generated artifacts; preserve Git internals and immutable historical evidence unless they are active product-facing source documents.
- Engram: migrate the active project identity and retain a recovery summary under the new project.
- Delivery strategy: `ask-on-risk`.
- Chain strategy: `feature-branch-chain`.
- Commit authorization: granted for the current session; push, PR creation, repository rename, and other external publishing remain separate decisions.
- TDD mode: not explicitly enabled for this rename. Use ordinary functional verification and update exact-output tests with behavior.
- Expected authored change: above 400 lines because command names, module imports, path contracts, tests, scripts, and documentation all change.

## Tasks

- [x] T1 — Rename the Go module, command tree, executable identity, imports, and release/build wiring.
  - Route: delegated writer; multi-file write trigger.
  - Verification: `go test ./cmd/vcsentinel ./tools/release`, `go build ./...`, `go test ./internal/setup`, focused `internal/git` tests, and scoped `git diff --check` passed; independent verification found and the writer corrected stale setup expectations and recovery guidance.
  - Evidence: slice commit range `61979cb..e554288` (nine guardian-created commits; core semantic unit `b1aef16`).
- [ ] T2 — Rename configuration, environment variables, persistent storage, daemon/runtime paths, markers, hooks, and setup behavior with a clean cut.
  - Route: delegated writer; multi-file write trigger.
  - Verification: focused config/setup/store/daemon/ops/graph/reviewsnapshot/CLI tests, `go test ./...`, `go build ./...`, `go vet ./...`, `git diff --check`, fixture checks, and independent residual audit passed.
  - Evidence: implementation verified; guardian commit pending.
- [ ] T3 — Update tests, fixtures, generated golden files, and exact-output assertions for the new identity.
  - Route: delegated writer; multi-file write trigger.
  - Verification: focused package tests and golden regeneration where required.
  - Evidence: pending.
- [ ] T4 — Update active documentation, project instructions, skills, examples, and repository-facing metadata while preserving immutable historical evidence.
  - Route: delegated writer; multi-file write trigger.
  - Verification: residual-reference audit and documentation checks.
  - Evidence: pending.
- [ ] T5 — Verify the complete repository and audit every remaining legacy-name match against an explicit allowlist.
  - Route: delegated verifier; verification trigger.
  - Verification: `go list ./...`, `go build ./...`, `go vet ./...`, `go test ./...`, `./build.sh`, and residual-reference searches.
  - Evidence: pending.
- [ ] T6 — Rename external repository identity and migrate Engram project identity after the source tree is coherent.
  - Route: parent-controlled external operations requiring explicit confirmation before irreversible/publishing changes.
  - Verification: remote URL, active Engram project, and recovery-context checks.
  - Evidence: pending.

## Progress Log

- Repository-wide read-only mapping completed.
- User selected native naming conventions and a clean-cut migration.
- Feature branch created: `feat/vcsentinel-rebrand`.
- User selected `feature-branch-chain` delivery and authorized commits for this session.
- T1 closed through the guardian slice flow; plan and answers transport artifacts were kept outside the worktree to avoid self-inclusion drift.
