# Prepare and Publish vcSentinel v1.1.0

## Goal
Bump the repository's release version from `1.0.1` to the next minor, `1.1.0`, and publish the complete GitHub release using `infra/release.sh`.

## Ordered Work Units
1. Set `release.yml` to `1.1.0`, run the Go test suite, and create a reviewable release-version commit on `release/v1.1.0`; fast-forward-push that commit to `origin/main`.
2. Run `sh infra/release.sh` from the updated `main`. The script runs `go vet ./...`, builds configured cross-platform assets, and creates the GitHub release `v1.1.0` with generated release notes.
3. Verify the GitHub release and uploaded assets, synchronize local `main`, clean the temporary release branch, and record delivery evidence here.

## Acceptance Criteria
- `release.yml` records `1.1.0` and the commit is present on `origin/main` before publication.
- `go test ./...`, the release script's `go vet ./...`, and all three configured asset builds pass.
- GitHub release `v1.1.0` exists with the Windows amd64, Linux amd64, and Linux arm64 assets.
- The local worktree is clean on `main`; no commit review is run, as requested.

## Constraints
- Use the repository's documented release script; it publishes a GitHub release and creates the remote tag.
- Do not use agent skills or run semantic/native commit reviews.
- Do not override `VCSENTINEL_VERSION`; `release.yml` is the source of truth.
- Current published release and repository version at task start: `v1.0.1`.

## Status
- Preflight: repository was clean on `main` at `fecc00d`; latest GitHub release is `v1.0.1`; no local or remote `v1.1.0` tag/release exists.
- Release script inspection: `infra/release.sh` runs `go vet ./...`, generates assets using `go run ./tools/release`, and publishes via `gh release create`.
- Verification after the version bump: `go test ./...` passed.
