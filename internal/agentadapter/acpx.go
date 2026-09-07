package agentadapter

import (
	"fmt"

	"github.com/ISeoane-Quental/vas.sentinel/internal/acpadapter"
)

// AcpxBridge presents an acpadapter.AcpxAdapter through the same structural
// contracts the factory returns for CLI adapters, so callers need zero
// changes when an agent entry declares kind: acpx. Embedding supplies
// RunPrompt, RunReview, ReviewWithContext and OwnedTree verbatim;
// this type only adds what ACP cannot inherit:
//
//   - AgentAdapter/AdapterWithDiff: commit-message generation rides the same
//     prompt builders and Conventional Commit validation as CLIAdapter.
//     Like claude/opencode, every ACP backend has tool access, so the plain
//     path is refused and the consented micro-diff path is required.
//   - ReportsEffectiveAgent: maps acpadapter.EffectiveIdentity onto this
//     package's EffectiveAgent so honest attribution records ACP runs too.
//   - String: readable chain diagnostics
//     ("acpx(npx:claude|enforcement=none)"), stating which restriction
//     backend the adapter declares alongside its identity.
type AcpxBridge struct {
	*acpadapter.AcpxAdapter

	commitLanguage string
}

// GetCommitMessage refuses the plain path on purpose: an ACP backend may
// use tools to explore the repository, and the slice flow answers with the
// micro-diff variant below, exactly as it already does for claude/opencode.
func (b *AcpxBridge) GetCommitMessage(paths []string, layer string, batchNum int) (string, error) {
	return "", fmt.Errorf("acpx requires the consented micro-diff path to generate commit messages")
}

// GetCommitMessageWithDiff generates the commit message from the prepared
// micro-diff through one prompt turn, then validates it with the SAME
// single-line Conventional Commit rule CLIAdapter applies.
func (b *AcpxBridge) GetCommitMessageWithDiff(paths []string, layer string, batchNum int, diff string) (string, error) {
	prompt := buildAgentPromptWithDiff(layer, batchNum, paths, diff, commitLanguageOrDefault(b.commitLanguage))
	output, err := b.RunPrompt(prompt)
	if err != nil {
		return "", err
	}
	return validateCommitMessage(output)
}

// EffectiveAgent reports who served the last request using the shared
// attribution vocabulary. It forwards only wire-observed ACP identity;
// configured model/effort declarations are not producer evidence.
func (b *AcpxBridge) EffectiveAgent() (EffectiveAgent, bool) {
	id := b.ObservedIdentity()
	return EffectiveAgent{
		Binary: id.Binary,
		Model:  id.Model,
		Effort: id.Effort,
	}, true
}

// String identifies the bridge in chain fallback errors and log lines. The
// validated enforcement declaration rides along so every such line states
// which backend was declared to contain the run, not only who answered.
// Durable-record threading of the declaration lands with the
// capability-policy runtime work — the store schema is outside the A2
// boundary — so this diagnostic surface is its operator-visible carrier
// today.
func (b *AcpxBridge) String() string {
	return "acpx(" + b.EffectiveIdentity().Binary + "|enforcement=" + b.EnforcementDeclaration() + ")"
}

// commitLanguageOrDefault mirrors CLIAdapter.commitLanguage for the bridge.
func commitLanguageOrDefault(language string) string {
	if language == "" {
		return DefaultLanguage
	}
	return language
}
