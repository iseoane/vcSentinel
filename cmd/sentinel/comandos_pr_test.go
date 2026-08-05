package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

// TestVerboPr: el dispatch decide entre review, create y el passthrough
// legacy a gh pr create.
func TestVerboPr(t *testing.T) {
	casos := []struct {
		args []string
		want string
	}{
		{[]string{}, "legacy"},
		{[]string{"review"}, "review"},
		{[]string{"review", "--base", "dev"}, "review"},
		{[]string{"create"}, "create"},
		{[]string{"create", "--draft"}, "create"},
		{[]string{"--title", "hola"}, "legacy"},
		{[]string{"-t", "hola"}, "legacy"},
	}
	for _, caso := range casos {
		if got := verboPr(caso.args); got != caso.want {
			t.Errorf("verboPr(%v) = %q, esperado %q", caso.args, got, caso.want)
		}
	}
}

// TestParsearFlagsPrReview cubre los flags de pr review.
func TestParsearFlagsPrReview(t *testing.T) {
	flags, err := parsearFlagsPrReview([]string{"--base", "dev", "--overview", "--json"})
	if err != nil {
		t.Fatalf("parsearFlagsPrReview falló: %v", err)
	}
	if flags.base != "dev" || !flags.overview || !flags.jsonOut || flags.soloPendientes {
		t.Errorf("flags = %+v, esperado base=dev overview json", flags)
	}

	flags, err = parsearFlagsPrReview([]string{"--only-unaudited"})
	if err != nil {
		t.Fatalf("parsearFlagsPrReview(--only-unaudited) falló: %v", err)
	}
	if !flags.soloPendientes {
		t.Errorf("flags = %+v, esperado soloPendientes", flags)
	}

	if _, err := parsearFlagsPrReview([]string{"--nope"}); err == nil {
		t.Error("flag desconocido debería fallar")
	}
	if _, err := parsearFlagsPrReview([]string{"--base"}); err == nil {
		t.Error("--base sin valor debería fallar")
	}
}

// TestDetalleEventoPrReview: el detail del evento es JSON con los campos del
// esquema de la guía §13.
func TestDetalleEventoPrReview(t *testing.T) {
	res := &review.ResultadoRama{
		Rama:       "feature/x",
		SHAs:       []string{"a1b2c3d4e5f6"},
		Pendientes: []string{"a1b2c3d4e5f6"},
		Fichas:     []review.Ficha{{SHA: "a1b2c3d4e5f6", Message: "feat: algo"}},
		Volumen:    420,
		Decision:   "chain",
	}
	detalle, err := detalleEventoPrReview("main", res, true)
	if err != nil {
		t.Fatalf("detalleEventoPrReview falló: %v", err)
	}

	var crudo map[string]any
	if err := json.Unmarshal([]byte(detalle), &crudo); err != nil {
		t.Fatalf("detail no es JSON válido: %v\n%s", err, detalle)
	}
	for clave, esperado := range map[string]any{
		"base":      "main",
		"rama":      "feature/x",
		"auditadas": float64(1),
		"nuevas":    float64(1),
		"volumen":   float64(420),
		"ci":        true,
		"overview":  false,
		"chain_pr":  true,
	} {
		if crudo[clave] != esperado {
			t.Errorf("detail[%q] = %v, esperado %v", clave, crudo[clave], esperado)
		}
	}
}

// TestTextoDecisionPrReview: la salida de decisión distingue single/chain y
// explica el motivo.
func TestTextoDecisionPrReview(t *testing.T) {
	single := textoDecision("single", 100, false)
	if !strings.Contains(single, "una sola PR") || strings.Contains(single, "cadena") {
		t.Errorf("textoDecision(single, 100) = %q", single)
	}
	chain := textoDecision("chain", 450, false)
	if !strings.Contains(chain, "cadena") {
		t.Errorf("textoDecision(chain, 450) = %q", chain)
	}
	singleGrande := textoDecision("single", 450, true)
	if !strings.Contains(singleGrande, "coherente") {
		t.Errorf("textoDecision(single, 450, coherente) = %q", singleGrande)
	}
}
