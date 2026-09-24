# PR evidence convergence

Status: in progress — the public review/evidence/review/create reproducer is red until the evidence contract is corrected.

## Goal

Make the documented `pr review → commit evidence → re-review → pr create` workflow converge in two rounds without weakening stale-review protection or exact evidence validation.

## Confirmed failure

The black-box `internal/cli_e2e/TestCLIPublicPRCreateE2E` runs the real public binary, commits exactly the first generated `.vcsentinel/evidence` file, runs public `pr review` again, and then invokes `pr create`. The second review rewrites the evidence because the publication body embeds the current branch/head attestation. `pr create` exits 1 with `The pr review evidence is not committed: .vcsentinel/evidence/.../pr-review.log`; fake `gh` is not invoked.

## Contract decision

Keep `PRReviewEntry.HeadSHA`, body attestation validation, and `EvidenceAtHEAD` exact-byte checks authoritative. Separate the immutable deterministic review receipt written to evidence from the publication body whose attestation is bound to the current HEAD. `AnalyzeBranch` must keep the public current HEAD separate from a semantic range: only a contiguous terminal suffix of commits whose changed paths are generated evidence may be excluded from semantic audit/net review. The receipt must contain the stable semantic range (branch, semantic base/head, and semantic SHAs) while excluding current-head/evidence-only identity, timestamps, body text, and model output.

## Tasks

1. Add NUL-safe changed-path inspection and strict generated-evidence path recognition; split `BranchResult` into public current-head and semantic-range fields, rejecting non-terminal evidence-only commits.
2. Keep NetReview and branch audit inputs on the semantic range, while retaining current HEAD for the persisted entry and attestation.
3. Add a deterministic receipt renderer and focused tests proving unchanged semantic inputs produce byte-identical receipts across evidence-only HEAD changes.
4. Make `pr review` write the receipt as evidence while retaining the current-head-bound body in the persisted PR review entry.
5. Run the black-box convergence E2E and preserve stale/evidence refusal coverage.
6. Update the governing piece-4/piece-5 documentation to describe semantic range, generated evidence commits, and receipt versus publication body.
7. Run focused review tests, the CLI E2E package, full Go tests, vet, build, race checks, and diff checks; record any unrelated failures separately.

## Acceptance

- The existing black-box convergence reproducer passes and fake `gh` receives the composed body.
- Evidence is committed after the first review, remains byte-identical after the second review, and the worktree is clean before `pr create`.
- Changing the reviewed branch after review still causes the documented stale-review refusal.
- Editing or staging evidence after review still fails exact evidence validation.
- No network, real GitHub, system installation path, or real agent is used by tests.
- Production changes and tests remain in reviewable work units; no delivery action is implied.

## Non-goals

- Positive install/upgrade/uninstall E2Es; those require separate endpoint, destination, and PATH seams.
- Relaxing `EvidenceAtHEAD`, accepting stale entries, or canonicalizing evidence at publication time.
