package agentadapter

type AgentAdapter interface {
	GetCommitMessage(paths []string, layer string, batchNum int) (string, error)
}

// PromptAdapter is the minimal interface to run an arbitrary prompt against
// the agent. The audit engine uses it and both CLIAdapter and AdapterChain
// satisfy it.
type PromptAdapter interface {
	RunPrompt(prompt string) (string, error)
}

// AdapterWithDiff is an optional interface an adapter may implement to receive
// the exact micro-diff of the staging area (git diff --cached) before
// generating the commit message. If the adapter does not implement it, the
// slice engine falls back to the base AgentAdapter interface.
type AdapterWithDiff interface {
	GetCommitMessageWithDiff(paths []string, layer string, batchNum int, diff string) (string, error)
}

// AdapterRefactor is an optional interface an adapter may implement to
// refactor a massive code file (potential SRP violation): it first proposes a
// split plan and then may apply it by editing the working tree (without
// committing).
type AdapterRefactor interface {
	// ProposeRefactorPlan returns the split plan as plain text.
	ProposeRefactorPlan(filePath string) (string, error)
	// ApplyRefactorPlan orders the agent to run the plan directly on the
	// working tree and returns a brief summary of the applied changes.
	ApplyRefactorPlan(filePath string, plan string) (string, error)
}
