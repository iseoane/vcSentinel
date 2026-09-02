// Package adaptersites is the ticket 12 slice-1 audit artifact: a curated,
// machine-checked inventory of EVERY site in this repository that either
//
//  1. spawns a provider/agent process or calls an agent adapter, or
//  2. mutates durable run lifecycle state (start/respond/abort/settle paths).
//
// Each site is classified into exactly one of five classes. The companion
// tests enforce three invariants:
//
//   - completeness: any file that starts carrying a canary marker
//     (exec.Command, EjecutarPrompt/EjecutarRevision calls, controller
//     construction, AppendEvent, capability/request construction, or a
//     store-primitive mutation call: CreateRun/AppendTerminalEvent/
//     SaveAttemptOutcome) without a declared entry FAILS
//     TestAdapterExecutionSitesInventory, so new execution sites cannot
//     appear unclassified;
//   - envelope totality: every in-scope (durable-controller) site is
//     asserted to route its provider call through an admitted
//     agentrun.InvocationEnvelope via the execution controller;
//   - rollback boundary: since ticket 13 (R11) removed the two release-
//     bounded switches (review.durable_runs / gate.durable_runs), ZERO
//     compatibility-gated sites are permitted; every lifecycle-mutating or
//     provider-executing site must be durably admitted or explicitly
//     out-of-scope.
//
// This package deliberately avoids AST magic: the value is the documented
// enumeration plus cheap textual canaries that fail loudly on drift. Entries
// whose Marker field is empty are documentation-only rows (files that host a
// path decision without directly matching any canary token).
package adaptersites

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Class is the lifecycle classification of one audited site.
type Class string

const (
	// ClassDurable sites admit their provider work through the execution
	// controller; every attempt carries an admitted InvocationEnvelope and
	// settles only through controller-authored lifecycle events.
	ClassDurable Class = "durable-controller"
	// ClassGated sites are the release-bounded rollback paths that used to
	// sit behind the review.durable_runs / gate.durable_runs switches.
	// Ticket 13 (R11) removed both switches and every gated site: the class
	// is kept only so the companion test can pin that it stays EMPTY.
	ClassGated Class = "compatibility-gated"
	// ClassHelper sites execute provider prompts but produce text that can
	// never influence a review verdict or gate outcome (commit messages,
	// advisory probes, narrative evidence). Documented lifecycle-out-of-scope.
	ClassHelper Class = "prompt-helper-out-of-scope"
	// ClassShared sites implement or wrap the low-level provider seam used by
	// BOTH admitted and gated callers; admission is decided upstream by the
	// caller, so the seam itself carries no envelope.
	ClassShared Class = "shared-provider-seam"
	// ClassInfra sites spawn processes that never invoke a provider agent
	// (git/gh/go plumbing, installers, tooling).
	ClassInfra Class = "non-agent-infrastructure"
)

// Site is one audited execution or lifecycle-mutation site.
type Site struct {
	// Path is the repository-relative slash-separated file path.
	Path string
	// Symbol names the representative function or type hosting the site.
	Symbol string
	// Anchor is the verbatim source text of the representative site, trimmed
	// of surrounding whitespace and of any trailing line comment. Entries with
	// a Marker assert that the file still CONTAINS this text. It deliberately
	// replaces the former line number, which every unrelated insertion above
	// the site invalidated: that churn produced refresh commits with no audit
	// signal in them.
	//
	// What this pins exactly: the anchored TEXT still exists somewhere in the
	// file. Where a file holds several byte-identical spawn lines (see
	// internal/agentadapter/cli.go), rewriting one of them leaves the check
	// green while a sibling survives — the pin is per-text, not per-site, and
	// the promise stops there. Completeness is the separate invariant, and it
	// does not depend on anchors: ScanMarkerFiles still fails on any
	// undeclared file that starts carrying a canary token.
	//
	// Symbol remains the human pointer to WHICH site the entry represents.
	Anchor string
	// Marker is the canary token that pins this entry to the scan. Empty for
	// documentation-only rows.
	Marker string
	// Class is the lifecycle classification of the whole file's relevant site.
	Class Class
	// Reason records WHY the site holds its class; for out-of-scope sites it
	// is the precise contract-required justification.
	Reason string
}

