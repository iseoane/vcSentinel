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
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
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

func newPromptRunAdapter(agent agentadapter.AdaptadorPrompt) (promptRunAdapter, error) {
	if agent == nil {
		return promptRunAdapter{}, errors.New("the configured agent adapter cannot execute arbitrary prompts")
	}
	return promptRunAdapter{delegate: agent}, nil
}

func (a promptRunAdapter) Execute(_ context.Context, job agentrun.LogicalJob, _ agentrun.InvocationEnvelope, response string) (execution.AdapterResult, error) {
	prompt := string(job.Request().Prompt())
	if strings.TrimSpace(response) != "" {
		prompt += "\n\n" + response
	}
	output, err := a.delegate.EjecutarPrompt(prompt)
	if err != nil {
		return execution.AdapterResult{}, execution.NewAdapterError(classifyOperationalError(err), err)
	}
	return execution.AdapterResult{Output: output}, nil
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
