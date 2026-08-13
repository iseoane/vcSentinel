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

// Verificador runs at most one probe per configured profile in a session.
type Verificador struct {
	store       *store.Store
	verificados sync.Map
}

func NuevoVerificador(s *store.Store) *Verificador {
	return &Verificador{store: s}
}

// Verificar records a mismatch without affecting the caller's review request.
func (v *Verificador) Verificar(perfil, esperado string, agente Agente) {
	if perfil == "" || agente == nil || v.store == nil {
		return
	}
	if _, loaded := v.verificados.LoadOrStore(perfil, struct{}{}); loaded {
		return
	}
	actual, err := agente.EjecutarPrompt(promptModelo)
	if err != nil {
		return
	}
	if esperado == "" {
		if reporta, ok := agente.(ReportaModeloConfigurado); ok {
			if modelo, ok := reporta.ModeloConfigurado(); ok {
				esperado = modelo
			}
		}
	}
	actual, ok := modeloReportadoValido(actual)
	if esperado == "" || !ok || actual == esperado {
		return
	}
	_ = v.store.GuardarPerfil(&store.Profile{
		Name:          perfil,
		Status:        store.ProfileUnverified,
		Event:         "model_mismatch",
		ExpectedModel: esperado,
		ActualModel:   actual,
	})
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
