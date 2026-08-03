package ops

import (
	"path/filepath"
	"testing"
)

func TestRegistrarYLeerEventos(t *testing.T) {
	dir := t.TempDir()

	if err := RegistrarEvento(dir, "check", 0, nil, "", ""); err != nil {
		t.Fatalf("RegistrarEvento devolvió error: %v", err)
	}
	if err := RegistrarEvento(dir, "review", 4, []string{"abc123"}, "provider_unavailable", filepath.Join("C:", "repo")); err != nil {
		t.Fatalf("RegistrarEvento devolvió error: %v", err)
	}

	eventos, err := UltimosEventos(dir, 10)
	if err != nil {
		t.Fatalf("UltimosEventos devolvió error: %v", err)
	}
	if len(eventos) != 2 {
		t.Fatalf("eventos = %d, esperado 2", len(eventos))
	}
	// El más reciente primero.
	if eventos[0].Cmd != "review" || eventos[0].Exit != 4 || eventos[0].Detail != "provider_unavailable" {
		t.Errorf("evento más reciente = %+v, no coincide", eventos[0])
	}
	if len(eventos[0].Shas) != 1 || eventos[0].Shas[0] != "abc123" {
		t.Errorf("shas = %+v, esperado [abc123]", eventos[0].Shas)
	}
	if eventos[1].Cmd != "check" {
		t.Errorf("evento anterior = %+v, esperado check", eventos[1])
	}
}

func TestUltimosEventosLimite(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		if err := RegistrarEvento(dir, "status", 0, nil, "", ""); err != nil {
			t.Fatal(err)
		}
	}

	eventos, err := UltimosEventos(dir, 3)
	if err != nil {
		t.Fatalf("UltimosEventos devolvió error: %v", err)
	}
	if len(eventos) != 3 {
		t.Fatalf("eventos = %d, esperado 3", len(eventos))
	}
}

func TestUltimosEventosSinLog(t *testing.T) {
	dir := t.TempDir()

	eventos, err := UltimosEventos(dir, 10)
	if err != nil {
		t.Fatalf("UltimosEventos devolvió error: %v", err)
	}
	if len(eventos) != 0 {
		t.Errorf("eventos = %d, esperado 0 sin log", len(eventos))
	}
}

func TestRegistrarMilEventosSinCorrupcion(t *testing.T) {
	dir := t.TempDir()

	for i := 0; i < 1000; i++ {
		if err := RegistrarEvento(dir, "check", 0, nil, "", ""); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	eventos, err := UltimosEventos(dir, 0)
	if err != nil {
		t.Fatalf("UltimosEventos devolvió error: %v", err)
	}
	if len(eventos) != 1000 {
		t.Errorf("eventos = %d, esperado 1000", len(eventos))
	}
}