// AnchorProblem reports why a curated entry no longer matches the file text,
// or "" when the entry is sound. It lives here, not in the test, so the rule
// that decides audit rot can be exercised against synthetic entries: before
// this existed, every real entry passed and no test proved the check would
// fail on a stale anchor.
//
// Documentation-only rows (empty Marker) carry no anchor and are always sound.
func AnchorProblem(text string, site Site) string {
	if site.Marker == "" {
		if site.Anchor != "" {
			return "documentation-only row declares an anchor but no marker: either pin a marker or drop the anchor"
		}
		return ""
	}
	if site.Anchor == "" {
		return "entry declares marker " + strconv.Quote(site.Marker) + " with no anchor: it cannot be pinned to the file"
	}
	if !strings.Contains(site.Anchor, site.Marker) {
		return "anchor " + strconv.Quote(site.Anchor) + " does not contain its own marker " + strconv.Quote(site.Marker) + ": the entry pins the wrong text"
	}
	if !strings.Contains(text, site.Anchor) {
		return "the file no longer contains the anchored site " + strconv.Quote(site.Anchor) + ": re-read the site and refresh the anchor, class and reason"
	}
	return ""
}

// canaryMarkers are the textual tokens whose presence in any non-test Go file
// marks it as an execution or lifecycle-mutation site candidate. The three
// store-primitive tokens (ticket 13 hardening pool, R10 M2) audit direct
// mutation of durable run state: CreateRun admits, AppendTerminalEvent
// settles, and SaveAttemptOutcome records attempt outcomes.
var canaryMarkers = []string{
	"exec.Command",           // process spawn (matches exec.CommandContext too)
	"EjecutarPrompt(",        // arbitrary-prompt adapter invocation
	"EjecutarRevision(",      // restricted reviewer invocation
	"NewController(",         // execution controller construction (lifecycle authority)
	"AppendEvent(",           // durable lifecycle event append
	"agentrun.NewCapability", // admission capability construction
	"NewRunRequest(",         // admission request construction
	"CreateRun(",             // durable run admission primitive (store)
	"AppendTerminalEvent(",   // durable terminal settlement primitive (store)
	"SaveAttemptOutcome(",    // durable attempt-outcome record primitive (store)
}

