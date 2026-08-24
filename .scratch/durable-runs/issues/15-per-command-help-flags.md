# 15: Per-Command Help Flags

**What to build:** Every `sentinel` command and subcommand accepts `-h`/`--help`
as its FIRST effective argument and responds with dedicated help text (purpose,
usage line, flags table, examples where valuable) exiting 0 — instead of today's
behavior where `--help` is an unknown-argument error or, worse, a misparsed
action (`sentinel pr --help` currently runs cleanup side effects).

**Blocked by:** 14 (complete, merged at 229aa98).

**Status:** ready-for-agent

**Design contract (agreed analysis):**

- Central interception in the dispatcher before command execution: when a
  command's argument list contains `-h` or `--help` (any position for
  flag-taking commands; first position minimum), print that command's help to
  stdout and exit 0. This also removes the `pr --help` side-effect bug.
- One help text per command/subcommand, English, matching the existing usage
  messages' vocabulary. Reuse existing usage strings where they already exist
  (extract into shared constants so runtime errors and help cannot drift).
  Commands: version, help, init, uninit, check, slice (+plan/apply), review,
  gate, lint, rebase, status, explain, consentimiento-diff, pr (+create/review),
  install, upgrade, uninstall, runs (+each subcommand).
- `sentinel help <command>` also resolves through the same texts (today it
  prints only the top-level list; keep that behavior plus per-command detail).
- Additive compatibility: no existing flag behavior changes; unknown-flag
  errors keep their exit codes; output text additions don't alter existing
  pinned strings.

**Acceptance criteria:**

- [ ] Table-driven test proves every registered command and subcommand answers
      -h/--help with exit 0, non-empty stdout containing the usage line, and
      no stderr.
- [ ] Focused test proves `pr --help` performs NO side effects (no cleanup
      run) and exits 0.
- [ ] Focused test proves `help <command>` prints the same text as
      `<command> --help`.
- [ ] Existing unknown-flag error tests remain green unchanged.
- [ ] The implementation records build, vet, tests, guardian, independent
      `code-review`, rollback boundary, and follow-ups here.

## Evidence

*(appended per slice)*

## Follow-ups

*(recorded at closure)*
