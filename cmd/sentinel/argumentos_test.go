package main

import (
	"strings"
	"testing"
)

// TestSubcomandosSinFlagsRechazanArgumentosExtra recorre los nueve
// subcomandos que no admiten argumentos (H1/B6). Hasta T0.5, `sentinel check
// --loquesea` salía 0 sin protestar.
func TestSubcomandosSinFlagsRechazanArgumentosExtra(t *testing.T) {
	subcomandos := []string{
		"init", "uninit", "check", "slice",
		"lint", "rebase", "install", "upgrade", "uninstall",
	}
	for _, subcomando := range subcomandos {
		t.Run(subcomando, func(t *testing.T) {
			if mensaje := validarArgumentos(subcomando, nil); mensaje != "" {
				t.Errorf("sin argumentos debería aceptarse, obtuve: %s", mensaje)
			}
			mensaje := validarArgumentos(subcomando, []string{"--loquesea"})
			if mensaje == "" {
				t.Fatal("aceptó un argumento desconocido en silencio")
			}
			if !strings.Contains(mensaje, "--loquesea") {
				t.Errorf("el mensaje no nombra el argumento rechazado: %s", mensaje)
			}
			if !strings.Contains(mensaje, subcomando) {
				t.Errorf("el mensaje no nombra el subcomando: %s", mensaje)
			}
		})
	}
}

// TestSliceAceptaSusSubcomandos: slice a secas no admite argumentos, pero
// `slice plan` y `slice apply` sí son válidos y tienen sus propios flags.
func TestSliceAceptaSusSubcomandos(t *testing.T) {
	casos := []struct {
		nombre   string
		extras   []string
		aceptado bool
	}{
		{"slice a secas", nil, true},
		{"slice plan", []string{"plan"}, true},
		{"slice plan con flags", []string{"plan", "--json"}, true},
		{"slice apply con flags", []string{"apply", "--plan", "p.json"}, true},
		{"slice con basura", []string{"loquesea"}, false},
		{"slice con flag suelto", []string{"--json"}, false},
	}
	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			mensaje := validarArgumentos("slice", caso.extras)
			if caso.aceptado && mensaje != "" {
				t.Errorf("debería aceptarse, obtuve: %s", mensaje)
			}
			if !caso.aceptado && mensaje == "" {
				t.Error("debería rechazarse y se aceptó en silencio")
			}
		})
	}
}

// TestSubcomandosConFlagsPropiosNoSeTocan: review, status y pr parsean sus
// propios argumentos, así que el dispatcher no debe adelantarse a rechazarlos.
func TestSubcomandosConFlagsPropiosNoSeTocan(t *testing.T) {
	for _, subcomando := range []string{"review", "status", "pr"} {
		if mensaje := validarArgumentos(subcomando, []string{"--json", "HEAD~1"}); mensaje != "" {
			t.Errorf("%s: el dispatcher no debe validar sus flags, obtuve: %s", subcomando, mensaje)
		}
	}
}

// TestMensajeDeRechazoRemiteAlaAyuda: el mensaje tiene que decir qué hacer,
// no solo que algo está mal.
func TestMensajeDeRechazoRemiteAlaAyuda(t *testing.T) {
	mensaje := validarArgumentos("check", []string{"--loquesea"})
	if !strings.Contains(mensaje, "sentinel help") {
		t.Errorf("el mensaje no remite a la ayuda: %s", mensaje)
	}
}