// Sites returns the complete curated inventory. Order follows the module tree
// for reviewability.
func Sites() []Site {
	return []Site{
		// --- cmd/sentinel -------------------------------------------------
		{Path: "cmd/sentinel/autoria.go", Symbol: "observedAgent.EjecutarPrompt/EjecutarRevision", Anchor: "salida, err := a.AuditorAgente.EjecutarPrompt(prompt)", Marker: "EjecutarPrompt(",
			Class: ClassShared, Reason: "Observer decorator over the configured auditor: it delegates to the wrapped adapter after recording the effective agent. Used identically by the admitted transport path and the gated legacy path; spawns nothing itself."},
		{Path: "cmd/sentinel/comandos_estado.go", Symbol: "ejecutarEnShell", Anchor: "cmd = exec.Command(\"cmd\", \"/C\", comando)", Marker: "exec.Command",
			Class: ClassInfra, Reason: "Deterministic shell runner for configured lint/test/build commands; never consults an agent."},
		{Path: "cmd/sentinel/comandos_explain.go", Symbol: "explain range plumbing", Anchor: "salida, err := exec.Command(\"git\", args...).Output()", Marker: "exec.Command",
			Class: ClassInfra, Reason: "Git plumbing for the change-profile explainer."},
		{Path: "cmd/sentinel/comandos_pr.go", Symbol: "pr/clipboard helpers", Anchor: "cmd := exec.Command(\"gh\", args...)", Marker: "exec.Command",
			Class: ClassInfra, Reason: "gh pr create publication, git config reads, and clipboard helpers; no provider agent. The legacy 'sentinel pr' gh passthrough was retired (T8.4), removing its former spawn site."},
		{Path: "cmd/sentinel/comandos_runs_actions.go", Symbol: "runs start/respond/abort/retry/recover via RepositoryHost", Anchor: "", Marker: "",
			Class: ClassDurable, Reason: "Operator control actions apply exclusively through execution.RepositoryHost/controller APIs over the common-dir store; the admission request construction itself moved to execution.ResolveAdmissionRequest (internal/execution/host.go), so this file no longer matches any canary token and is documented as a path-decision row."},
		{Path: "cmd/sentinel/comandos_runs.go", Symbol: "promptRunAdapter/buildRunsController", Anchor: "return execution.NewController(backing, promptAdapter), nil", Marker: "NewController(",
			Class: ClassDurable, Reason: "`sentinel runs` operator prompts execute ONLY inside the controller flow: buildRunsController hands promptRunAdapter to execution.NewController, so every Execute receives the admitted InvocationEnvelope. This closes the parallel path R7 slice 2 left out of scope."},
		{Path: "cmd/sentinel/comandos_tui.go", Symbol: "spawnDetachedTuiDaemon", Anchor: "cmd := exec.Command(exe, \"runs\", \"daemon\", \"start\")", Marker: "exec.Command",
			Class: ClassInfra, Reason: "Control-center daemon lifecycle (slice 9): spawns THIS binary as `runs daemon start` detached for the current repository; the child admits runs through the same durable controller path as every other daemon start. Never spawns a provider agent directly."},
		{Path: "cmd/sentinel/staged_check.go", Symbol: "staged volume plumbing", Anchor: "output, err := exec.Command(\"git\", args...).Output()", Marker: "exec.Command",
			Class: ClassInfra, Reason: "Git plumbing for the staged-commit volume measurement."},
		{Path: "cmd/sentinel/main.go", Symbol: "elegirAdaptadorYGenerarMensajes -> git.GenerarMensajesLotes", Anchor: "", Marker: "",
			Class: ClassHelper, Reason: "Commit-message generation asks the agent for batch message TEXT consumed by the interactive slice flow. It produces no verdict and cannot flip any gate outcome; adapter unavailability degrades to deterministic fallback messages."},
		{Path: "cmd/sentinel/review_transport.go", Symbol: "nuevoDurableReviewTransport/applyDurableCutover", Anchor: "", Marker: "",
			Class: ClassDurable, Reason: "Sole production construction site of the review DurableTransport and the gate durable wiring; since ticket 13 (R11) both are unconditional — a missing git common dir fails honestly instead of degrading to a removed legacy path."},

		// --- internal/acpadapter (ACP/acpx production adapter, ticket 16) ---
		{Path: "internal/acpadapter/adapter.go", Symbol: "AcpxAdapter.Command", Anchor: "cmd := exec.CommandContext(ctx, a.launcher[0], a.Args(prompt)...)", Marker: "exec.Command",
			Class: ClassShared, Reason: "Provider process spawn seam for the ACP/acpx strategy (ticket 16): Command is the transparent spawn description; the production path runs through the owned-tree spawner in review.go (process.Spawn), mirroring cli.go's seam split. Admission binding happens at the caller, not here."},
		{Path: "internal/acpadapter/review.go", Symbol: "AcpxAdapter EjecutarPrompt/EjecutarRevision/ReviewWithContext", Anchor: "func (a *AcpxAdapter) EjecutarPrompt(prompt string) (string, error) {", Marker: "EjecutarPrompt(",
			Class: ClassShared, Reason: "Prompt and restricted-review surface of the ACP/acpx adapter (ticket 16 slice 2, wired in slice 3): review runs reuse the shared reviewsnapshot.Create snapshot discipline and spawn through the owned-tree process.Spawn seam like cli.go; admission binding happens at the caller, not here."},
		{Path: "internal/agentadapter/acpx.go", Symbol: "AcpxBridge.ObtenerMensajeCommitConDiff", Anchor: "salida, err := b.EjecutarPrompt(prompt)", Marker: "EjecutarPrompt(",
			Class: ClassHelper, Reason: "Factory wiring of kind:acpx entries (ticket 16 slice 3): the bridge delegates commit-message generation through one prompt turn on the acp adapter. Commit-message text cannot influence a verdict or gate outcome and degrades to deterministic fallback messages on failure."},

		// --- internal/agentadapter ----------------------------------------
		{Path: "internal/agentadapter/cadena.go", Symbol: "CadenaAdaptador.EjecutarPrompt", Anchor: "func (c *CadenaAdaptador) EjecutarPrompt(prompt string) (string, error) {", Marker: "EjecutarPrompt(",
			Class: ClassShared, Reason: "Fallback chain over prompt adapters; whichever member answers becomes the caller's responsibility to have admitted upstream."},
		{Path: "internal/agentadapter/cli.go", Symbol: "CLIAdapter EjecutarPrompt/EjecutarRevision", Anchor: "cmd := exec.CommandContext(ctx, c.BinaryName, args...)", Marker: "exec.Command",
			Class: ClassShared, Reason: "THE provider process spawn seam (exec.CommandContext). Both the admitted review adapter and the gated legacy reviewers funnel through these methods; admission binding happens at the caller, not here."},
		{Path: "internal/agentadapter/contractadapter.go", Symbol: "AgentAdapter/AdaptadorPrompt interfaces", Anchor: "EjecutarPrompt(prompt string) (string, error)", Marker: "EjecutarPrompt(",
			Class: ClassShared, Reason: "Interface declarations only; no execution."},
		{Path: "internal/agentadapter/factory.go", Symbol: "shim resolution note", Anchor: "", Marker: "",
			Class: ClassInfra, Reason: "Documentation-only row: the file's ONLY exec.Command occurrence is prose inside the resolverBinarioReal doc comment (Windows .cmd shim caveat), so there is no spawn site to anchor; the actual spawn lives in cli.go. Anchoring the sentence would make rewording unrelated prose fail the audit."},
		{Path: "internal/agentadapter/snapshot.go", Symbol: "snapshot delegation to reviewsnapshot", Anchor: "", Marker: "",
			Class: ClassInfra, Reason: "Since ticket 16 slice 3 this file only delegates to internal/reviewsnapshot (shared by both adapter families); the git plumbing and its spawn seam moved with the implementation."},

		// --- internal/agentrun / internal/execution / internal/store ------
		{Path: "internal/agentrun/contracts.go", Symbol: "InvocationEnvelope/NewRunRequest/NewCapability", Anchor: "func NewRunRequest(candidate Candidate, prompt Prompt, capabilities []Capability) RunRequest {", Marker: "NewRunRequest(",
			Class: ClassDurable, Reason: "Contract definitions: envelopes, requests, capabilities, lifecycle states, and the transition table. The authority itself, not a caller."},
		{Path: "internal/execution/controller.go", Symbol: "Controller Start admission / terminal persistence", Anchor: "if err := c.store.CreateRun(job, policy); err != nil {", Marker: "CreateRun(",
			Class: ClassDurable, Reason: "The single lifecycle authority admits every run through store.CreateRun and settles attempts through store.AppendTerminalEvent; no feature package may call either primitive directly."},
		{Path: "internal/execution/controller_abort.go", Symbol: "Controller abort settlement", Anchor: "receipt, persistenceErr := c.store.AppendTerminalEvent(string(runID), event, state.revision, outcome)", Marker: "AppendTerminalEvent(",
			Class: ClassDurable, Reason: "Abort settles through the guarded store terminal-event primitive under the controller's revision checks; nothing mutates state outside controller APIs."},
		{Path: "internal/execution/controller_recover.go", Symbol: "Controller recover settlement", Anchor: "return c.store.AppendTerminalEvent(head.RunID, event, projection.Revision, outcome)", Marker: "AppendTerminalEvent(",
			Class: ClassDurable, Reason: "Recovery settlement appends its terminal event through the same guarded store primitive; the honest reconciled verdict is the only source."},
		{Path: "internal/execution/controller_retry.go", Symbol: "Controller retry append", Anchor: "receipt, err := c.store.AppendEvent(string(runID), event, appendGuard)", Marker: "AppendEvent(",
			Class: ClassDurable, Reason: "Retry relaunch events append through the same guarded store primitive."},
		{Path: "internal/execution/host.go", Symbol: "ResolveAdmissionRequest", Anchor: "return agentrun.NewRunRequest(agentrun.Candidate(request.Candidate), agentrun.Prompt(request.Prompt), nil)", Marker: "NewRunRequest(",
			Class: ClassDurable, Reason: "Constructs the canonical admission request from the transport-safe Candidate/Prompt pair so every RepositoryHost Start admits exclusively through the controller's durable envelope flow."},
		{Path: "internal/store/execution.go", Symbol: "Store.CreateRun", Anchor: "func (s *Store) CreateRun(job agentrun.LogicalJob, policy RunPolicy) error {", Marker: "CreateRun(",
			Class: ClassDurable, Reason: "Admission record primitive: writes the immutable request.json for a new durable run. Called only by the execution controller, never by feature packages."},
		{Path: "internal/store/execution_events.go", Symbol: "Store.AppendEvent/AppendTerminalEvent", Anchor: "func (s *Store) AppendEvent(runID string, event agentrun.NormalizedEvent, expectedRevision uint64) (EventReceipt, error) {", Marker: "AppendEvent(",
			Class: ClassDurable, Reason: "Append-only event log primitives (plain and terminal-with-outcome); called only by the execution controller internals, never by feature packages."},
		{Path: "internal/store/execution_outcomes.go", Symbol: "Store.SaveAttemptOutcome", Anchor: "func (s *Store) SaveAttemptOutcome(outcome AttemptOutcome) error {", Marker: "SaveAttemptOutcome(",
			Class: ClassDurable, Reason: "Immutable attempt-outcome record primitive: durably persists one admitted invocation result before the caller continues. Written only beside controller-authored terminal events."},

		// --- internal/reviewexec (admitted review transport) ---------------
		{Path: "internal/reviewexec/durable_transport.go", Symbol: "DurableTransport.RunWithPolicy", Anchor: "request := agentrun.NewRunRequest(candidate, agentrun.Prompt(prompt), nil)", Marker: "NewRunRequest(",
			Class: ClassDurable, Reason: "Review admission transport: builds the RunRequest, starts it through the controller, validates snapshot binding, and admits completions only against verified AttemptOutcome evidence."},
		{Path: "internal/reviewexec/reviewexec.go", Symbol: "ReviewAdapter.Execute", Anchor: "output, err = legacy.EjecutarRevision(prompt, a.sha, a.paths)", Marker: "EjecutarRevision(",
			Class: ClassDurable, Reason: "Executes exactly one admitted physical invocation per controller dispatch; receives the InvocationEnvelope and forwards cancellation/tree ownership to the controller."},

		// --- internal/gate --------------------------------------------------
		{Path: "internal/gate/gate_durable.go", Symbol: "EjecutarGate/asentarTrabajosValidacion", Anchor: "controller := execution.NewController(opts.DurableStore, rootRunAdapter{settle: settle})", Marker: "NewController(",
			Class: ClassDurable, Reason: "Gate orchestrator and sole execution path (ticket 13 R11 merged the removed legacy orchestration into it): admits ONE root run plus one settled child job per validation command, all through controller.Start with persisted parent linkage."},
		{Path: "internal/gate/gate_durable_adapters.go", Symbol: "rootRunAdapter/settledValidationAdapter", Anchor: "stamped = append(stamped, agentrun.NewCapability(capability.Name(), attributes))", Marker: "agentrun.NewCapability",
			Class: ClassDurable, Reason: "Gate execution adapters receive the admitted InvocationEnvelope on every controller dispatch; neither spawns anything nor mutates state outside controller APIs."},
		{Path: "internal/gate/gate_run_plan.go", Symbol: "BuildGateRunPlan", Anchor: "root := agentrun.NewLogicalJob(agentrun.NewRunRequest(", Marker: "NewRunRequest(",
			Class: ClassDurable, Reason: "Deterministic plan construction: builds the admission requests (candidate/prompt/capabilities) later admitted verbatim by the controller."},

		// --- internal/review -------------------------------------------------
		{Path: "internal/review/engine.go", Symbol: "policy-bound durable reviewer adapter", Anchor: "func (a policyBoundReviewer) EjecutarRevision(prompt, sha string, paths []string) (string, error) {", Marker: "EjecutarRevision(",
			Class: ClassShared, Reason: "Engine-level injection seam behind OpcionesAuditoria.ReviewTransport. Since ticket 13 (R11) removed the review.durable_runs switch, production wiring always supplies the admitted durable transport (the CRITICAL refuter routes through it unconditionally); the direct restricted call with its transport retry survives only as a defensive fallback for direct-call fixtures."},
		{Path: "internal/review/rama.go", Symbol: "overviewDeRama", Anchor: "salida, err := agente.EjecutarPrompt(ConstruirPromptOverview(rama, fichas))", Marker: "EjecutarPrompt(",
			Class: ClassHelper, Reason: "Branch-overview coherence prompt for the ADVISORY `pr review` report. It shapes operator-facing narrative only: overview failure degrades to the safe decision-chain fallback and can never flip a gate outcome or a commit-blocking verdict. Recorded as a follow-up candidate should pr review ever become enforcement."},
		{Path: "internal/review/snapshot.go", Symbol: "snapshot reader", Anchor: "salida, err := exec.Command(\"git\", args...).Output()", Marker: "exec.Command",
			Class: ClassInfra, Reason: "Git plumbing feeding reviewer context snapshots."},
		{Path: "internal/reviewsnapshot/snapshot.go", Symbol: "reviewsnapshot.Create/gitTreeEntry", Anchor: "cmd := exec.Command(\"git\", \"-C\", worktree, \"ls-tree\", \"-z\", sha, \"--\", filePath)", Marker: "exec.Command",
			Class: ClassInfra, Reason: "Shared read-only review snapshot discipline (git ls-tree/show plumbing) relocated in ticket 16 slice 3 so both adapter families run the exact same committed-content materialization; never invokes a provider agent."},

		// --- advisory/narrative helpers --------------------------------------
		{Path: "internal/modelprobe/verificador.go", Symbol: "Verificador.Verificar", Anchor: "actual, err := agente.EjecutarPrompt(promptModelo)", Marker: "EjecutarPrompt(",
			Class: ClassHelper, Reason: "One-shot model identity probe; records a mismatch in the profile store and explicitly never affects the caller's review request."},
		{Path: "internal/ops/verificar.go", Symbol: "Verificar delegar mode", Anchor: "salida, err := opts.Agente.EjecutarPrompt(promptVerificacion())", Marker: "EjecutarPrompt(",
			Class: ClassHelper, Reason: "Advisory tested-contract delegation; every failure degrades to ModoOmitido. Verification never blocks (aviso, nunca bloqueo)."},
		{Path: "internal/validation/validacion.go", Symbol: "delegarSinCapabilities", Anchor: "salida, err := opts.Agente.EjecutarPrompt(promptDelegacion())", Marker: "EjecutarPrompt(",
			Class: ClassHelper, Reason: "Delegated validation profile fallback. Structurally incapable of influencing gate outcomes: delegated runs (capabilityDelegada) are excluded from Fallo AND Hallazgos before any blocking decision, and every error path returns an empty list. Narrative evidence only, by documented design."},
		{Path: "internal/agentshell/agentshell.go", Symbol: "system shell runner", Anchor: "cmd = exec.Command(\"cmd\", \"/c\", comando)", Marker: "exec.Command",
			Class: ClassHelper, Reason: "Shell executor beneath the advisory delegated contracts (ops.Verificar / validation delegation); consumers are advisory-only, so the shell itself gates nothing."},

		// --- non-agent infrastructure ----------------------------------------
		{Path: "internal/change/perfil.go", Symbol: "change profiling", Anchor: "salida, err := exec.Command(\"git\", args...).Output()", Marker: "exec.Command",
			Class: ClassInfra, Reason: "Git diff/tree-hash plumbing for change profiles."},
		{Path: "internal/git/gitdir.go", Symbol: "ObtenerGitCommonDir", Anchor: "cmd := exec.Command(\"git\", \"-C\", path, \"rev-parse\", \"--git-common-dir\")", Marker: "exec.Command",
			Class: ClassInfra, Reason: "Git common-dir discovery backing the shared durable store location."},
		{Path: "internal/git/parent.go", Symbol: "git command runner", Anchor: "cmd := exec.Command(command, args...)", Marker: "exec.Command",
			Class: ClassInfra, Reason: "Generic git plumbing."},
		{Path: "internal/git/plan.go", Symbol: "slice commit plumbing", Anchor: "if salida, err := exec.Command(\"git\", argsAdd...).CombinedOutput(); err != nil {", Marker: "exec.Command",
			Class: ClassInfra, Reason: "git add/commit plumbing for approved slice batches (--no-verify by design). Message generation above is classified separately as a helper."},
		{Path: "internal/git/selection_apply.go", Symbol: "selection apply plumbing", Anchor: "cmd := exec.Command(\"git\", args...)", Marker: "exec.Command",
			Class: ClassInfra, Reason: "Git plumbing applying approved selections."},
		{Path: "internal/git/slice.go", Symbol: "diff/measurement plumbing", Anchor: "cmd := exec.Command(\"git\", args...)", Marker: "exec.Command",
			Class: ClassInfra, Reason: "Git diff and volume measurement plumbing."},
		{Path: "internal/graph/codegraph.go", Symbol: "codegraph CLI client", Anchor: "cmd := exec.CommandContext(ctx, ejecutable, args...)", Marker: "exec.Command",
			Class: ClassInfra, Reason: "CodeGraph enrichment binary; metadata only, never a provider agent."},
		{Path: "internal/graph/native.go", Symbol: "native graph plumbing", Anchor: "cmd := exec.Command(\"git\", append([]string{\"-c\", \"core.attributesFile=\" + os.DevNull, \"-c\", \"diff.external=\", \"-C\", directorio}, args...)...)", Marker: "exec.Command",
			Class: ClassInfra, Reason: "Git plumbing for the native graph analyzer."},
		{Path: "internal/inventory/inventory.go", Symbol: "execRunner.run", Anchor: "cmd := exec.Command(\"git\", args...)", Marker: "exec.Command",
			Class: ClassInfra, Reason: "Read-only git plumbing for repository inventory snapshots (rev-parse/config/worktree list/status); never invokes a provider agent."},
		{Path: "internal/ops/events.go", Symbol: "gh pr view probe", Anchor: "salida, err := exec.Command(\"gh\", \"pr\", \"view\", strconv.Itoa(numero), \"--json\", \"state\").Output()", Marker: "exec.Command",
			Class: ClassInfra, Reason: "GitHub CLI state probe for ops events."},
		{Path: "internal/planning/context.go", Symbol: "planning context", Anchor: "output, err := exec.Command(\"git\", args...).Output()", Marker: "exec.Command",
			Class: ClassInfra, Reason: "Git plumbing for planning context."},
		{Path: "internal/process/process.go", Symbol: "process runner", Anchor: "cmd := exec.CommandContext(ctx, name, args...)", Marker: "exec.Command",
			Class: ClassInfra, Reason: "Low-level context-aware process runner beneath the provider seam; owns tree accounting, not agents."},
		{Path: "internal/setup/github.go", Symbol: "gh auth token", Anchor: "cmd := exec.Command(\"gh\", \"auth\", \"token\")", Marker: "exec.Command",
			Class: ClassInfra, Reason: "Installer credential probe."},
		{Path: "internal/setup/install.go", Symbol: "installer", Anchor: "cmd := exec.Command(\"go\", \"install\", paquete)", Marker: "exec.Command",
			Class: ClassInfra, Reason: "go install/GOPATH/PATH installer plumbing."},
		{Path: "internal/setup/uninstall.go", Symbol: "uninstaller", Anchor: "cmd := exec.Command(\"powershell\", \"-NoProfile\", \"-Command\", comando)", Marker: "exec.Command",
			Class: ClassInfra, Reason: "PATH cleanup plumbing."},
		{Path: "internal/git/commit.go", Symbol: "ContenidoEnAlgunRefDe", Anchor: "cmd := exec.Command(\"git\", \"-C\", worktree, \"branch\", \"-a\", \"--contains\", sha)", Marker: "exec.Command",
			Class: ClassInfra, Reason: "Read-only ref containment probe scoped to an explicit worktree instead of the process working directory. Git plumbing behind the orphan-ficha purge, which decides deletions and must classify against the repository it is purging; consults no agent."},
		{Path: "internal/setup/upgrade.go", Symbol: "upgrader", Anchor: "cmd := exec.Command(binarioActual, \"--version\")", Marker: "exec.Command",
			Class: ClassInfra, Reason: "Binary version probe for upgrades."},
		{Path: "tools/release/main.go", Symbol: "release tooling", Anchor: "cmd := exec.Command(\"gh\", \"release\", \"view\", \"--json\", \"tagName\", \"--jq\", \".tagName\")", Marker: "exec.Command",
			Class: ClassInfra, Reason: "Release asset tooling outside the sentinel runtime."},
		{Path: "tools/fu10divergence/main.go", Symbol: "divergence measurement harness", Anchor: "salida, err := exec.Command(\"git\", args...).Output()", Marker: "exec.Command",
			Class: ClassInfra, Reason: "Read-only Git plumbing for the FU-10 divergence measurement (ticket 03): rev-list, diff-tree and show against existing commits. Analysis tooling outside the sentinel runtime; consults no agent and mutates nothing."},
	}
}

// ModuleRoot walks up from this package's directory until it finds go.mod,
// so the tests locate the module regardless of the working directory.
func ModuleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}

// skippedDir reports whether a directory is excluded from the canary scan.
// The adaptersites package itself is excluded because its source embeds the
// literal canary tokens this audit greps for (self-reference).
func skippedDir(rel string) bool {
	switch rel {
	case ".git", ".codegraph", "bin", "docs", ".scratch", "internal/adaptersites":
		return true
	}
	return strings.HasSuffix(rel, "/testdata") || rel == "testdata"
}

// ScanMarkerFiles walks every non-test Go file under root (minus skipped
// directories) and returns, per file, the subset of canaryMarkers it
// contains. Files absent from the result carry no audited site candidates.
func ScanMarkerFiles(root string) (map[string][]string, error) {
	found := make(map[string][]string)
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if info.IsDir() {
			if rel != "." && skippedDir(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		text := string(content)
		for _, marker := range canaryMarkers {
			if strings.Contains(text, marker) {
				found[rel] = append(found[rel], marker)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}
