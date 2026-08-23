# 12: Add The Repository-Local Daemon

**What to build:** One authenticated local daemon per repository owns durable
run admission, supervision, and command serialization. Commands prefer a
healthy daemon endpoint and otherwise keep today's in-process behavior
byte-compatibly. Startup claims exclusive ownership with stale-endpoint
recovery; shutdown bounds its wait and explicitly orphans active runs;
restart reconciliation reuses the R8 machinery instead of duplicating it.

**Blocked by:** 09 (complete, merged), 10 (complete, merged).

**Status:** in_progress

**Design contract (agreed analysis):**

- Endpoint state lives under `<git-common-dir>/vas-sentinel/daemon/`: an
  owner claim file plus, once transport exists, an `endpoint.json`
  describing transport kind, address, pid, boot identity, and protocol
  revision. A claim is stale when its pid is dead or its endpoint is
  unreachable; stale claims are reclaimed deterministically, healthy ones
  never are.
- Single-owner startup: exactly one daemon admits runs per repository. A
  second starter connects as a client or reports the healthy owner; it never
  becomes a rival admitter.
- Transport sits behind a small listen/dial seam over `net.Listener` and
  `net.Conn`, speaking length-prefixed JSON frames that carry the D1
  envelopes unchanged. Linux uses a Unix domain socket inside a 0700
  directory. Windows uses a current-user-scoped named pipe built on
  `golang.org/x/sys/windows` (already a direct dependency) or loopback TCP
  with a 0600 bearer-token file, whichever the evidence justifies. No new
  `go.mod` dependencies.
- Every request carries the D1 `AuthContext`; the daemon enforces presence
  and binds requests to the repository fingerprint of its own git common
  dir. Cross-repository requests are rejected deterministically. Richer
  remote identity belongs to D4, not here.
- One admission mutex serializes `Start` and `Apply`; `Inspect` and
  `Subscribe` serve concurrently.
- On boot the daemon runs the R8 recovery scan: auto-recoverable classes
  settle automatically, `operator_required` entries surface untouched with
  their exact missing-evidence naming. Repairing bytes stays an explicit
  operator action.
- Stop bounds its drain with a grace budget, then persists explicit orphaned
  evidence for still-active invocations so the next scan classifies them
  cleanly. A killed daemon produces the same outcome on next boot.
- `sentinel runs` commands connect to a healthy endpoint when present and
  fall back to the in-process host otherwise, byte-identically. Starting
  the daemon stays explicit (`runs daemon start|status|stop`); autostart on
  demand is out of scope. Rollback boundary: removing the endpoint preference
  restores today's behavior exactly.

**Acceptance criteria:**

- [ ] Two concurrent startups in one repository yield exactly one owner and
      a deterministic connect-or-refuse outcome for the other.
- [ ] Dead-pid and unreachable endpoints are reclaimed without operator
      intervention; live healthy endpoints are never reclaimed.
- [ ] Every operation enforces principal presence and repository binding;
      cross-repository requests fail with a stable error.
- [ ] Start/Apply serialize under concurrency; parity versus InProcessHost
      is proven over the real transport for all four operations, including
      sentinel-error identity via `errors.Is`.
- [ ] A daemon restart mid-run leaves R8-classified state: auto-recoverable
      settled, operator-required surfaced, nothing silently rewritten.
- [ ] Graceful stop respects its grace budget and orphans survivors with
      persisted evidence; the following scan needs no special cases.
- [ ] `runs daemon start|status|stop` exist; runs commands transparently
      prefer a healthy endpoint and fall back in-process otherwise.
- [ ] Windows and Linux lifecycle behaviors are exercised by harnesses or
      CI-equivalent proofs recorded in evidence.
- [ ] Evidence records build, vet, tests, guardian, independent
      `code-review` per slice, Judgment Day at closure, rollback boundary,
      and follow-ups.

**Out of scope:** Attach TUI (D3); remote hosts (D4/D5); autostart on
demand; multi-repository daemons; identity models beyond presence plus
repository binding; changing durable persistence formats.

**Suggested slices:**

1. Ownership claim and discovery protocol over the daemon directory with
   stale detection and reclaim; platform pid-liveness isolation; no
   transport yet.
2. Transport framing plus server-side `RepositoryHost` dispatch with
   authentication, repository binding, and serialization; client adapter and
   runs-command wiring with in-process fallback; parity proofs over real
   transports.
3. Graceful-shutdown semantics, restart reconciliation through the R8 scan,
   daemon CLI lifecycle, Windows/Linux harness proofs, documentation, and
   Judgment Day closure.

## Evidence — slice 1 (ownership claim and discovery)

- Base: main at e1b9433 (after A1-ticket renumbering).
- Commits: feat(daemon) add exclusive repository ownership claim protocol
  (+381 across daemon.go, process_unix.go, process_windows.go),
  test(daemon) pin claim exclusivity, stale reclaim, and typed failures
  (+360). Both staged candidates passed the pre-commit budget (381 / 360,
  both PUNTO_OPTIMO).
