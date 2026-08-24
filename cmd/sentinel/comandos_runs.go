package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/daemon"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/process"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

func runExitCode(err error) int {
	switch {
	case err == nil:
		return runExitSuccess
	case errors.Is(err, store.ErrExecutionNotFound):
		return runExitRunNotFound
	case errors.Is(err, execution.ErrStaleRevision):
		return runExitStaleRevision
	case errors.Is(err, execution.ErrRunNotActive),
		errors.Is(err, execution.ErrRunNotRetryable),
		errors.Is(err, execution.ErrRunNotRecoverable),
		errors.Is(err, execution.ErrDecisionNotPending),
		errors.Is(err, execution.ErrUnsupportedAction),
		errors.Is(err, execution.ErrRunAlreadyExists):
		return runExitInvalidState
	default:
		return runExitInfrastructure
	}
}

func executeRuns(out io.Writer, worktree string, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(out, runsUsage)
		return runExitUsage
	}
	subcommand, flags := args[0], args[1:]
	switch subcommand {
	case "start":
		return executeRunsStart(out, worktree, flags)
	case "status":
		return executeRunsStatus(out, worktree, flags)
	case "logs":
		return executeRunsLogs(out, worktree, flags)
	case "respond":
		return executeRunsRespond(out, worktree, flags)
	case "abort":
		return executeRunsAbort(out, worktree, flags)
	case "retry":
		return executeRunsRetry(out, worktree, flags)
	case "recover":
		return executeRunsRecover(out, worktree, flags)
	case "verify":
		return executeRunsVerify(out, worktree, flags)
	case "attach":
		return executeRunsAttach(out, worktree, flags)
	case "daemon":
		return executeRunsDaemon(out, worktree, flags)
	case "prune":
		return executeRunsPrune(out, worktree, flags)
	default:
		fmt.Fprintf(out, "❌ Unknown subcommand for 'sentinel runs': %q\n%s", subcommand, runsUsage)
		return runExitUsage
	}
}

// runsFlag identifies one declared flag of the `sentinel runs` CLI surface.
// Every flag except --json consumes exactly one value.
type runsFlag string

const (
	flagJSON             runsFlag = "--json"
	flagRun              runsFlag = "--run"
	flagRepair           runsFlag = "--repair"
	flagText             runsFlag = "--text"
	flagPrompt           runsFlag = "--prompt"
	flagPolicyID         runsFlag = "--policy-id"
	flagAfter            runsFlag = "--after"
	flagLimit            runsFlag = "--limit"
	flagExpectedRevision runsFlag = "--expected-revision"
	flagOlderThan        runsFlag = "--older-than"
	flagFollow           runsFlag = "--follow"
)

// runsSubcommandFlags is the single source of truth for which flags each
// `runs` subcommand accepts (ticket 13 hardening pool, JD-R10 W2). The
// parser rejects anything not declared here with a usage error instead of
// silently ignoring it, so a stray --older-than outside prune can never
// degrade into a misread operator intent.
var runsSubcommandFlags = map[string][]runsFlag{
	"start":   {flagPrompt, flagPolicyID, flagJSON},
	"status":  {flagRun, flagJSON},
	"logs":    {flagRun, flagAfter, flagLimit, flagJSON},
	"respond": {flagRun, flagText, flagJSON},
	"abort":   {flagRun, flagJSON},
	"retry":   {flagRun, flagExpectedRevision, flagJSON},
	"recover": {flagRun, flagExpectedRevision, flagRepair, flagJSON},
	"verify":  {flagRun, flagJSON},
	"attach":  {flagRun, flagAfter, flagFollow},
	"prune":   {flagOlderThan, flagJSON},
}

func parseRunOptions(subcommand string, args []string) (runOptions, error) {
	options := runOptions{policyID: runsDefaultPolicyID, limit: runsLogsDefaultLimit}
	declared, ok := runsSubcommandFlags[subcommand]
	if !ok {
		return options, fmt.Errorf("unknown subcommand for 'sentinel runs': %s", subcommand)
	}
	accepted := make(map[runsFlag]bool, len(declared))
	for _, flag := range declared {
		accepted[flag] = true
	}
	for i := 0; i < len(args); i++ {
		flag := args[i]
		if !accepted[runsFlag(flag)] {
			return options, fmt.Errorf("flag %s is not accepted by 'sentinel runs %s'", flag, subcommand)
		}
		if runsFlag(flag) == flagJSON {
			options.jsonOut = true
			continue
		}
		if runsFlag(flag) == flagFollow {
			// Boolean flag: it consumes no value, exactly like --json.
			options.follow = true
			continue
		}
		if i+1 >= len(args) {
			return options, fmt.Errorf("flag %s requires a value", flag)
		}
		value := args[i+1]
		var err error
		switch runsFlag(flag) {
		case flagRun:
			options.runID = value
		case flagRepair:
			options.repairID = value
			options.repairSet = true
		case flagText:
			options.text = value
		case flagPrompt:
			options.prompt = value
		case flagPolicyID:
			options.policyID = value
		case flagAfter:
			var parsed uint64
			if parsed, err = strconv.ParseUint(value, 10, 64); err != nil {
				err = fmt.Errorf("flag --after requires a non-negative integer, received %q", value)
			}
			options.afterCursor = parsed
		case flagLimit:
			var parsed uint64
			if parsed, err = strconv.ParseUint(value, 10, 64); err == nil && (parsed == 0 || parsed > math.MaxInt32) {
				err = fmt.Errorf("flag --limit requires a positive integer, received %q", value)
			}
			options.limit = int(parsed)
		case flagExpectedRevision:
			var parsed uint64
			if parsed, err = strconv.ParseUint(value, 10, 64); err != nil {
				err = fmt.Errorf("flag --expected-revision requires a non-negative integer, received %q", value)
			}
			options.expectedRevision = parsed
			options.expectedRevisionSet = true
		case flagOlderThan:
			options.olderThan = value
			options.olderThanSet = true
		default:
			err = fmt.Errorf("unknown flag for 'sentinel runs %s': %s", subcommand, flag)
		}
		if err != nil {
			return options, err
		}
		i++
	}
	return options, nil
}

