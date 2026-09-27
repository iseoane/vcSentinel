# Fix and Release Cross-Filesystem Self-Upgrade

## Goal
Fix Linux self-upgrade failures across filesystems and deliver the fix in patch release `v1.1.1`.

## Acceptance Matrix
| ID | Criterion | Evidence |
|---|---|---|
| C-01 | Replacing a binary from a different filesystem succeeds by staging in the destination filesystem before atomic rename. | Linux test proves device IDs differ, then asserts replacement content and mode `0755`. |
| C-02 | Source/copy failures happen before replacement and leave the existing binary intact. | `TestReplaceLinuxBinaryCopyErrorLeavesCurrentBinaryIntact` preserves old bytes. |
| C-03 | The GitHub-upgrade E2E test accurately describes destination-filesystem staging. | Test name/comments match behavior; source temp directory remains unused. |
| C-04 | The fix and version `1.1.1` are committed and fast-forward-pushed to `main`. | Slice evidence and remote ref verification. |
| C-05 | GitHub release `v1.1.1` contains all configured Windows/Linux assets. | `gh release view v1.1.1` reports tag and all three asset names. |

## Ordered Work Units
1. Add a behavior-level cross-filesystem regression, capture RED, implement destination-side staging, and correct the misleading E2E test.
2. Run repository tests and required build/vet/race checks.
3. Bump `release.yml` from `1.1.0` to `1.1.1`, create reviewable commits with `slice plan/apply`, and fast-forward-push to `origin/main`.
4. Publish with `sh infra/release.sh`; verify the remote release, tag, and assets.
5. Record final evidence, synchronize local `main`, and remove the temporary release branch.

## Constraints
- Technical artifacts in English.
- Code changes are limited to `internal/setup/upgrade.go`, `internal/setup/upgrade_test.go`, and `internal/setup/upgrade_e2e_test.go`.
- Do not alter user configuration or target a real installed binary during tests; use disposable `/tmp` and `/dev/shm` paths.
- User explicitly authorized committing, pushing to `main`, and publishing patch release `v1.1.1`.
- No semantic/native commit reviews; do not use skills.

## Status
- Diagnosis: Linux `rename(2)` returned EXDEV because `/tmp` and `/usr/local/bin` are separate filesystems. Current GitHub-download code stages beside the executable, but the `go install` fallback passes a GOBIN artifact directly to rename. The old cross-filesystem test set `TMPDIR` while production passed an explicit temp directory, so it did not exercise cross-filesystem replacement.
- Baseline `go run ./cmd/vcsentinel check`: exit 0, `0 [SMALL]` before changes.
- TDD RED: `go test ./internal/setup -run '^TestReplaceLinuxBinaryAcrossFilesystems$' -count=1` exited 1 before production changes with `invalid cross-device link` from `/tmp/.../new` to `/dev/shm/.../vcsentinel`.
- Implementation: `replaceLinuxBinary` copies the source to a temporary file in the destination directory, applies mode `0755`, closes it, then atomically renames it over the target. Staging/copy errors leave the current binary untouched. The prior misleading GitHub test was renamed to describe destination-filesystem staging.
- Focused GREEN: `go test ./internal/setup -run '^TestReplaceLinuxBinaryAcrossFilesystems$' -count=1 -v` passed and the test ran (did not skip); `go test ./internal/setup -run '^TestUpgradeFromGitHubStagesDownloadBesideExecutable$' -count=1 -v` passed.
- Independent verification: `go test ./internal/setup -count=1`, `go vet ./internal/setup`, `go build ./...`, `go vet ./...`, `go test -count=1 ./...`, `go test -count=1 -race ./internal/setup`, `GOOS=windows go test -c -o /tmp/vcsentinel-setup-windows.test.exe ./internal/setup`, `go run ./cmd/vcsentinel check` (140 [SMALL]), and `git diff --check` all passed.
- The verified implementation was transferred to branch `fix/upgrade-cross-device-integration`; the source/test patch matched the dedicated writer worktree byte-for-byte. The temporary writer worktree and branch were removed after transfer.
- Preflight after authorization: latest published release is `v1.1.0`; no local or remote `v1.1.1` tag exists.
- User authorized committing/pushing the fix and publishing `v1.1.1`; `release.yml` is now `1.1.1`.
- Final candidate verification after the version bump: `go test ./...` passed.
- Release-preparation commits: `8e6339a0d2b521b539f241b8244837db9e82277f` (release.yml 1.1.1), `d134e9b0806892a7187e3905557dffec1b2cefd3` (upgrade fix and tests), `25de460b586471b3c0f08dd1248579dbb9753db1` (task evidence). No commit reviews were run, as requested.
