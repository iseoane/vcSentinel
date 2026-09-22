# Feature: Reorder and simplify CLI help

## Goal
Make `vcsentinel --help` follow the user's workflow and make command descriptions understandable without internal implementation vocabulary.

## Decisions
- Keep `version` and `help` first as global entry points.
- Then show exactly this workflow: `check`, `slice`; `review`, `refute`, `accept`, `reopen`; `gate`, `lint`; `explain`; `pr`; `runs`, `tui`; `consent-diff`; `rebase`, `status`, `doctor`; `init`, `uninit`; `install`, `upgrade`, `uninstall`.
- Preserve dedicated `--help` commands and update their wording for clarity and accuracy.
- Keep new user-facing artifacts in English, per repository policy.
- Do not change command behavior, only help wording/order and tests.

## Tasks

- [x] Reorder top-level usage and help rows.
- [x] Simplify and correct per-command help text.
- [x] Update help tests and catch omissions/inaccuracies.
- [x] Run focused verification and inspect the diff.

## Non-goals
- Renaming commands or flags.
- Changing command behavior or exit codes.
- Removing detailed help from subcommands.
