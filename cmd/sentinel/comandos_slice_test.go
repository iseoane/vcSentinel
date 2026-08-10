package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestEjecutarSlicePlanDevuelveTresConDecisionPendiente cubre el contrato de
// exit de T0.9: 3 cuando hay una decisión que solo el usuario puede responder.
func TestEjecutarSlicePlanDevuelveTresConDecisionPendiente(t *testing.T) {
	prepararRepoParaPlan(t)
	contenido := strings.Repeat("// línea de relleno\n", 600)
	if err := os.WriteFile("gigante.go", []byte(contenido), 0644); err != nil {
		t.Fatalf("no se pudo escribir gigante.go: %v", err)
	}

	var salida bytes.Buffer
	if codigo := ejecutarSlicePlan(&salida, []string{"--json"}); codigo != codigoSalidaDecisionesPendientes {
		t.Fatalf("exit = %d, esperado %d. Salida: %s", codigo, codigoSalidaDecisionesPendientes, salida.String())
	}

	var plan struct {
		PlanID               string `json:"plan_id"`
		DecisionesPendientes []struct {
			Archivo string `json:"archivo"`
		} `json:"decisiones_pendientes"`
	}
	if err := json.Unmarshal(salida.Bytes(), &plan); err != nil {
		t.Fatalf("la salida --json no es JSON válido: %v", err)
	}
	if plan.PlanID == "" {
		t.Error("el plan emitido no trae plan_id")
	}
	if len(plan.DecisionesPendientes) != 1 || plan.DecisionesPendientes[0].Archivo != "gigante.go" {
		t.Errorf("decisiones pendientes = %+v", plan.DecisionesPendientes)
	}
}

func prepararRepoParaPlan(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@vas.sentinel"},
		{"config", "user.name", "VAS Sentinel Test"},
		{"config", "core.hooksPath", ""},
	} {
		if err := exec.Command("git", args...).Run(); err != nil {
			t.Fatalf("preparación %v falló: %v", args, err)
		}
	}
	if err := os.WriteFile("base.txt", []byte("base\n"), 0644); err != nil {
		t.Fatalf("no se pudo escribir base.txt: %v", err)
	}
	for _, args := range [][]string{{"add", "base.txt"}, {"commit", "-m", "chore: base"}} {
		if err := exec.Command("git", args...).Run(); err != nil {
			t.Fatalf("preparación %v falló: %v", args, err)
		}
	}
}
