# Upgrade E2E hardening

Status: complete

## Goal

Exercise vcSentinel's real upgrade boundary instead of only same-filesystem replacement helpers. Reproduce the cross-device deployment failure, stage downloads on the destination filesystem, and establish a reusable expectation for CLI-facing end-to-end coverage.

## Evidence before implementation

- `vcsentinel check` exits 0 and the worktree is clean.
- `vcsentinel upgrade` failed while replacing `/usr/local/bin/vcsentinel` with `rename /tmp/vcsentinel-upgrade-...: invalid cross-device link`.
- This host reports `/tmp` on tmpfs and `/usr/local/bin` on the root ext4 filesystem.
- Existing setup tests use `t.TempDir()` for both source and destination, so they cannot detect cross-device rename failures.
- Existing durable-run E2E tests exercise real stores/controllers but do not cover the install/upgrade CLI boundary.

## Scope

Owned paths:

- `internal/setup/upgrade.go`
- `internal/setup/upgrade_test.go` or a focused setup E2E test file
- `internal/setup/github.go` only if required to make the real download seam testable
- `odd/tasks/upgrade-e2e-hardening.md`

Do not touch the installed `/usr/local/bin/vcsentinel`, the live Git hook, release assets, or unrelated repository files.

## Acceptance matrix

| ID | Criterion | Evidence | Owner | Status |
|---|---|---|---|---|
| C-01 | A red-capable test reproduces upgrade replacement across distinct filesystems without touching the real installation. | `go test ./internal/setup -run '^TestUpgradeFromGitHubAcrossFilesystems$' -count=1 -v` exited 1 before the fix with `invalid cross-device link`; the same test passed after the fix. | Writer | met |
| C-02 | Upgrade stages the downloaded binary on the destination filesystem and replaces/verifies it successfully. | Destination-side temporary staging in `internal/setup/upgrade.go`; focused E2E passed in the writer worktree. | Writer | met |
| C-03 | Existing Linux/Windows replacement and configuration-preservation behavior remains intact. | `go test ./internal/setup -count=1`, focused race tests, and `go test ./...` passed; `go vet ./...` passed. | Writer/coordinator | met |
| C-04 | At least one real CLI boundary smoke test runs against an isolated environment, and remaining E2E gaps are recorded rather than assumed covered. | Built `/tmp/vcsentinel-upgrade-cli-smoke`; `version`, `check`, `doctor`, and `gate --stage pre-push` all exited 0. The test does not invoke real `upgrade`; the release/replace path is covered by the isolated setup E2E. | Coordinator | met |
| C-05 | Full relevant verification passes and unrelated worktree state remains untouched. | `go test ./...`, `go vet ./...`, build, CLI smoke checks, and `git diff --check` exited 0. Only the two setup files and this task document are dirty. | Coordinator | met |

## Tasks

1. Map the upgrade path and existing E2E seams. **Done:** cross-device cause reproduced; current coverage gap documented.
2. Add a failing distinct-filesystem upgrade integration test. **Done:** the test fails with `invalid cross-device link` before the production change and passes after it.
3. Fix download staging and run focused green checks. **Done:** destination-directory staging is implemented; focused E2E, setup tests, vet, race checks, formatting, and diff checks passed.
4. Run the broader suite and a real CLI smoke test; record remaining E2E gaps. **Done:** full Go tests/vet/build and isolated CLI `version`, `check`, `doctor`, and `gate --stage pre-push` passed.

## Outcome

The direct release upgrade now creates its temporary download beside the running binary, so the atomic rename stays on one filesystem. The E2E reproduces the old `/tmp` to `/dev/shm` cross-device failure before the fix and verifies replacement, executable verification, and cleanup after the fix.

## Remaining E2E gaps

- The CLI smoke run intentionally did not invoke `upgrade` against the real installation. A future CLI-process test could add an explicit injectable release endpoint and a disposable executable target, but that is outside this focused fix.
- The Linux cross-filesystem E2E skips on platforms without distinct writable `/tmp` and `/dev/shm` mounts.

## Non-goals

- Do not retry the real `vcsentinel upgrade` against `/usr/local/bin` during tests.
- Do not use root privileges, mutate release infrastructure, or fake a passing upgrade by bypassing verification.
- Do not claim the whole CLI is end-to-end covered from one upgrade test.
