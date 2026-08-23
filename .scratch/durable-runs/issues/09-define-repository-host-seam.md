# 09: Define The Repository Host Seam

> Renumbered from 08 to 09: the concurrent R7 session claimed ticket 08 for
> cancellation ownership while this ticket was being drafted.

**What to build:** The run controller gains a narrow repository-host port
covering start, inspect, subscribe, and apply. Local transport envelopes carry
an authentication context, expected revisions, and idempotency identities.
The in-process controller remains the default host implementation, commands
keep their exact output and exit-code contracts, and both sides of the seam
are contract-tested without starting any background daemon.

**Blocked by:** 05 (complete).

**Status:** complete

**Design contract (agreed analysis):**

- The port lives beside the controller in `internal/execution`:
  `RepositoryHost` with exactly `Start`, `Inspect`, `Subscribe`, `Apply`.
  Existing controller error identity (`ErrRunNotActive`,
  `ErrUnsupportedAction`, `ErrStaleRevision`, `ErrDecisionNotPending`,
  `ErrRunAlreadyExists`) flows through unchanged; the port never reclassifies
  controller outcomes. Seam-level validation introduces exactly two new
  exported sentinels: one for a missing authentication principal, one for an
  `Apply` replay carrying an already-consumed idempotency identity.
- `InProcessHost` delegates one-to-one to `Controller` and is the default
  implementation wired into `sentinel runs` commands. Behavior-neutral:
  outputs and exit codes stay byte-identical, pinned by untouched CLI tests.
- Envelope types (per-operation request/response structs) carry stable JSON
  tags; they are the D2 wire shape. Every envelope embeds an authentication
  context; D1 validates presence only, never identity semantics.
- `Subscribe` reads durable events after a sequence cursor through the store,
  matching today's `logs --after/--limit` semantics. No channels, no fan-out,
  no daemon at D1.
- `Apply` envelopes carry a caller-generated idempotency identity. The
  in-process host records seen identities per run and deterministically
  rejects replays within its process lifetime. Cross-restart dedup belongs to
  D2/R8.
- Contract tests exercise the port through envelopes (client side, dispatch,
  response) rather than past the interface; direct-controller parity
  scenarios pin behavior neutrality.

**Acceptance criteria:**

- [x] `RepositoryHost` exposes exactly start, inspect, subscribe, apply;
      controller internals stay behind it. *(Slice 1; the adapter reaches the
      store only through Controller.ReadEventPage after slice-1 review.)*
- [x] Routed `sentinel runs` commands execute through the in-process host
      with byte-identical output and unchanged exit-code mapping. *(start,
      respond, abort since slice 1; status, verify pre-check, and logs
      paging since slice 2 — existing CLI tests untouched and green.)*
- [x] Envelopes round-trip JSON stably; an empty authentication context is
      rejected on every operation with a deterministic error.
      *(Slice 2: ErrMissingPrincipal on all four ops before any store contact;
      key-name snapshot pins tag drift. Caveat recorded in slice 2 evidence:
      StartRequest.Request marshals as {} because agentrun.RunRequest fields
      are unexported by design.)*
- [x] An `Apply` replay reusing the same idempotency identity within one
      process lifetime is rejected; a fresh identity proceeds normally.
      *(Slice 2: ErrDuplicateAction, identity scoped per run, at-most-once on
      admission attempt — documented in Apply.)*
- [x] Stale-revision and invalid-state errors keep their identity through the
      port (`errors.Is` parity). *(Slice 1 table; ErrStaleRevision itself has
      no port path until envelopes carry expected revisions — recorded as a
      future envelope evolution, not this ticket.)*
- [x] No daemon, socket, autostart, or background process appears anywhere;
      contract tests run entirely in-process.
- [x] The implementation records build, vet, tests, guardian, independent
      `code-review`, rollback boundary, and follow-ups here.

**Out of scope:** Daemon lifecycle, endpoint discovery, real authentication
(D2); restart reconciliation (R8); attach TUI (D3); remote hosts (D4/D5);
routing `reviewexec.DurableTransport` through the port (revisited at R9);
extending the port with retry/recover/verify equivalents (D2 decides their
transport shape).

**Suggested slices:**

1. Port plus in-process host plus command rewiring, behavior-neutral, with
   direct-versus-port parity tests.
2. Envelope round-trip guarantees, authentication-presence validation,
   idempotency-identity replay rejection, subscribe-from-cursor contract
   tests, documentation touch-ups.

## Evidence — slice 1 (port + in-process host + rewiring)

- Base: main at a2700f4. R7 landed from a concurrent session while this slice
  was in flight; the full suite is green on the merged tree, which doubles as
  seam-compatibility proof against the new escalation machinery.
- Commits: feat(execution) add repository host seam over the run controller
  (+101/-6 across host.go, Controller.ReadEventPage, comandos_runs_actions.go
  rewiring), test(execution) pin repository host parity through the port
  (+291). Both staged candidates passed the pre-commit budget (101 / 291).
- Surface: RepositoryHost with exactly Start/Inspect/Subscribe/Apply; request
  structs StartRequest{Request, Policy}, ApplyRequest{RunID, Action},
  SubscribeRequest{RunID, AfterCursor, Limit}; InProcessHost is pure
  one-to-one delegation (68 lines). New exported Controller.ReadEventPage owns
  cursor paging normalization (nil-ctx handling, ErrControllerNotReady on nil
  store, limit <= 0 falls back to eventPageSize), so the adapter never reaches
  through the controller's private store field. Routed CLI paths:
  `runs start` -> host.Start; `runs respond`/`runs abort` -> host.Apply;
  abort's idempotent-head inspect -> host.Inspect. retry/recover/verify and
  status/logs readers stay direct per the narrow-port contract.
