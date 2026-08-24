package agentadapter

import (
	"fmt"

	"github.com/ISeoane-Quental/vas.sentinel/internal/acpadapter"
)

// AcpxBridge presents an acpadapter.AcpxAdapter through the same structural
// contracts the factory returns for CLI adapters, so callers need zero
// changes when an agent entry declares kind: acpx. Embedding supplies
// EjecutarPrompt, EjecutarRevision, ReviewWithContext and OwnedTree verbatim;
// this type only adds what ACP cannot inherit:
//
//   - AgentAdapter/AdapterConDiff: commit-message generation rides the same
//     prompt builders and Conventional Commit validation as CLIAdapter.
//     Like claude/opencode, every ACP backend has tool access, so the plain
//     path is refused and the consented micro-diff path is required.
//   - ReportaAgenteEfectivo: maps acpadapter.EffectiveIdentity onto this
//     package's AgenteEfectivo so honest attribution records ACP runs too.
//   - String: readable chain diagnostics
//     ("acpx(npx:claude|enforcement=none)"), stating which restriction
//     backend the adapter declares alongside its identity.
type AcpxBridge struct {
	*acpadapter.AcpxAdapter

	commitLanguage string
}

// ObtenerMensajeCommit refuses the plain path on purpose: an ACP backend may
// use tools to explore the repository, and the slice flow answers with the
// micro-diff variant below, exactly as it already does for claude/opencode.
func (b *AcpxBridge) ObtenerMensajeCommit(rutasArchivos []string, capa string, batchNum int) (string, error) {
	return "", fmt.Errorf("acpx requires the consented micro-diff path to generate commit messages")
}

// ObtenerMensajeCommitConDiff generates the commit message from the prepared
// micro-diff through one prompt turn, then validates it with the SAME
// single-line Conventional Commit rule CLIAdapter applies.
func (b *AcpxBridge) ObtenerMensajeCommitConDiff(rutasArchivos []string, capa string, batchNum int, diff string) (string, error) {
	prompt := construirPromptAgenteConDiff(capa, batchNum, rutasArchivos, diff, commitLanguageOrDefault(b.commitLanguage))
	salida, err := b.EjecutarPrompt(prompt)
	if err != nil {
		return "", err
	}
	return validarMensajeCommit(salida)
}

// AgenteEfectivo reports who served the last request using the shared
// attribution vocabulary. It forwards acpadapter.EffectiveIdentity, which
// never invents model or effort (C1/C8).
func (b *AcpxBridge) AgenteEfectivo() (AgenteEfectivo, bool) {
	id := b.EffectiveIdentity()
	return AgenteEfectivo{
		Binario:  id.Binary,
		Modelo:   id.Model,
		Esfuerzo: id.Effort,
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

// commitLanguageOrDefault mirrors CLIAdapter.idiomaCommit for the bridge.
func commitLanguageOrDefault(idioma string) string {
	if idioma == "" {
		return IdiomaPorDefecto
	}
	return idioma
}
