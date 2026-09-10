package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/consent"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/intent"
)

// pendingDecisionsExitCode is `slice plan`'s exit code when decisions only
// the user can answer remain. It is distinct from 1 (error) so an orchestrator
// knows it must ask, not that something failed.
const pendingDecisionsExitCode = 3

var newAgentAdapterForMessage = agentadapter.NewAgentAdapterForMessage
var buildSlicePlan = git.BuildPlanForAgentWithOptions

// runSlicePlan emits the fragmentation plan without committing anything and
// returns the exit code: 0 when no decisions are pending, 3 when some are.
// The interactive mode of `sentinel slice` stays intact: this is an added
// path, not a replacement.
func runSlicePlan(out io.Writer, args []string) int {
	asJSON := false
	declaredText := ""
	hasDeclared := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			asJSON = true
		case "--intent":
			if hasDeclared || i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "--") {
				fmt.Fprintln(out, "❌ 'slice plan' accepts a single --intent with a non-empty value.")
				return 1
			}
			hasDeclared = true
			declaredText = args[i+1]
			i++
		case "--intent-transcript", "--transcript-consent":
			// Withdrawn from piece 1 on 2026-09-10, see docs/issues/actionable.md
			// item 15. Refused by name rather than reported as unknown, so an
			// operator or script that used it learns what happened.
			fmt.Fprintf(out, "❌ %s was withdrawn: deriving the intent from a transcript is not part of this command today. Use --intent \"<what the work was for>\".\n", args[i])
			return 1
		default:
			fmt.Fprintf(out, "❌ Unknown option for 'slice plan': %s\n", args[i])
			return 1
		}
	}

	declaredIntent := intent.Intent{}
	var err error
	if hasDeclared {
		declaredIntent, err = intent.Normalize(declaredText, intent.SourceDeclared)
		if err != nil {
			fmt.Fprintf(out, "❌ Invalid --intent: %v\n", err)
			return 1
		}
	}

	root, err := git.GetWorktreeRoot()
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return 1
	}
	var adapter agentadapter.AgentAdapter
	// The micro-diff contains source code: it requires the versioned request
	// and the user's local consent for this repository.
	//
	// The error is not discarded. A constructor can return a non-nil adapter
	// alongside an error — a chain that resolved some agents and failed on
	// others does exactly that — and passing it on would send the diff to a
	// half-built adapter. Failing closed here is what the withdrawn transcript
	// path did explicitly, and dropping it with the rest was a regression.
	if allowsExternalAgentDiff(root) {
		built, err := newAgentAdapterForMessage(root)
		if err != nil {
			built = nil
		}
		adapter = built
	}

	options := git.SemanticSliceOptions{
		Intent:       declaredIntent.Text,
		IntentSource: declaredIntent.Source,
	}
	plan, err := buildSlicePlan(adapter, options)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return 1
	}

	if asJSON {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(plan); err != nil {
			fmt.Fprintf(out, "❌ Could not serialize the plan: %v\n", err)
			return 1
		}
	} else {
		printSerializedPlan(out, plan)
	}

	if len(plan.PendingDecisions) > 0 {
		return pendingDecisionsExitCode
	}
	return 0
}

func allowsExternalAgentDiff(root string) bool {
	if !config.RepositoryRequestsExternalAgentDiff(root) {
		return false
	}
	status, err := consent.ExternalDiffStatus(root)
	return err == nil && status.Granted
}

// runSliceApply executes a previously emitted plan, only with the user's
// explicit answers. It returns 0 when it committed, 1 when it refused.
func runSliceApply(out io.Writer, args []string) int {
	planPath, answersPath := "", ""
	for i := 0; i < len(args); i++ {
		value := ""
		if i+1 < len(args) {
			value = args[i+1]
		}
		switch args[i] {
		case "--plan":
			planPath, i = value, i+1
		case "--answers":
			answersPath, i = value, i+1
		default:
			fmt.Fprintf(out, "❌ Unknown option for 'slice apply': %s\n", args[i])
			return 1
		}
	}
	if planPath == "" || answersPath == "" {
		fmt.Fprintln(out, "❌ "+sliceApplyUsage)
		return 1
	}

	var plan git.SerializedPlan
	if err := readJSON(planPath, &plan); err != nil {
		fmt.Fprintf(out, "❌ Could not read the plan: %v\n", err)
		return 1
	}
	var answers git.PlanAnswers
	if err := readJSON(answersPath, &answers); err != nil {
		fmt.Fprintf(out, "❌ Could not read the answers: %v\n", err)
		return 1
	}

	for _, automatic := range plan.AutomaticDecisions {
		fmt.Fprintf(out, "⚙️ Automatic bypass granted by the plan: %s (%d lines, %s)\n", automatic.ID, automatic.Lines, automatic.Reason)
	}

	results, err := git.ApplyApprovedPlan(&plan, answers)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return 1
	}
	for _, result := range results {
		fmt.Fprintf(out, "✅ %s [%s] %s (%d files)\n", result.Hash, result.Layer, result.Message, result.Files)
	}
	fmt.Fprintf(out, "\n🎉 %d commits created from the approved plan.\n", len(results))
	return 0
}

func readJSON(path string, target any) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(content, target)
}

func printSerializedPlan(out io.Writer, plan *git.SerializedPlan) {
	fmt.Fprintf(out, "🧭 Plan %s (tree %s)\n", plan.PlanID[:12], plan.WorktreeState[:12])
	if plan.Intent != "" {
		fmt.Fprintf(out, "🎯 Intent (%s): %s\n", plan.IntentSource, plan.Intent)
	}
	for _, warning := range plan.Warnings {
		prefix := "⚠️"
		if strings.HasPrefix(warning, "Transcript sent to") {
			prefix = "ℹ️"
		}
		fmt.Fprintf(out, "%s %s\n", prefix, warning)
	}
	if plan.Explanation != "" {
		fmt.Fprintf(out, "ℹ️ %s\n", plan.Explanation)
	}
	if len(plan.Batches) == 0 {
		fmt.Fprintln(out, "📭 No pending modifications to process.")
	}
	for _, batch := range plan.Batches {
		fmt.Fprintf(out, "\n📦 Batch #%d [%s] — %d lines\n", batch.Number, batch.Layer, batch.Lines)
		fmt.Fprintf(out, "   💬 %s\n", batch.Message)
		for _, path := range batch.Paths {
			fmt.Fprintf(out, "   • %s\n", path)
		}
	}
	for _, automatic := range plan.AutomaticDecisions {
		fmt.Fprintf(out, "\n⚙️ Automatic bypass %s — %d lines (%s)\n   %s: the plan grants it without asking because the unit cannot be divided safely.\n", automatic.ID, automatic.Lines, automatic.File, automatic.Reason)
	}
	for _, decision := range plan.PendingDecisions {
		fmt.Fprintf(out, "\n❓ Pending decision %s\n   %s\n   Options: %v\n", decision.ID, decision.Question, decision.Options)
	}
	if len(plan.PendingDecisions) > 0 {
		fmt.Fprintln(out, "\n⚠️ The plan cannot be applied until the user answers every decision.")
	}
}
