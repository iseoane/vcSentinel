# Slice 12 — Abort & Retry Actions

## Goal
Wire `a` (abort) and `r` (retry) so the operator acts on the focused
run from the keyboard: the request flows through the same daemon-
preferred host the runs CLI uses, and the 2-second refresh loop shows
the resulting state transition.

## Scope
- internal/tui/control:
  - `NewLive` gains a fourth parameter `actions RunActions` (interface
    with `Abort(repoPath, runID string) tea.Cmd` and
    `Retry(repoPath, runID string) tea.Cmd`); nil interface disables
    actions (static `New` models never have them). Document the
    contract.
  - Key handling: `a`/`r` act only when help is closed AND focus is
    FocusRuns AND the cursor sits on a visible run of an Enabled,
    non-missing, error-free repository; anything else is a no-op.
    They return the hook's command plus store a pending marker
    (accessor `PendingAction()` returning kind+repo+runID, cleared on
    any action-result message).
  - Private `actionResultMsg{Kind, RepoPath, RunID string, Err error}`
    handling: clears the pending marker; failure records into the
    same `Err()` channel as refresh failures (first-error-wins does
    NOT apply here — action feedback overwrites a prior refresh error
    string but never a repo.Error); success is silent because the
    next snapshot shows the transition.
- cmd/sentinel/comandos_tui.go:
  - Implement RunActions for the SESSION repository only: both methods
    capture the worktree root; each invocation resolves principal +
    `runsHostWithDaemonPreference` exactly like the runs CLI, sends
    `Apply(ControlAction{Kind: ActionAbort})` with a fresh unique
    ActionID (same format as executeRunsAbort) or `Retry` WITHOUT the
    CLI's settle wait, applies the idempotent-head fallback on
    rejection, and returns a command yielding actionResultMsg. Other
    repositories' paths return an immediate no-op command with an
    error message "actions are limited to the session repository".
  - Pass the implementation into NewLive.
- Rendering stays untouched: feedback arrives through the refreshed
  snapshot; the footer keeps advertising a/r now truthfully.

## Non-goals
- No respond/recover actions; no confirmation dialog; no status-line
  UI (a future polish slice may surface Err()/PendingAction visually);
  no cross-repo actions.

## Acceptance
- Control table tests: gating matrix (help open / wrong focus / repo
  degraded / disabled repo / nil actions -> no-op), pending marker set
  and cleared, actionResultMsg error lands in Err(), success clears
  quietly, stale cursor between dispatch and result still clears.
- cmd tests with fake hosts: abort sends Apply with unique ActionIDs
  and AuthContext principal; retry omits settle wait; idempotent-head
  fallback produces accepted result; foreign-repo path yields the
  documented no-op error command.
- build/vet/focused/race/full green; bypass recorded via orchestrator
  if indivisible.

## Review decisions
- Budget bypass granted by standing operator authorization: model
  dispatch plus wire-side session actions plus their fakes are one
  unit (1013 lines).
- Spec amendments: (1) ActionResult exported constructor + kind
  constants accepted as the minimal Go cross-package bridge (cmd
  cannot build unexported message structs); (2) pending marker clears
  on MATCHING results only, not on any action-result message.
- ActionID format stays triplicated across respond/CLI abort/TUI
  abort for now; extraction deferred until a fourth site appears.
- Robustness note accepted: a hook returning nil would strand the
  pending marker; interface contract documents the requirement.
