package main

import "testing"

func TestParsearFlagsAuditoriaDefault(t *testing.T) {
	flags, err := parsearFlagsAuditoria(nil)
	if err != nil {
		t.Fatalf("parsearFlagsAuditoria(nil) devolvió error: %v", err)
	}
	if flags.target != "HEAD" {
		t.Errorf("target = %q, esperado HEAD", flags.target)
	}
	if flags.all || flags.chain || flags.gate || flags.jsonOut {
		t.Error("flags booleanos deberían estar apagados por defecto")
	}
}

func TestParsearFlagsAuditoriaCompletas(t *testing.T) {
	flags, err := parsearFlagsAuditoria([]string{
		"abc123", "--dims", "logic, security", "--profile", "deep",
		"--chain", "--answer", "no aplica aqui", "--gate",
	})
	if err != nil {
		t.Fatalf("parsearFlagsAuditoria devolvió error: %v", err)
	}
	if flags.target != "abc123" {
		t.Errorf("target = %q, esperado abc123", flags.target)
	}
	if len(flags.dims) != 2 || flags.dims[0] != "logic" || flags.dims[1] != "security" {
		t.Errorf("dims = %+v, esperado [logic security]", flags.dims)
	}
	if flags.profile != "deep" || flags.answer != "no aplica aqui" {
		t.Errorf("profile/answer = %q/%q", flags.profile, flags.answer)
	}
	if !flags.chain || !flags.gate {
		t.Error("chain/gate deberían estar activos")
	}
}

func TestParsearFlagsAuditoriaErrores(t *testing.T) {
	pruebas := []struct {
		nombre string
		args   []string
	}{
		{"flag sin valor", []string{"--profile"}},
		{"opción desconocida", []string{"--nada"}},
		{"dos targets", []string{"abc", "def"}},
		{"HEAD explicito y otro target", []string{"HEAD", "abc123"}},
	}
	for _, prueba := range pruebas {
		if _, err := parsearFlagsAuditoria(prueba.args); err == nil {
			t.Errorf("%s: debería devolver error", prueba.nombre)
		}
	}
}

func TestParsearFlagsAuditoriaHeadExplicito(t *testing.T) {
	flags, err := parsearFlagsAuditoria([]string{"HEAD"})
	if err != nil {
		t.Fatalf("parsearFlagsAuditoria(HEAD) devolvió error: %v", err)
	}
	if flags.target != "HEAD" || !flags.targetOk {
		t.Errorf("target = %q targetOk = %v, esperado HEAD/true", flags.target, flags.targetOk)
	}
}

func TestStatusRechazaFlagsNoAplicables(t *testing.T) {
	pruebas := []struct {
		nombre string
		args   []string
	}{
		{"--dims", []string{"--dims", "logic"}},
		{"--profile", []string{"--profile", "deep"}},
		{"--chain", []string{"--chain"}},
		{"--gate", []string{"--gate"}},
		{"--all", []string{"--all"}},
		{"target", []string{"abc123"}},
	}
	for _, prueba := range pruebas {
		flags, err := parsearFlagsAuditoria(prueba.args)
		if err != nil {
			t.Fatalf("%s: parsearFlagsAuditoria devolvió error: %v", prueba.nombre, err)
		}
		if !flagsNoAplicablesAStatus(flags) {
			t.Errorf("%s: debería detectarse como no aplicable a status", prueba.nombre)
		}
	}
}
