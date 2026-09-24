# Safe setup seams and public E2E coverage

Status: in progress — setup seams and public lifecycle verification are complete; the remaining task is to record the reviewable work-unit commit.

## Goal

Make the documented setup commands testable through the real public binary without contacting GitHub, mutating system installation paths, changing the operator's home/PATH, invoking real `go install` or `sudo`, or weakening setup validation.

## Context

The public CLI E2E harness already isolates repositories, HOME/USERPROFILE, Git configuration, and PATH. The remaining setup gap is production code that hardcodes the GitHub release API, `/usr/local/bin`, shell PATH text, PowerShell user PATH state, and the running executable's installation target. Existing package tests cover helpers but cannot prove a separately built CLI process reaches the complete setup contract.

## Design decision

Add narrowly scoped, test-only environment overrides with safe defaults:

- `VCSENTINEL_TEST_RELEASE_API_URL`: loopback-only absolute HTTP release API endpoint. The release payload supplies the asset URL; invalid or non-loopback overrides fail closed.
- `VCSENTINEL_TEST_INSTALL_ROOT`: absolute disposable installation directory used to resolve the platform binary path. The default remains `/usr/local/bin` on Unix and `$HOME/.vcsentinel/bin` on Windows.
- `VCSENTINEL_TEST_WINDOWS_PATH_FILE`: absolute disposable file that simulates the Windows user PATH; when absent, production continues to use PowerShell.
- `VCSENTINEL_TEST_DISABLE_GO_INSTALL_FALLBACK=1`: disables fallback for deterministic release-path E2Es; default behavior remains unchanged.

The existing isolated HOME/USERPROFILE remains the home seam. Overrides are not generic user-facing root flags and are accepted only under the explicit `VCSENTINEL_TEST_*` namespace. Release endpoint and path-file overrides are confined to loopback/absolute disposable paths and never silently fall back to live endpoints or system state.

Upgrade verification must fail safely: verify the downloaded staged binary before replacing the current binary, and preserve the old binary when replacement verification fails. This is required before adding a truthful rollback E2E.

## Acceptance matrix

| ID | Criterion | Evidence | Status |
|---|---|---|---|
| S-01 | Release API and asset download are configurable across a public CLI process without live GitHub access. | Loopback HTTP fixture receives the release and asset requests; no external request occurs. | met |
| S-02 | Install, upgrade, and uninstall use an isolated disposable destination while defaults remain unchanged. | Public binary creates/replaces/removes the disposable executable and global config; `/usr/local/bin`, real HOME, and real PATH remain unchanged. | met |
| S-03 | Managed shell/PATH state is isolated and idempotent on supported platforms. | Install does not duplicate its managed entry; uninstall removes only the managed entry and preserves unrelated content. | met |
| S-04 | Release-path failures fail closed without partial side effects. | API/download/missing-asset cases leave destination, config, PATH, and external state unchanged; fallback is disabled by the test environment. | met |
| S-05 | Upgrade verification preserves the old binary on invalid replacement. | The disposable invalid-artifact E2E proves the old executable/configuration remain unchanged and temporary artifacts are removed. | met |
| S-06 | The public setup E2E remains black-box, isolated, deterministic, and bounded. | `internal/cli_e2e/setup_e2e_test.go` runs separately built processes; no internal store fixture or real setup path is used. | met |
| S-07 | Existing behavior and cross-platform compilation remain intact. | Focused setup tests, CLI setup E2Es, full tests, vet, build, race, and diff checks pass. | met |

## Tasks

1. [x] Implement the loopback release endpoint, disposable install-root, Windows PATH-file, and fallback-disable seams with safe default behavior; add unit tests for resolver validation and idempotent PATH handling.
2. [x] Harden upgrade verification/rollback before replacement; add focused invalid-binary and cleanup tests.
3. [x] Add public black-box install → upgrade → uninstall E2Es with release/download fixtures, side-effect assertions, idempotency, and release-path failure coverage.
4. [x] Update the CLI E2E coverage contract and setup hardening notes with the new seam and remaining platform limitations.
5. [ ] Run focused and full verification, then close this task in reviewable commits without pushing.

## Non-goals

- No real GitHub requests, release publication, system installation, user-owned PATH changes, real `go install`, sudo, or Windows registry access.
- No generic production `--root`, endpoint, or PATH command-line flags.
- No installer redesign beyond the minimum rollback guarantee needed to keep a failed upgrade safe.
- No claim that Linux process-level coverage proves native Windows execution; cross-platform behavior remains compile-checked and platform-specific helper tests remain explicit.
