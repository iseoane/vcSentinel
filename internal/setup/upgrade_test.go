package setup

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReemplazarWindows(t *testing.T) {
	dir := t.TempDir()
	binarioActual := filepath.Join(dir, "sentinel.exe")
	tmpPath := filepath.Join(dir, "nuevo.exe")

	if err := os.WriteFile(binarioActual, []byte("version-vieja"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmpPath, []byte("version-nueva"), 0644); err != nil {
		t.Fatal(err)
	}

	respaldo, err := reemplazarWindows(tmpPath, binarioActual)
	if err != nil {
		t.Fatalf("reemplazarWindows devolvió error: %v", err)
	}
	if respaldo != binarioActual+".old" {
		t.Errorf("respaldo = %q, esperado %q", respaldo, binarioActual+".old")
	}

	contenido, err := os.ReadFile(binarioActual)
	if err != nil {
		t.Fatalf("no se pudo leer el binario actual: %v", err)
	}
	if string(contenido) != "version-nueva" {
		t.Errorf("el binario actual no fue reemplazado, obtuve %q", contenido)
	}

	if _, err := os.Stat(respaldo); !os.IsNotExist(err) {
		t.Errorf("el respaldo %s debería haberse limpiado, err=%v", respaldo, err)
	}
}

func TestReemplazarLinux(t *testing.T) {
	dir := t.TempDir()
	binarioActual := filepath.Join(dir, "sentinel")
	tmpPath := filepath.Join(dir, "nuevo")

	if err := os.WriteFile(binarioActual, []byte("version-vieja"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmpPath, []byte("version-nueva"), 0755); err != nil {
		t.Fatal(err)
	}

	if err := reemplazarLinux(tmpPath, binarioActual); err != nil {
		t.Fatalf("reemplazarLinux devolvió error: %v", err)
	}

	contenido, err := os.ReadFile(binarioActual)
	if err != nil {
		t.Fatalf("no se pudo leer el binario actual: %v", err)
	}
	if string(contenido) != "version-nueva" {
		t.Errorf("el binario no fue reemplazado, obtuve %q", contenido)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(binarioActual)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0755 {
			t.Errorf("permisos esperados 0755, obtuve %o", perm)
		}
	}
}

func TestReemplazarWindowsRespaldoInexistente(t *testing.T) {
	dir := t.TempDir()
	binarioActual := filepath.Join(dir, "sentinel.exe")
	tmpPath := filepath.Join(dir, "nuevo.exe")

	if err := os.WriteFile(tmpPath, []byte("version-nueva"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := reemplazarWindows(tmpPath, binarioActual)
	if err == nil {
		t.Fatalf("se esperaba error cuando el binario actual no existe")
	}
	if !strings.Contains(err.Error(), "respaldar") {
		t.Errorf("el error debe mencionar el respaldo, obtuve: %v", err)
	}
}

func TestReemplazarWindowsInstalacionFallidaRestauraRespaldo(t *testing.T) {
	dir := t.TempDir()
	binarioActual := filepath.Join(dir, "sentinel.exe")

	if err := os.WriteFile(binarioActual, []byte("version-vieja"), 0644); err != nil {
		t.Fatal(err)
	}

	// tmpPath no existe: falla el segundo rename y debe restaurarse el respaldo.
	tmpPath := filepath.Join(dir, "inexistente.exe")
	_, err := reemplazarWindows(tmpPath, binarioActual)
	if err == nil {
		t.Fatalf("se esperaba error al instalar la nueva versión")
	}
	if !strings.Contains(err.Error(), "instalar") {
		t.Errorf("el error debe mencionar la instalación, obtuve: %v", err)
	}

	contenido, err := os.ReadFile(binarioActual)
	if err != nil {
		t.Fatalf("el binario actual debería existir de nuevo: %v", err)
	}
	if string(contenido) != "version-vieja" {
		t.Errorf("el binario actual no fue restaurado, obtuve %q", contenido)
	}
}

func TestReemplazarLinuxErrorAlSustituir(t *testing.T) {
	dir := t.TempDir()
	binarioActual := filepath.Join(dir, "sentinel")
	tmpPath := filepath.Join(dir, "nuevo")

	if err := os.Mkdir(binarioActual, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmpPath, []byte("version-nueva"), 0755); err != nil {
		t.Fatal(err)
	}

	err := reemplazarLinux(tmpPath, binarioActual)
	if err == nil {
		t.Fatalf("se esperaba error al reemplazar sobre un directorio")
	}
	if !strings.Contains(err.Error(), "reemplazar") {
		t.Errorf("el error debe mencionar el reemplazo, obtuve: %v", err)
	}
}

func TestVerificarBinario(t *testing.T) {
	dir := t.TempDir()
	ruta := filepath.Join(dir, "sentinel")
	if runtime.GOOS == "windows" {
		ruta += ".cmd"
		if err := os.WriteFile(ruta, []byte("@echo off\r\necho v1.2.3\r\n"), 0755); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.WriteFile(ruta, []byte("#!/bin/sh\necho v1.2.3\n"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(ruta, 0755); err != nil {
			t.Fatal(err)
		}
	}

	if err := verificarBinario(ruta); err != nil {
		t.Fatalf("verificarBinario devolvió error: %v", err)
	}
}

func TestVerificarBinarioQueFalla(t *testing.T) {
	dir := t.TempDir()
	ruta := filepath.Join(dir, "sentinel")
	if runtime.GOOS == "windows" {
		ruta += ".cmd"
		if err := os.WriteFile(ruta, []byte("@echo off\r\nexit /b 1\r\n"), 0755); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.WriteFile(ruta, []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(ruta, 0755); err != nil {
			t.Fatal(err)
		}
	}

	err := verificarBinario(ruta)
	if err == nil {
		t.Fatalf("se esperaba error cuando el binario no responde a --version")
	}
	if !strings.Contains(err.Error(), "--version") {
		t.Errorf("el error debe mencionar --version, obtuve: %v", err)
	}
}

func TestLocalizarBinarioActual(t *testing.T) {
	ruta, err := localizarBinarioActual()
	if err != nil {
		t.Fatalf("localizarBinarioActual devolvió error: %v", err)
	}
	if ruta == "" {
		t.Error("localizarBinarioActual devolvió una ruta vacía")
	}
	if _, err := os.Stat(ruta); err != nil {
		t.Errorf("la ruta devuelta no existe: %v", err)
	}
}
