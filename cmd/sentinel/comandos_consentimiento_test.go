package main

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

func TestEjecutarConsentimientoDiffCicloNoInteractivo(t *testing.T) {
	repo := t.TempDir()
	if err := exec.Command("git", "-C", repo, "init").Run(); err != nil {
		t.Fatal(err)
	}
	for _, caso := range []struct {
		accion string
		texto  string
	}{
		{"estado", "revocado"},
		{"otorgar", "otorgado"},
		{"estado", "otorgado"},
		{"revocar", "revocado"},
	} {
		var salida bytes.Buffer
		if codigo := ejecutarConsentimientoDiff(&salida, repo, []string{caso.accion}); codigo != 0 {
			t.Fatalf("%s exit = %d: %s", caso.accion, codigo, salida.String())
		}
		if !strings.Contains(strings.ToLower(salida.String()), caso.texto) {
			t.Fatalf("%s no informa %q: %s", caso.accion, caso.texto, salida.String())
		}
	}
}
