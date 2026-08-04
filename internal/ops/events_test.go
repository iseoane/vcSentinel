package ops

import (
	"os"
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

func TestRotarEventosDejaUltimasLineas(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		if err := RegistrarEvento(dir, "status", 0, nil, "", ""); err != nil {
			t.Fatal(err)
		}
	}

	if err := RotarEventos(dir, 2); err != nil {
		t.Fatalf("RotarEventos devolvió error: %v", err)
	}
	eventos, err := UltimosEventos(dir, 10)
	if err != nil {
		t.Fatalf("UltimosEventos devolvió error: %v", err)
	}
	if len(eventos) != 2 {
		t.Errorf("eventos = %d, esperado 2 tras rotar", len(eventos))
	}

	// El log sigue siendo appendable tras la rotación.
	if err := RegistrarEvento(dir, "review", 1, nil, "", ""); err != nil {
		t.Fatalf("RegistrarEvento tras rotar devolvió error: %v", err)
	}
	eventos, err = UltimosEventos(dir, 10)
	if err != nil {
		t.Fatalf("UltimosEventos devolvió error: %v", err)
	}
	if len(eventos) != 3 || eventos[0].Cmd != "review" {
		t.Errorf("eventos = %d, el más reciente %q, esperado 3 con review", len(eventos), eventos[0].Cmd)
	}
}

func TestRotarEventosSinLogEsNoOp(t *testing.T) {
	dir := t.TempDir()
	if err := RotarEventos(dir, 100); err != nil {
		t.Fatalf("RotarEventos sin log devolvió error: %v", err)
	}
}

func TestRotarEventosDescartaLineasCorruptas(t *testing.T) {
	dir := t.TempDir()
	if err := RegistrarEvento(dir, "status", 0, nil, "", ""); err != nil {
		t.Fatal(err)
	}
	ruta := filepath.Join(dir, eventosRel)
	if err := appendLinea(ruta, "{\"corrupto\""); err != nil {
		t.Fatal(err)
	}
	if err := RegistrarEvento(dir, "review", 0, nil, "", ""); err != nil {
		t.Fatal(err)
	}

	if err := RotarEventos(dir, 10); err != nil {
		t.Fatalf("RotarEventos devolvió error: %v", err)
	}
	// El archivo reescrito solo contiene las líneas que se pudieron parsear
	// como evento: la corrupta se descarta en la rotación.
	eventos, err := UltimosEventos(dir, 10)
	if err != nil {
		t.Fatalf("UltimosEventos devolvió error: %v", err)
	}
	if len(eventos) != 2 {
		t.Errorf("eventos = %d, esperado 2 (las corruptas se descartan al rotar)", len(eventos))
	}
}

func TestPurgeEventosDeBorraSoloLosShasPedidos(t *testing.T) {
	dir := t.TempDir()
	if err := RegistrarEvento(dir, "review", 4, []string{"aaa111"}, "provider_unavailable", ""); err != nil {
		t.Fatal(err)
	}
	if err := RegistrarEvento(dir, "review", 0, []string{"bbb222"}, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := RegistrarEvento(dir, "status", 0, nil, "", ""); err != nil {
		t.Fatal(err)
	}

	eliminadas, err := PurgeEventosDe(dir, []string{"aaa111"})
	if err != nil {
		t.Fatalf("PurgeEventosDe devolvió error: %v", err)
	}
	if eliminadas != 1 {
		t.Errorf("líneas eliminadas = %d, esperado 1", eliminadas)
	}

	eventos, err := UltimosEventos(dir, 10)
	if err != nil {
		t.Fatalf("UltimosEventos devolvió error: %v", err)
	}
	if len(eventos) != 2 {
		t.Fatalf("eventos = %d, esperado 2 (los de commits vivos se conservan)", len(eventos))
	}
	// El evento de aaa111 desapareció; el de bbb222 y el status siguen.
	if eventos[0].Cmd != "status" || eventos[1].Cmd != "review" || eventos[1].Shas[0] != "bbb222" {
		t.Errorf("eventos restantes = %+v, no coinciden", eventos)
	}

	// El log sigue siendo appendable tras el purge.
	if err := RegistrarEvento(dir, "check", 0, nil, "", ""); err != nil {
		t.Fatalf("RegistrarEvento tras purge devolvió error: %v", err)
	}
}

func TestPurgeEventosDeSinShasEsNoOp(t *testing.T) {
	dir := t.TempDir()
	if err := RegistrarEvento(dir, "review", 0, []string{"aaa111"}, "", ""); err != nil {
		t.Fatal(err)
	}
	eliminadas, err := PurgeEventosDe(dir, nil)
	if err != nil || eliminadas != 0 {
		t.Errorf("PurgeEventosDe(nil) = %d, %v; esperado 0, nil", eliminadas, err)
	}
	eventos, _ := UltimosEventos(dir, 10)
	if len(eventos) != 1 {
		t.Errorf("eventos = %d, esperado 1 (sin cambios)", len(eventos))
	}
}

func TestPurgeEventosDeSinLogEsNoOp(t *testing.T) {
	dir := t.TempDir()
	eliminadas, err := PurgeEventosDe(dir, []string{"aaa111"})
	if err != nil || eliminadas != 0 {
		t.Errorf("PurgeEventosDe sin log = %d, %v; esperado 0, nil", eliminadas, err)
	}
}

func appendLinea(ruta, linea string) error {
	archivo, err := os.OpenFile(ruta, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer archivo.Close()
	_, err = archivo.WriteString(linea + "\n")
	return err
}
