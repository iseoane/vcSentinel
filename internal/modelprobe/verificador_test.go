package modelprobe

import (
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

type agenteModeloFalso struct {
	modelo string
}

func (a agenteModeloFalso) EjecutarPrompt(string) (string, error) {
	return a.modelo, nil
}

type agenteModeloConfiguradoFalso struct {
	agenteModeloFalso
	esperado string
}

func (a agenteModeloConfiguradoFalso) ModeloConfigurado() (string, bool) {
	return a.esperado, true
}

func TestVerificadorMarcaPerfilAnteDesajuste(t *testing.T) {
	s := store.NuevoStore(t.TempDir())
	v := NuevoVerificador(s)

	v.Verificar("normal", "openai/gpt-5.6-terra", agenteModeloFalso{modelo: "openai/gpt-5.6-sol"})

	perfil, err := s.LeerPerfil("normal")
	if err != nil {
		t.Fatalf("LeerPerfil: %v", err)
	}
	if perfil == nil {
		t.Fatal("the mismatched profile was not recorded")
	}
	if perfil.Status != store.ProfileUnverified {
		t.Errorf("Status = %q, want %q", perfil.Status, store.ProfileUnverified)
	}
	if perfil.Event != "model_mismatch" {
		t.Errorf("Event = %q, want model_mismatch", perfil.Event)
	}
}

func TestVerificadorUsaModeloConfiguradoPorElAdaptador(t *testing.T) {
	s := store.NuevoStore(t.TempDir())
	v := NuevoVerificador(s)

	v.Verificar("normal", "", agenteModeloConfiguradoFalso{
		agenteModeloFalso: agenteModeloFalso{modelo: "openai/gpt-5.6-sol"},
		esperado:          "openai/gpt-5.6-terra",
	})

	perfil, err := s.LeerPerfil("normal")
	if err != nil {
		t.Fatalf("LeerPerfil: %v", err)
	}
	if perfil == nil || perfil.Status != store.ProfileUnverified {
		t.Errorf("profile = %+v, want unverified", perfil)
	}
}
