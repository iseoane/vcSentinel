package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	gitinterno "github.com/ISeoane-Quental/vas.sentinel/internal/git"
)

type adapterSlicePlanFake struct{}

func (*adapterSlicePlanFake) ObtenerMensajeCommit([]string, string, int) (string, error) {
	return "feat(slice): mensaje desde perfil commit", nil
}

func TestEjecutarSlicePlanUsaFactoryDeMensajes(t *testing.T) {
	prepararRepoParaPlan(t)
	escribirConsentimientoDiff(t, true)
	if err := os.WriteFile("app.go", []byte("package app\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	var rutaFactory string
	anterior := nuevoAgentAdapterParaMensaje
	nuevoAgentAdapterParaMensaje = func(ruta string) (agentadapter.AgentAdapter, error) {
		rutaFactory = ruta
		return &adapterSlicePlanFake{}, nil
	}
	t.Cleanup(func() { nuevoAgentAdapterParaMensaje = anterior })

	var salida bytes.Buffer
	if codigo := ejecutarSlicePlan(&salida, []string{"--json"}); codigo != 0 {
		t.Fatalf("exit = %d: %s", codigo, salida.String())
	}
	var plan struct {
		Lotes []struct {
			Mensaje string `json:"mensaje"`
		} `json:"lotes"`
	}
	if err := json.Unmarshal(salida.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(rutaFactory) != filepath.Clean(cwd) {
		t.Fatalf("factory recibió %q, esperada raíz %q", rutaFactory, cwd)
	}
	if plan.Lotes[0].Mensaje != "feat(slice): mensaje desde perfil commit" {
		t.Fatalf("mensaje serializado = %q", plan.Lotes[0].Mensaje)
	}
}

func TestEjecutarSlicePlanSinConsentimientoNoInvocaAgente(t *testing.T) {
	prepararRepoParaPlan(t)
	escribirConsentimientoDiff(t, false)
	if err := os.WriteFile("app.go", []byte("package app\n"), 0644); err != nil {
		t.Fatal(err)
	}
	invocado := false
	nuevoAgentAdapterParaMensaje = func(string) (agentadapter.AgentAdapter, error) {
		invocado = true
		return &adapterSlicePlanFake{}, nil
	}

	var salida bytes.Buffer
	if codigo := ejecutarSlicePlan(&salida, []string{"--json"}); codigo != 0 {
		t.Fatalf("exit = %d: %s", codigo, salida.String())
	}
	if invocado {
		t.Fatal("se invocó el factory del agente sin consentimiento para exponer el micro-diff")
	}
	if !strings.Contains(salida.String(), "chore(slice): auto-fragmented") {
		t.Fatalf("no se usó el fallback determinista: %s", salida.String())
	}
}

func TestElegirAdaptadorSinConsentimientoUsaFallbackDeterminista(t *testing.T) {
	ruta := t.TempDir()
	t.Chdir(ruta)
	escribirConsentimientoDiff(t, false)
	plan := &gitinterno.PlanFragmentacion{Lotes: []gitinterno.LotePlanificado{{
		Numero: 1, MensajeAutomatico: "chore(slice): fallback local",
	}}}

	adapter, cancelado := elegirAdaptadorYGenerarMensajes(ruta, plan)
	if adapter != nil || cancelado {
		t.Fatalf("adapter = %v, cancelado = %t", adapter, cancelado)
	}
	if plan.Lotes[0].Mensaje != "chore(slice): fallback local" || !plan.Lotes[0].MensajeDeterminista {
		t.Fatalf("lote sin fallback determinista: %+v", plan.Lotes[0])
	}
}

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

// TestSlicePlanYApplyCaminoCompleto recorre el flujo de dos pasos de T0.10:
// plan → respuestas del usuario → apply, sin ninguna lectura de stdin.
func TestSlicePlanYApplyCaminoCompleto(t *testing.T) {
	prepararRepoParaPlan(t)
	if err := os.WriteFile("app.go", []byte("package app\n"), 0644); err != nil {
		t.Fatalf("no se pudo escribir app.go: %v", err)
	}

	var salidaPlan bytes.Buffer
	if codigo := ejecutarSlicePlan(&salidaPlan, []string{"--json"}); codigo != 0 {
		t.Fatalf("plan exit = %d, esperado 0. Salida: %s", codigo, salidaPlan.String())
	}
	if err := os.WriteFile("plan.json", salidaPlan.Bytes(), 0644); err != nil {
		t.Fatalf("no se pudo escribir plan.json: %v", err)
	}

	var plan struct {
		PlanID string `json:"plan_id"`
	}
	if err := json.Unmarshal(salidaPlan.Bytes(), &plan); err != nil {
		t.Fatalf("plan no serializable: %v", err)
	}
	respuestas := []byte(`{"plan_id":"` + plan.PlanID + `","respuestas":{}}`)
	if err := os.WriteFile("respuestas.json", respuestas, 0644); err != nil {
		t.Fatalf("no se pudo escribir respuestas.json: %v", err)
	}

	// Escribir los artefactos de aprobación dentro del repo NO invalida el
	// plan: el hash del árbol se ata a las rutas del plan (app.go), no al
	// worktree entero.
	var salidaApply bytes.Buffer
	if codigo := ejecutarSliceApply(&salidaApply, []string{"--plan", "plan.json", "--answers", "respuestas.json"}); codigo != 0 {
		t.Fatalf("apply exit = %d, esperado 0. Salida: %s", codigo, salidaApply.String())
	}
	if !strings.Contains(salidaApply.String(), "commits creados") {
		t.Errorf("no se confirmaron commits: %s", salidaApply.String())
	}

	// Reaplicar el mismo plan ya no es posible: app.go quedó commiteado, así
	// que el estado de sus rutas cambió.
	var salidaRepeticion bytes.Buffer
	if codigo := ejecutarSliceApply(&salidaRepeticion, []string{"--plan", "plan.json", "--answers", "respuestas.json"}); codigo != 1 {
		t.Fatalf("reaplicar exit = %d, esperado 1. Salida: %s", codigo, salidaRepeticion.String())
	}
}

func prepararRepoParaPlan(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
	anterior := nuevoAgentAdapterParaMensaje
	nuevoAgentAdapterParaMensaje = func(string) (agentadapter.AgentAdapter, error) {
		return nil, errors.New("agente no disponible en test")
	}
	t.Cleanup(func() { nuevoAgentAdapterParaMensaje = anterior })
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

func escribirConsentimientoDiff(t *testing.T, permitido bool) {
	t.Helper()
	if err := os.MkdirAll(".vas_sentinel", 0755); err != nil {
		t.Fatal(err)
	}
	contenido := fmt.Sprintf("allow_external_agent_diff: %t\n", permitido)
	if err := os.WriteFile(filepath.Join(".vas_sentinel", "vassentinel.yml"), []byte(contenido), 0644); err != nil {
		t.Fatal(err)
	}
}
