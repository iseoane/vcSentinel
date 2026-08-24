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
	// Line is the representative current line number. Entries with a Marker
	// assert that this line still contains it, so the enumeration cannot rot
	// silently; refresh the number when code moves.
	Line int
	// Marker is the canary token that pins this entry to the scan. Empty for
	// documentation-only rows.
	Marker string
	// Class is the lifecycle classification of the whole file's relevant site.
	Class Class
	// Reason records WHY the site holds its class; for out-of-scope sites it
	// is the precise contract-required justification.
	Reason string
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
		{Path: "cmd/sentinel/autoria.go", Symbol: "agenteObservado.EjecutarPrompt/EjecutarRevision", Line: 42, Marker: "EjecutarPrompt(",
			Class: ClassShared, Reason: "Observer decorator over the configured auditor: it delegates to the wrapped adapter after recording the effective agent. Used identically by the admitted transport path and the gated legacy path; spawns nothing itself."},
		{Path: "cmd/sentinel/comandos_estado.go", Symbol: "ejecutarEnShell", Line: 137, Marker: "exec.Command",
			Class: ClassInfra, Reason: "Deterministic shell runner for configured lint/test/build commands; never consults an agent."},
		{Path: "cmd/sentinel/comandos_explain.go", Symbol: "explain range plumbing", Line: 164, Marker: "exec.Command",
			Class: ClassInfra, Reason: "Git plumbing for the change-profile explainer."},
		{Path: "cmd/sentinel/comandos_pr.go", Symbol: "pr/clipboard helpers", Line: 81, Marker: "exec.Command",
			Class: ClassInfra, Reason: "gh CLI calls, git config reads, and clipboard helpers; no provider agent."},
		{Path: "cmd/sentinel/comandos_runs_actions.go", Symbol: "runs start/respond/abort/retry/recover via RepositoryHost", Line: 0, Marker: "",
			Class: ClassDurable, Reason: "Operator control actions apply exclusively through execution.RepositoryHost/controller APIs over the common-dir store; the admission request construction itself moved to execution.ResolveAdmissionRequest (internal/execution/host.go), so this file no longer matches any canary token and is documented as a path-decision row."},
		{Path: "cmd/sentinel/comandos_runs.go", Symbol: "promptRunAdapter/buildRunsController", Line: 206, Marker: "NewController(",
			Class: ClassDurable, Reason: "`sentinel runs` operator prompts execute ONLY inside the controller flow: buildRunsController hands promptRunAdapter to execution.NewController, so every Execute receives the admitted InvocationEnvelope. This closes the parallel path R7 slice 2 left out of scope."},
		{Path: "cmd/sentinel/staged_check.go", Symbol: "staged volume plumbing", Line: 126, Marker: "exec.Command",
			Class: ClassInfra, Reason: "Git plumbing for the staged-commit volume measurement."},
		{Path: "cmd/sentinel/main.go", Symbol: "elegirAdaptadorYGenerarMensajes -> git.GenerarMensajesLotes", Line: 0, Marker: "",
			Class: ClassHelper, Reason: "Commit-message generation asks the agent for batch message TEXT consumed by the interactive slice flow. It produces no verdict and cannot flip any gate outcome; adapter unavailability degrades to deterministic fallback messages."},
		{Path: "cmd/sentinel/review_transport.go", Symbol: "nuevoDurableReviewTransport/applyDurableCutover", Line: 0, Marker: "",
			Class: ClassDurable, Reason: "Sole production construction site of the review DurableTransport and the gate durable wiring; since ticket 13 (R11) both are unconditional — a missing git common dir fails honestly instead of degrading to a removed legacy path."},

		// --- internal/agentadapter ----------------------------------------
		{Path: "internal/agentadapter/cadena.go", Symbol: "CadenaAdaptador.EjecutarPrompt", Line: 32, Marker: "EjecutarPrompt(",
			Class: ClassShared, Reason: "Fallback chain over prompt adapters; whichever member answers becomes the caller's responsibility to have admitted upstream."},
		{Path: "internal/agentadapter/cli.go", Symbol: "CLIAdapter EjecutarPrompt/EjecutarRevision", Line: 191, Marker: "exec.Command",
			Class: ClassShared, Reason: "THE provider process spawn seam (exec.CommandContext). Both the admitted review adapter and the gated legacy reviewers funnel through these methods; admission binding happens at the caller, not here."},
		{Path: "internal/agentadapter/contractadapter.go", Symbol: "AgentAdapter/AdaptadorPrompt interfaces", Line: 11, Marker: "EjecutarPrompt(",
			Class: ClassShared, Reason: "Interface declarations only; no execution."},
		{Path: "internal/agentadapter/factory.go", Symbol: "shim resolution note", Line: 203, Marker: "exec.Command",
			Class: ClassInfra, Reason: "Comment-only reference (Windows .cmd shim caveat); the actual spawn lives in cli.go."},
		{Path: "internal/agentadapter/snapshot.go", Symbol: "snapshot readers", Line: 58, Marker: "exec.Command",
			Class: ClassInfra, Reason: "Git ls-tree/show snapshot plumbing feeding reviewer context."},

		// --- internal/agentrun / internal/execution / internal/store ------
		{Path: "internal/agentrun/contracts.go", Symbol: "InvocationEnvelope/NewRunRequest/NewCapability", Line: 61, Marker: "NewRunRequest(",
			Class: ClassDurable, Reason: "Contract definitions: envelopes, requests, capabilities, lifecycle states, and the transition table. The authority itself, not a caller."},
		{Path: "internal/execution/controller.go", Symbol: "Controller Start admission / terminal persistence", Line: 244, Marker: "CreateRun(",
			Class: ClassDurable, Reason: "The single lifecycle authority admits every run through store.CreateRun and settles attempts through store.AppendTerminalEvent; no feature package may call either primitive directly."},
		{Path: "internal/execution/controller_abort.go", Symbol: "Controller abort settlement", Line: 161, Marker: "AppendTerminalEvent(",
			Class: ClassDurable, Reason: "Abort settles through the guarded store terminal-event primitive under the controller's revision checks; nothing mutates state outside controller APIs."},
		{Path: "internal/execution/controller_recover.go", Symbol: "Controller recover settlement", Line: 162, Marker: "AppendTerminalEvent(",
			Class: ClassDurable, Reason: "Recovery settlement appends its terminal event through the same guarded store primitive; the honest reconciled verdict is the only source."},
		{Path: "internal/execution/controller_retry.go", Symbol: "Controller retry append", Line: 94, Marker: "AppendEvent(",
			Class: ClassDurable, Reason: "Retry relaunch events append through the same guarded store primitive."},
		{Path: "internal/execution/host.go", Symbol: "ResolveAdmissionRequest", Line: 92, Marker: "NewRunRequest(",
			Class: ClassDurable, Reason: "Constructs the canonical admission request from the transport-safe Candidate/Prompt pair so every RepositoryHost Start admits exclusively through the controller's durable envelope flow."},
		{Path: "internal/store/execution.go", Symbol: "Store.CreateRun", Line: 53, Marker: "CreateRun(",
			Class: ClassDurable, Reason: "Admission record primitive: writes the immutable request.json for a new durable run. Called only by the execution controller, never by feature packages."},
		{Path: "internal/store/execution_events.go", Symbol: "Store.AppendEvent/AppendTerminalEvent", Line: 197, Marker: "AppendEvent(",
			Class: ClassDurable, Reason: "Append-only event log primitives (plain and terminal-with-outcome); called only by the execution controller internals, never by feature packages."},
		{Path: "internal/store/execution_outcomes.go", Symbol: "Store.SaveAttemptOutcome", Line: 47, Marker: "SaveAttemptOutcome(",
			Class: ClassDurable, Reason: "Immutable attempt-outcome record primitive: durably persists one admitted invocation result before the caller continues. Written only beside controller-authored terminal events."},

		// --- internal/reviewexec (admitted review transport) ---------------
		{Path: "internal/reviewexec/durable_transport.go", Symbol: "DurableTransport.Run", Line: 199, Marker: "NewRunRequest(",
			Class: ClassDurable, Reason: "Review admission transport: builds the RunRequest, starts it through the controller, validates snapshot binding, and admits completions only against verified AttemptOutcome evidence."},
		{Path: "internal/reviewexec/reviewexec.go", Symbol: "ReviewAdapter.Execute", Line: 125, Marker: "EjecutarRevision(",
			Class: ClassDurable, Reason: "Executes exactly one admitted physical invocation per controller dispatch; receives the InvocationEnvelope and forwards cancellation/tree ownership to the controller."},

		// --- internal/gate --------------------------------------------------
		{Path: "internal/gate/gate_durable.go", Symbol: "EjecutarGate/asentarTrabajosValidacion", Line: 88, Marker: "NewController(",
			Class: ClassDurable, Reason: "Gate orchestrator and sole execution path (ticket 13 R11 merged the removed legacy orchestration into it): admits ONE root run plus one settled child job per validation command, all through controller.Start with persisted parent linkage."},
		{Path: "internal/gate/gate_durable_adapters.go", Symbol: "rootRunAdapter/settledValidationAdapter", Line: 98, Marker: "agentrun.NewCapability",
			Class: ClassDurable, Reason: "Gate execution adapters receive the admitted InvocationEnvelope on every controller dispatch; neither spawns anything nor mutates state outside controller APIs."},
		{Path: "internal/gate/gate_run_plan.go", Symbol: "BuildGateRunPlan", Line: 134, Marker: "NewRunRequest(",
			Class: ClassDurable, Reason: "Deterministic plan construction: builds the admission requests (candidate/prompt/capabilities) later admitted verbatim by the controller."},

		// --- internal/review -------------------------------------------------
		{Path: "internal/review/engine.go", Symbol: "invokeReview/ejecutarConReintento/refutarHallazgosCriticos", Line: 477, Marker: "EjecutarRevision(",
			Class: ClassShared, Reason: "Engine-level injection seam behind OpcionesAuditoria.ReviewTransport. Since ticket 13 (R11) removed the review.durable_runs switch, production wiring always supplies the admitted durable transport (the CRITICAL refuter routes through it unconditionally); the direct restricted call with its transport retry survives only as a defensive fallback for direct-call fixtures."},
		{Path: "internal/review/rama.go", Symbol: "overviewDeRama", Line: 418, Marker: "EjecutarPrompt(",
			Class: ClassHelper, Reason: "Branch-overview coherence prompt for the ADVISORY `pr review` report. It shapes operator-facing narrative only: overview failure degrades to the safe decision-chain fallback and can never flip a gate outcome or a commit-blocking verdict. Recorded as a follow-up candidate should pr review ever become enforcement."},
		{Path: "internal/review/snapshot.go", Symbol: "snapshot reader", Line: 31, Marker: "exec.Command",
			Class: ClassInfra, Reason: "Git plumbing feeding reviewer context snapshots."},

		// --- advisory/narrative helpers --------------------------------------
		{Path: "internal/modelprobe/verificador.go", Symbol: "Verificador.Verificar", Line: 44, Marker: "EjecutarPrompt(",
			Class: ClassHelper, Reason: "One-shot model identity probe; records a mismatch in the profile store and explicitly never affects the caller's review request."},
		{Path: "internal/ops/verificar.go", Symbol: "Verificar delegar mode", Line: 148, Marker: "EjecutarPrompt(",
			Class: ClassHelper, Reason: "Advisory tested-contract delegation; every failure degrades to ModoOmitido. Verification never blocks (aviso, nunca bloqueo)."},
		{Path: "internal/validation/validacion.go", Symbol: "delegarSinCapabilities", Line: 229, Marker: "EjecutarPrompt(",
			Class: ClassHelper, Reason: "Delegated validation profile fallback. Structurally incapable of influencing gate outcomes: delegated runs (capabilityDelegada) are excluded from Fallo AND Hallazgos before any blocking decision, and every error path returns an empty list. Narrative evidence only, by documented design."},
		{Path: "internal/agentshell/agentshell.go", Symbol: "system shell runner", Line: 33, Marker: "exec.Command",
			Class: ClassHelper, Reason: "Shell executor beneath the advisory delegated contracts (ops.Verificar / validation delegation); consumers are advisory-only, so the shell itself gates nothing."},

		// --- non-agent infrastructure ----------------------------------------
		{Path: "internal/change/perfil.go", Symbol: "change profiling", Line: 137, Marker: "exec.Command",
			Class: ClassInfra, Reason: "Git diff/tree-hash plumbing for change profiles."},
		{Path: "internal/git/gitdir.go", Symbol: "ObtenerGitCommonDir", Line: 32, Marker: "exec.Command",
			Class: ClassInfra, Reason: "Git common-dir discovery backing the shared durable store location."},
		{Path: "internal/git/parent.go", Symbol: "git command runner", Line: 242, Marker: "exec.Command",
			Class: ClassInfra, Reason: "Generic git plumbing."},
		{Path: "internal/git/plan.go", Symbol: "slice commit plumbing", Line: 384, Marker: "exec.Command",
			Class: ClassInfra, Reason: "git add/commit plumbing for approved slice batches (--no-verify by design). Message generation above is classified separately as a helper."},
		{Path: "internal/git/selection_apply.go", Symbol: "selection apply plumbing", Line: 396, Marker: "exec.Command",
			Class: ClassInfra, Reason: "Git plumbing applying approved selections."},
		{Path: "internal/git/slice.go", Symbol: "diff/measurement plumbing", Line: 164, Marker: "exec.Command",
			Class: ClassInfra, Reason: "Git diff and volume measurement plumbing."},
		{Path: "internal/graph/codegraph.go", Symbol: "codegraph CLI client", Line: 154, Marker: "exec.Command",
			Class: ClassInfra, Reason: "CodeGraph enrichment binary; metadata only, never a provider agent."},
		{Path: "internal/graph/native.go", Symbol: "native graph plumbing", Line: 387, Marker: "exec.Command",
			Class: ClassInfra, Reason: "Git plumbing for the native graph analyzer."},
		{Path: "internal/ops/events.go", Symbol: "gh pr view probe", Line: 201, Marker: "exec.Command",
			Class: ClassInfra, Reason: "GitHub CLI state probe for ops events."},
		{Path: "internal/planning/context.go", Symbol: "planning context", Line: 34, Marker: "exec.Command",
			Class: ClassInfra, Reason: "Git plumbing for planning context."},
		{Path: "internal/process/process.go", Symbol: "process runner", Line: 158, Marker: "exec.Command",
			Class: ClassInfra, Reason: "Low-level context-aware process runner beneath the provider seam; owns tree accounting, not agents."},
		{Path: "internal/setup/github.go", Symbol: "gh auth token", Line: 53, Marker: "exec.Command",
			Class: ClassInfra, Reason: "Installer credential probe."},
		{Path: "internal/setup/install.go", Symbol: "installer", Line: 198, Marker: "exec.Command",
			Class: ClassInfra, Reason: "go install/GOPATH/PATH installer plumbing."},
		{Path: "internal/setup/uninstall.go", Symbol: "uninstaller", Line: 107, Marker: "exec.Command",
			Class: ClassInfra, Reason: "PATH cleanup plumbing."},
		{Path: "internal/setup/upgrade.go", Symbol: "upgrader", Line: 163, Marker: "exec.Command",
			Class: ClassInfra, Reason: "Binary version probe for upgrades."},
		{Path: "tools/release/main.go", Symbol: "release tooling", Line: 89, Marker: "exec.Command",
			Class: ClassInfra, Reason: "Release asset tooling outside the sentinel runtime."},
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
