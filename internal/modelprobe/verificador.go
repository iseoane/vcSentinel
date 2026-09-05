// Package modelprobe verifies the model that an agent reports for a session.
package modelprobe

import (
	"strings"
	"sync"

	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

const promptModelo = "What model are you actually using? Reply with only the exact model identifier."

const maxModelIdentifierLength = 128

// Agente is the minimal agent capability needed for a model probe.
type Agente interface {
	EjecutarPrompt(prompt string) (string, error)
}

// ReportaModeloConfigurado is implemented when an adapter can identify the
// model selected after any fallback resolution.
type ReportaModeloConfigurado interface {
	ModeloConfigurado() (string, bool)
}

// Outcome is the typed result of one model probe. Only OutcomeMatched lets
// a consumer claim the model as verified; every other outcome keeps the
// honest default of unverified.
type Outcome string

const (
	// OutcomeMatched: probed and the reported identifier equals the expected one.
	OutcomeMatched Outcome = "matched"
	// OutcomeMismatch: probed with a parseable reply that differs.
	OutcomeMismatch Outcome = "mismatch"
	// OutcomeProbeError: the probe prompt itself failed.
	OutcomeProbeError Outcome = "probe_error"
	// OutcomeUnparseable: the reply fails modeloReportadoValido.
	OutcomeUnparseable Outcome = "unparseable"
	// OutcomeSkipped: nothing to probe (empty profile, nil agent or store)
	// or no expected model resolvable.
	OutcomeSkipped Outcome = "skipped"
)

// Verificador runs at most one probe per configured profile in a session,
// retaining each outcome for later queries.
type Verificador struct {
	store       *store.Store
	verificados sync.Map // profile name -> Outcome
}

func NuevoVerificador(s *store.Store) *Verificador {
	return &Verificador{store: s}
}

// Verify probes the agent for its model and records the outcome without
// affecting the caller's review request: a probe that errors never fails a
// review, a gate, or a PR flow. It probes at most once per profile per
// session; a repeated call returns the stored first outcome.
//
// A match writes a positive profile record (status verified), giving the
// mismatch signal a counterpart and making store.LeerPerfil useful. A
// mismatch keeps writing the unverified record. Every other outcome
// records nothing.
func (v *Verificador) Verify(perfil, esperado string, agente Agente) Outcome {
	if perfil == "" || agente == nil || v.store == nil {
		return OutcomeSkipped
	}
	if outcome, loaded := v.verificados.LoadOrStore(perfil, OutcomeSkipped); loaded {
		if previous, ok := outcome.(Outcome); ok {
			return previous
		}
		return OutcomeSkipped
	}
	outcome := v.probe(perfil, esperado, agente)
	v.verificados.Store(perfil, outcome)
	return outcome
}

// Verified reports whether the profile was probed and matched in this
// session. Anything else — mismatch, error, unparseable reply, or never
// probed — is false.
func (v *Verificador) Verified(perfil string) bool {
	outcome, ok := v.verificados.Load(perfil)
	return ok && outcome == OutcomeMatched
}

func (v *Verificador) probe(perfil, esperado string, agente Agente) Outcome {
	actual, err := agente.EjecutarPrompt(promptModelo)
	if err != nil {
		return OutcomeProbeError
	}
	if esperado == "" {
		if reporta, ok := agente.(ReportaModeloConfigurado); ok {
			if modelo, ok := reporta.ModeloConfigurado(); ok {
				esperado = modelo
			}
		}
	}
	actual, ok := modeloReportadoValido(actual)
	if esperado == "" || !ok {
		if esperado == "" {
			return OutcomeSkipped
		}
		return OutcomeUnparseable
	}
	if actual == esperado {
		_ = v.store.GuardarPerfil(&store.Profile{
			Name:          perfil,
			Status:        store.ProfileVerified,
			Event:         "model_match",
			ExpectedModel: esperado,
			ActualModel:   actual,
		})
		return OutcomeMatched
	}
	_ = v.store.GuardarPerfil(&store.Profile{
		Name:          perfil,
		Status:        store.ProfileUnverified,
		Event:         "model_mismatch",
		ExpectedModel: esperado,
		ActualModel:   actual,
	})
	return OutcomeMismatch
}

func modeloReportadoValido(modelo string) (string, bool) {
	modelo = strings.TrimSpace(modelo)
	if modelo == "" || len(modelo) > maxModelIdentifierLength {
		return "", false
	}
	for _, r := range modelo {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' || r == '/') {
			return "", false
		}
	}
	return modelo, true
}
