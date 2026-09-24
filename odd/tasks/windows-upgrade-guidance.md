# Windows upgrade guidance

Status: complete — Windows manual-upgrade guidance is implemented, independently verified, and committed in reviewable work units.

## Goal

Make `vcsentinel upgrade` truthful and safe on Windows. The running executable must not attempt to replace itself; instead, the command must explain the platform limitation and provide an exact PowerShell command that the operator can run after vcSentinel exits.

## Accepted design

- On Windows, `upgrade` returns a non-zero result before contacting GitHub or attempting replacement.
- The diagnostic explains that the current executable is locked while it is running.
- The diagnostic includes a PowerShell command using `go install github.com/ISeoane-Quental/vcSentinel/cmd/vcsentinel@latest`.
- The command sets `GOBIN` only for the installation and targets the resolved vcSentinel install root (`%USERPROFILE%\\.vcsentinel\\bin` by default), then restores the caller's previous `GOBIN` value.
- The command is source-based and requires a working Go installation; this is stated explicitly.
- The existing Linux release-asset upgrade remains unchanged.

## Acceptance matrix

| ID | Criterion | Evidence | Status |
|---|---|---|---|
| WU-01 | Windows upgrade never tries to replace the running executable. | `RunUpgradeFromGitHub` returns the manual-upgrade diagnostic before release lookup, token prompting, download, or replacement on Windows. | met |
| WU-02 | The recommendation is an exact, safe command for the managed install root. | Unit tests cover the command, PowerShell apostrophe quoting, `GOBIN` targeting, and restoration semantics. | met |
| WU-03 | Public setup E2E truthfully covers the Windows path. | The public lifecycle asserts the non-zero recommendation, `go install` command, unchanged installed version/config/PATH, and driver-launched uninstall; Linux install→self-upgrade→uninstall remains green. | met |
| WU-04 | User-facing documentation matches the behavior. | Upgrade help and setup/upgrade task documentation state the Windows source-based manual path and native self-replacement limitation. | met |
| WU-05 | Existing behavior remains intact. | Focused tests, full tests, native and Windows-target vet/build checks, targeted race checks, diff check, and volume check pass. | met |

## Tasks

1. [x] Add the Windows manual-upgrade command renderer and fail-fast production path without changing Linux release upgrades.
2. [x] Add unit and public E2E coverage for the recommendation, target path, no-side-effect behavior, and preserved lifecycle cleanup.
3. [x] Update command help and setup/upgrade task documentation, retaining the distinction between source-based manual upgrade and release-asset upgrades.
4. [x] Run independent verification, slice the work into local commits, and leave push as a separate user decision.

## Delivery evidence

- Work-unit commits: `5418f88`, `6a8321d`, and `b7ea177`.
- Verification: focused setup and CLI E2Es, `go test ./...`, `go vet ./...`, `go build ./...`, targeted race tests, Windows-target vet/test compilation, `git diff --check`, and `vcsentinel check` passed.
- Native Windows execution was not available on the Linux host; the Windows process behavior is covered by the public test branch and cross-target compilation. No real `go install`, system installation path, GitHub request, user HOME/PATH, sudo, or live hook was used.

## Non-goals

- No Windows updater process, delayed replacement service, or automatic relaunch.
- No changes to the documented Linux upgrade path.
- No real installation path, GitHub request, user PATH, or Go installation during tests.
- No claim that a Linux runner proves native Windows process behavior.