- Surface: internal/daemon package — Owner{pid, started_at, host,
  protocol_revision=1} with stable json tags; Claim() via a single
  O_CREATE|O_EXCL open (exclusivity observable before payload exists;
  write-then-rename deliberately rejected); ErrDaemonOwned + *OwnedError
  carrying the live owner; bounded stale-reclaim loop (5 attempts, 2ms
  backoff mirroring store's lock retry style) where an existing-but-empty
  claim is treated as a concurrent winner mid-write; InspectOwner();
  Release() restricted to the owning pid with deterministic foreign/missing
  errors. Platform pid liveness behind build tags: unix kill(pid,0)
  (ESRCH dead, EPERM alive), windows x/sys OpenProcess + exit probe
  (ERROR_ACCESS_DENIED alive; local stillActive=259 because x/sys v0.47.0
  does not export it).
- Review-driven fixes: empty-claim poison pill now fails fast and typed —
  ErrEmptyOwnerClaim + *EmptyClaimError naming the path instead of a generic
  attempts-exhausted message; exhausted errors wrap their last underlying
  cause (%w); parseClaim rejects decoded payloads with pid <= 0 as unreadable
  so garbage like {} cannot produce platform-divergent liveness verdicts;
  overclaiming retry comment corrected (torn non-empty payloads fail
  explicitly by design rather than retrying); dead Owner.ClaimPath removed.
  Documentation honesty: exclusivity proven single-process under concurrency
  (multi-process rests on O_EXCL semantics), Release read-check-remove
  theoretical window acknowledged, Windows exit-code-259 ambiguity recorded
  beside PID reuse as accepted residual risk.
- Independent code review (dual axis): spec PASS with no scope creep — no
  transport/socket/CLI creep, zero existing-package modifications, corrupt
  claims never enable dual ownership, live owners never reclaimed. Standards
  found the poison-pill diagnosability gap and the documentation gaps above,
  all fixed; one accepted divergence noted (*OwnedError pointer receiver vs
  store's value style) recorded as follow-up nit.
- Test strategy: single-binary re-exec helper (os.Args[0] +
  anchored -test.run) provides deterministic short-lived and long-lived
  children on both platforms; exclusivity proven with parallel racers under
  -race; honest limit documented that multi-process exclusivity rests on
  O_EXCL kernel semantics rather than a multi-process test.
- Verification: gofmt clean; build/vet OK; go test ./internal/daemon/...
  -race green (plus -count=10 stress during development); GOOS=windows build
  OK; FULL suite green across 24 packages (-count=1).
- Known pre-existing breakage outside this slice: GOOS=darwin full-repo build
  fails in internal/process (undefined newPlatformOwner; reproduced on clean
  base). Package-scoped darwin build+vet for internal/daemon passes via the
  unix tag.

*(slice 2 pending)*

## Evidence — slice 2a (transport framing + server dispatch)

- Base: branch state after slice-1 evidence commit a0fa45f. No main contact:
  this worktree stays isolated per joint decision until integration is
  agreed.
- Commits: feat(daemon) add framed wire protocol with sentinel registry
  (+203), feat(daemon) add platform endpoint listeners and discovery (+357),
  feat(daemon) dispatch host operations over the local transport
  (+354/-6), test(daemon) pin framing, codec registry, and endpoint
  persistence (+291), test(daemon) cover wire op flows over real transports
  (+383), test(daemon) prove sentinel identity parity across the wire
  (+241), test(daemon) pin handshake guards and admission exactly-once
  (+229). All seven staged candidates passed the pre-commit budget; the two
  original oversized test files (406/415 lines) were split by cohesion into
  five files under 350 with an identical 32-test-function count before and
  after.
- Surface: 4-byte big-endian length-prefixed JSON frames (MaxFrameSize
  enforced both directions); wireRequest/wireResponse/handshake types with
  stable tags; sentinel code registry (canonical codes like
  "execution.stale_revision", "store.execution_not_found",
  "daemon.daemon_owned") so *RemoteError.Unwrap resolves errors.Is across
  the wire; Endpoint listen/dial seam — unix domain socket inside the 0700
  daemon dir with stale-probe dial (250 ms) refusing binds under live
  owners, Windows loopback TCP 127.0.0.1 + bearer token file justified in
  evidence (stdlib-only; named-pipe upgrade deferred); endpoint.json
  persisted atomically rename-first (POSIX) with remove-rename fallback
  (Windows), carrying {network, address, pid, started_at, host,
  protocol_revision} plus omitempty TokenFile; Server dispatch enforcing
  handshake protocol revision and repository fingerprint (sha256 of cleaned
  absolute git common dir), re-checking principal presence server-side,
  serializing Start/Apply behind one admission mutex while Inspect/Subscribe
  run concurrently.
- Independent code review (dual axis): spec PASS on binding, serialization,
  and parity scope; standards found three hard issues all fixed —
  writeFileAtomic forfeited POSIX atomicity (now rename-first mirroring
  store's atomicWrite), endpointRecord silently dropped Owner.Host (persisted
  and round-trip asserted now), Close doc overclaimed cancellation of run
  workers (reworded: detached workers survive by design; drain/orphan is
  slice 3). Cheap hardening applied: registry completed with
  ErrRunNotRetryable/ErrRunNotRecoverable codes, constant-time token compare,
  LoadEndpoint network validation, zero-RunRequest limitation promoted into
  production docs at handleStart, fingerprint caveat documented (no symlink
  resolution or case normalization; both sides derive from git output).
- Honest limits recorded: ErrStaleRevision and ErrDaemonOwned are pinned at
  codec level only (no wired op raises them yet; future retry/recover ops
  inherit identity from the registry); Apply against a never-existing run
  surfaces store.ErrExecutionNotFound through reconstructAwaitingState, and
  both wire and local parity cases assert that real behavior; wire starts
  admit only the zero agentrun.RunRequest because its fields are deliberately
  unexported identity-canonical data — prompt transport is the headline
  design decision for slice 2b; a momentarily overloaded live daemon could be
  misjudged stale by the 250 ms probe until slice 3 couples the probe to the
  ownership claim.
- Verification: gofmt clean; build/vet OK; internal/daemon -race green
  including -count=5 stress during development; GOOS=windows build+vet OK;
  FULL suite green across 24 packages (-count=1).

*(slice 2b pending)*

## Evidence — slice 3 (lifecycle, reconciliation, closure)

*(pending)*