- Independent code review (dual axis): spec PASS with no blockers — port shape
  exact, rewiring byte-neutral, untouched paths untouched, parity tests cross
  the real seam, subscribe honest (exclusive cursor confirmed at
  execution_events.go:338). Standards axis produced three accepted fixes:
  errEmptyRunIdentity removed as a contract-violating new error class
  (presence validation returns in slice 2 together with the auth context that
  gives it a consumer); Subscribe now delegates to Controller.ReadEventPage;
  the overclaiming stale-revision test was renamed
  TestStaleRevisionSentinelRemainsOutsidePortUntilSliceTwo and a genuine
  port-level ErrControllerNotReady test added for Start/Apply/Subscribe. The
  standards note about missing JSON tags/auth/idempotency fields was
  adjudicated mis-scoped: those are slice 2 deliverables under this ticket's
  own slice plan.
- Honest limits recorded: Inspect on a zero-value Controller still panics
  (pre-existing direct behavior preserved byte-for-byte); ErrStaleRevision
  cannot surface through the four port operations until Apply envelopes gain
  ExpectedRevision (slice 2); ErrControllerNotReady is guaranteed through
  Start, Apply, and Subscribe only.
- Verification: gofmt clean; go build OK; go vet OK; focused execution + cmd
  suites green; full suite green across 23 packages (-count=1) on the merged
  post-R7 tree.
- Housekeeping: unrelated untracked sess-show*.txt files at the repository
  root were preserved untouched and never staged.

## Evidence — slice 2 (envelopes, auth presence, idempotency, subscribe)

- Base: main at 936b7b8 (slice-1 evidence commit).
- Commits: feat(execution) carry principal and action identities in host
  envelopes (+194/-30 across host.go, actions, decls, read),
  test(execution) pin envelope auth presence and replay rejection
  (+237/-31 across host_test.go, reconciliation_test.go signature updates).
  Both staged candidates passed the pre-commit budget (194 / 237).
- Surface: AuthContext{Principal} embedded in all four request envelopes;
  InspectRequest{RunID, AuthContext} makes the interface uniform;
  ApplyRequest.ActionID with process-lifetime replay rejection via
  ErrDuplicateAction (scoped per run); ErrMissingPrincipal returned before
  any controller/store contact on every operation; stable json tags pinned by
  key-name snapshot round-trip tests; CLI resolves the principal through
  USERNAME -> USER -> os/user.Current() and generates ActionIDs mirroring the
  candidate pattern; logs paging routes through host.Subscribe byte-neutrally
  (limit normalization unreachable: parseRunOptions defaults 100 and rejects
  --limit 0).
- Independent code review (dual axis): spec PASS — zero-value-Controller
  tests prove pre-contact rejection; dedup keyed (runID, ActionID) verified
  mutex-correct; executeRunsLogs proven byte-identical; retry/recover keep
  direct controller calls; Controller untouched. Accepted polish fixes:
  fmt.Errorf-without-args replaced by errors.New matching controller style;
  at-most-once consumption documented explicitly in Apply ("a rejected action
  still burns its identity"); over-promising test renamed to
  TestStaleRevisionSentinelRemainsOutsidePortUntilEnvelopesCarryExpectedRevisions.
- Race found during full-suite verification and fixed: the dedup test's first
  accepted Apply spawned a child worker that outlived the test, racing
  TempDir RemoveAll (`unlinkat ... directory not empty`, 2/5 rounds under
  concurrent package load). Fixed by draining run B to awaiting-decision with
  the file's existing waitForStateViaHost idiom; paired-package stress then
  reported exe=0 cmd=0 in 10/10 rounds. Two transient cmd/sentinel failures
  under whole-suite load (incomplete-terminal-persistence windows) vanished
  after the drain fix and never reproduced again: two consecutive full-suite
  runs green, confirming they were second-order contention from the leak, not
  independent defects.
- Honest limits recorded: StartRequest.Request marshals as {} because
  agentrun.RunRequest keeps its fields unexported (identity-canonical), so
  wire transport of a start payload remains an explicit D2 decision;
  ErrStaleRevision has no port path until envelopes evolve an expected-
  revision field; replay detection is process-lifetime only by contract.
- Follow-ups accepted without code change: the four-fold principal guard in
  the adapter could share one helper if it grows; resolveRunsPrincipal plus
  error-print boilerplate repeats across six command sites (candidate for one
  command-level helper); principal-resolution failure exits via runExitCode
  in action commands but runExitInfrastructure in status/logs/verify — same
  class, two policies, worth unifying at the next exit-code contract touch.
- Verification: gofmt clean; build/vet OK; focused execution + cmd suites
  green; FULL suite green twice consecutively (-count=1) after the drain fix;
  paired-load stress 10/10 clean.
- Rollback boundary as contracted: the seam is additive internal refactoring
  with no configuration surface; reverting this ticket's five commits
  restores pre-D1 behavior exactly, and commands remain bound to the
  in-process host.

**Closed:** D1 complete — the controller now runs behind a RepositoryHost
seam whose envelopes carry an authenticated principal and per-run
idempotency identities, with the in-process host as default implementation
and both sides of the seam contract-tested in-process. D2 can define endpoint
discovery and daemon lifecycle against this exact port.