func buildRunsStore(worktree string) (*store.Store, error) {
	gitCommonDir, err := git.ObtenerGitCommonDir(worktree)
	if err != nil {
		return nil, err
	}
	return store.NuevoStore(gitCommonDir), nil
}

func buildReadonlyController(worktree string) (*store.Store, *execution.Controller, error) {
	backing, err := buildRunsStore(worktree)
	if err != nil {
		return nil, nil, err
	}
	return backing, execution.NewController(backing, nil), nil
}

func buildRunsController(worktree string) (*execution.Controller, error) {
	backing, err := buildRunsStore(worktree)
	if err != nil {
		return nil, err
	}
	agent, err := newAgentForRuns(worktree)
	if err != nil {
		return nil, err
	}
	promptAdapter, err := newPromptRunAdapter(agent)
	if err != nil {
		return nil, err
	}
	return execution.NewController(backing, promptAdapter), nil
}

// daemonEndpointForRuns resolves the repository-local daemon endpoint for
// runs commands: it loads endpoint.json from <git-common-dir>/vas-sentinel/
// daemon (daemon.Dir), dials it, and performs the authenticated handshake
// bound to FingerprintRepository(gitCommonDir). ANY failure along that chain
// — missing or corrupt record, unreachable endpoint, handshake rejection —
// degrades into resolved=false so callers fall back cleanly; a daemon
// problem is never an operator-visible runs error.
//
// Residual honesty (accepted for this slice): while a daemon owns admission,
// another process invoking `runs retry` or `runs recover` still operates
// through its own controller over the shared locked storage instead of the
// daemon's serialized admission — acceptable single-operator behavior,
// revisited when the port grows those operations.
func daemonEndpointForRuns(worktree string) (execution.RepositoryHost, func(), bool) {
	gitCommonDir, err := git.ObtenerGitCommonDir(worktree)
	if err != nil {
		return nil, nil, false
	}
	endpoint, _, err := daemon.LoadEndpoint(daemon.Dir(gitCommonDir))
	if err != nil {
		return nil, nil, false
	}
	host, err := daemon.DialRemoteHost(endpoint, daemon.FingerprintRepository(gitCommonDir))
	if err != nil {
		return nil, nil, false
	}
	return host, func() { _ = host.Close() }, true
}

// runsHostWithDaemonPreference picks the host a runs handler routes its
// repository-host operations through: a healthy daemon endpoint when one
// resolves, otherwise NewInProcessHost(controller) exactly as before the
// transport existed. The returned teardown must be deferred by the caller;
// it closes the remote connection when one was dialed and is a no-op
// otherwise. Outputs stay byte-compatible either way because both hosts read
// and write the same durable storage under the same lock discipline.
func runsHostWithDaemonPreference(worktree string, controller *execution.Controller) (execution.RepositoryHost, func()) {
	remote, teardown, resolved := daemonEndpointForRuns(worktree)
	if resolved && remote != nil {
		return remote, teardown
	}
	return execution.NewInProcessHost(controller), func() {}
}

// enforcementCapabilityName names the admission capability that carries the
// prompt delegate's validated enforcement declaration into durable records.
const enforcementCapabilityName = "agent.enforcement"

// runsAdmissionCapabilities reports the additional capabilities an
// operator-started prompt run stamps onto its admission request. When the
// configured prompt delegate declares an enforcement backend through
// EnforcementDeclaration() (AcpxBridge does), one agent.enforcement
// capability carries the declaration so CreateRun persists it among the
// request capability identities — mirroring how the gate path stamps its
// planned capabilities via agentrun.NewCapability. Delegates without the
// interface contribute nothing and their admissions stay byte-identical; a
// delegate implementing the interface but returning an empty declaration is
// skipped too, because nothing may be invented on a durable record
// (production adapters normalize their declaration to "none" instead).
func runsAdmissionCapabilities(worktree string) ([]agentrun.Capability, error) {
	agent, err := newAgentForRuns(worktree)
	if err != nil {
		return nil, err
	}
	declarer, ok := agent.(interface {
		EnforcementDeclaration() string
	})
	if !ok {
		return nil, nil
	}
	declaration := strings.TrimSpace(declarer.EnforcementDeclaration())
	if declaration == "" {
		return nil, nil
	}
	return []agentrun.Capability{
		agentrun.NewCapability(enforcementCapabilityName, map[string]string{"declaration": declaration}),
	}, nil
}

