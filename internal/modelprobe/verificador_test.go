package modelprobe

import (
	"errors"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

type agenteModeloFalso struct {
	modelo string
	err    error
}

func (a agenteModeloFalso) EjecutarPrompt(string) (string, error) {
	return a.modelo, a.err
}

type agenteModeloConfiguradoFalso struct {
	agenteModeloFalso
	esperado string
}

func (a agenteModeloConfiguradoFalso) ModeloConfigurado() (string, bool) {
	return a.esperado, true
}

type agenteModeloConfiguradoNoDisponible struct {
	agenteModeloFalso
}

func (agenteModeloConfiguradoNoDisponible) ModeloConfigurado() (string, bool) {
	return "openai/gpt-5.6-terra", false
}

func TestVerificadorRegistraSoloDesajustesConModelosValidos(t *testing.T) {
	tests := []struct {
		name     string
		profile  string
		expected string
		agent    Agente
		stored   *store.Profile
	}{
		{
			name:     "mismatch persists normalized values",
			profile:  "normal",
			expected: "openai/gpt-5.6-terra",
			agent:    agenteModeloFalso{modelo: "  openai/gpt-5.6-sol  "},
			stored: &store.Profile{
				Name:          "normal",
				Status:        store.ProfileUnverified,
				Event:         "model_mismatch",
				ExpectedModel: "openai/gpt-5.6-terra",
				ActualModel:   "openai/gpt-5.6-sol",
			},
		},
		{
			name:     "matching surrounding whitespace is ignored",
			profile:  "normal",
			expected: "openai/gpt-5.6-terra",
			agent:    agenteModeloFalso{modelo: "  openai/gpt-5.6-terra\t"},
		},
		{
			name:     "mismatch with trailing LF is recorded",
			profile:  "normal",
			expected: "openai/gpt-5.6-terra",
			agent:    agenteModeloFalso{modelo: "openai/gpt-5.6-sol\n"},
			stored: &store.Profile{
				Name:          "normal",
				Status:        store.ProfileUnverified,
				Event:         "model_mismatch",
				ExpectedModel: "openai/gpt-5.6-terra",
				ActualModel:   "openai/gpt-5.6-sol",
			},
		},
		{
			name:     "mismatch with trailing CRLF is recorded",
			profile:  "normal",
			expected: "openai/gpt-5.6-terra",
			agent:    agenteModeloFalso{modelo: "openai/gpt-5.6-sol\r\n"},
			stored: &store.Profile{
				Name:          "normal",
				Status:        store.ProfileUnverified,
				Event:         "model_mismatch",
				ExpectedModel: "openai/gpt-5.6-terra",
				ActualModel:   "openai/gpt-5.6-sol",
			},
		},
		{
			name:     "agent error is ignored",
			profile:  "normal",
			expected: "openai/gpt-5.6-terra",
			agent:    agenteModeloFalso{err: errors.New("unavailable")},
		},
		{
			name:    "unavailable configured model is ignored",
			profile: "normal",
			agent: agenteModeloConfiguradoNoDisponible{
				agenteModeloFalso: agenteModeloFalso{modelo: "openai/gpt-5.6-sol"},
			},
		},
		{
			name:    "available configured model is used",
			profile: "normal",
			agent: agenteModeloConfiguradoFalso{
				agenteModeloFalso: agenteModeloFalso{modelo: "openai/gpt-5.6-sol"},
				esperado:          "openai/gpt-5.6-terra",
			},
			stored: &store.Profile{
				Name:          "normal",
				Status:        store.ProfileUnverified,
				Event:         "model_mismatch",
				ExpectedModel: "openai/gpt-5.6-terra",
				ActualModel:   "openai/gpt-5.6-sol",
			},
		},
		{
			name:     "invalid response is ignored",
			profile:  "normal",
			expected: "openai/gpt-5.6-terra",
			agent:    agenteModeloFalso{modelo: "openai/gpt-5.6-sol\nuntrusted"},
		},
		{
			name:     "literal 129 character response is ignored",
			profile:  "normal",
			expected: "openai/gpt-5.6-terra",
			agent:    agenteModeloFalso{modelo: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := store.NuevoStore(t.TempDir())
			v := NuevoVerificador(s)

			v.Verificar(tt.profile, tt.expected, tt.agent)

			profile, err := s.LeerPerfil(tt.profile)
			if err != nil {
				t.Fatalf("LeerPerfil: %v", err)
			}
			if tt.stored == nil {
				if profile != nil {
					t.Errorf("profile = %+v, want nil", profile)
				}
				return
			}
			if profile == nil {
				t.Fatal("the mismatched profile was not recorded")
			}
			if *profile != *tt.stored {
				t.Errorf("profile = %+v, want %+v", profile, tt.stored)
			}
		})
	}
}

func TestModeloReportadoValidoAceptaLiteralDe128Caracteres(t *testing.T) {
	modelo, ok := modeloReportadoValido("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if !ok {
		t.Fatal("modeloReportadoValido rejected a 128 character identifier")
	}
	if len(modelo) != 128 {
		t.Errorf("len(modelo) = %d, want 128", len(modelo))
	}
}

func TestVerificadorSoloSondeaUnaVezPorPerfil(t *testing.T) {
	s := store.NuevoStore(t.TempDir())
	v := NuevoVerificador(s)
	agent := agenteModeloFalso{modelo: "openai/gpt-5.6-terra"}

	v.Verificar("normal", "openai/gpt-5.6-terra", agent)
	v.Verificar("normal", "openai/gpt-5.6-sol", agent)

	profile, err := s.LeerPerfil("normal")
	if err != nil {
		t.Fatalf("LeerPerfil: %v", err)
	}
	if profile != nil {
		t.Errorf("profile = %+v, want nil", profile)
	}
}