// runsRelayedAdmission reports whether a repository-local daemon endpoint is
// configured for worktree, i.e. whether Start admission may travel to the
// remote host. Capabilities cannot cross the daemon wire yet: RunRequest is
// deliberately unexported identity-canonical data, so a canonical envelope
// arrives server-side as its zero value and ValidateStartRequest would refuse
// the mixed form. Relayed admissions therefore keep the explicit
// Candidate/Prompt form without the enforcement capability until
// capability-policy runtime work extends the transport; only the local
// in-process path stamps today. A configured-but-down endpoint conservatively
// skips stamping instead of guessing which host will serve admission.
func runsRelayedAdmission(worktree string) bool {
	gitCommonDir, err := git.ObtenerGitCommonDir(worktree)
	if err != nil {
		return false
	}
	if _, _, err := daemon.LoadEndpoint(daemon.Dir(gitCommonDir)); err != nil {
		return false
	}
	return true
}

// runsStartAdmission builds the admission envelope of one operator-started
// prompt run. Without stamped capabilities — or when a daemon endpoint may
// relay the envelope to the remote host — it stays exactly on the
// transport-safe explicit Candidate/Prompt form used before enforcement
// stamping existed. Only local in-process admission promotes a stamped
// request into the canonical form carrying the capabilities, because
// ValidateStartRequest forbids mixing the two payload forms.
func runsStartAdmission(candidate, prompt, policyID, principal string, capabilities []agentrun.Capability, relayed bool) execution.StartRequest {
	admission := execution.StartRequest{
		Candidate:   candidate,
		Prompt:      prompt,
		Policy:      store.RunPolicy{ID: policyID},
		AuthContext: execution.AuthContext{Principal: principal},
	}
	if len(capabilities) == 0 || relayed {
		return admission
	}
	return execution.StartRequest{
		Request:     agentrun.NewRunRequest(agentrun.Candidate(candidate), agentrun.Prompt(prompt), capabilities),
		Policy:      store.RunPolicy{ID: policyID},
		AuthContext: execution.AuthContext{Principal: principal},
	}
}

func newPromptRunAdapter(agent agentadapter.AdaptadorPrompt) (promptRunAdapter, error) {
	if agent == nil {
		return promptRunAdapter{}, errors.New("the configured agent adapter cannot execute arbitrary prompts")
	}
	return promptRunAdapter{delegate: agent}, nil
}

func (a promptRunAdapter) Execute(ctx context.Context, job agentrun.LogicalJob, _ agentrun.InvocationEnvelope, response string) (execution.AdapterResult, error) {
	prompt := string(job.Request().Prompt())
	if strings.TrimSpace(response) != "" {
		prompt += "\n\n" + response
	}
	output, err := a.runPrompt(ctx, prompt)
	if err != nil {
		return execution.AdapterResult{}, execution.NewAdapterError(classifyOperationalError(err), err)
	}
	return execution.AdapterResult{Output: output}, nil
}

// runPrompt prefers the caller-supplied controller context over the legacy
// detached contract: when the delegate can honor a context (the acpx family
// does through EjecutarPromptWithContext), cancellation propagates down to
// the owned child process tree; delegates speaking only the legacy
// EjecutarPrompt contract behave exactly as before.
func (a promptRunAdapter) runPrompt(ctx context.Context, prompt string) (string, error) {
	if contextual, ok := a.delegate.(interface {
		EjecutarPromptWithContext(context.Context, string) (string, error)
	}); ok {
		if ctx == nil {
			ctx = context.Background()
		}
		return contextual.EjecutarPromptWithContext(ctx, prompt)
	}
	return a.delegate.EjecutarPrompt(prompt)
}

// OwnedTree forwards tree discovery to the delegate when it owns a live
// child process tree, mirroring how agenteObservado forwards the same
// contract in autoria.go. The execution controller discovers this
// TreeProvider shape structurally, so `sentinel runs abort` escalates
// against exactly the tree this adapter spawned instead of finding nothing.
func (a promptRunAdapter) OwnedTree() *process.Tree {
	if provider, ok := a.delegate.(interface {
		OwnedTree() *process.Tree
	}); ok {
		return provider.OwnedTree()
	}
	return nil
}

func classifyOperationalError(err error) agentrun.OutcomeClass {
	switch {
	case errors.Is(err, context.Canceled):
		return agentrun.OutcomeCancellation
	case errors.Is(err, context.DeadlineExceeded):
		return agentrun.OutcomeTimeout
	default:
		return agentrun.OutcomeFailure
	}
}

func encodeStableJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
