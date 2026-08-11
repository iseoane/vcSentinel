package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/change"
)

func TestEjecutarExplainJSONExponePerfilDetectoresRiesgoYCohesion(t *testing.T) {
	perfil := change.ChangeProfile{
		Base: "BASE", Head: "HEAD", Kind: "feature",
		Size:    change.ChangeSize{Files: 2, Added: 2, Hunks: 1},
		Symbols: change.ChangeSymbols{}, Modules: []string{"internal/auth"},
		FileClasses: map[string]int{"source": 1, "docs": 1},
	}
	lector := func(args ...string) (string, error) {
		comando := strings.Join(args, " ")
		switch {
		case strings.Contains(comando, "diff --name-only"):
			return "internal/auth/login.go\x00docs/guia.md\x00", nil
		case strings.Contains(comando, "diff --no-color --unified=0"):
			return "diff --git a/internal/auth/login.go b/internal/auth/login.go\n+++ b/internal/auth/login.go\n@@ -0,0 +1 @@\n+func ValidateToken() {}\ndiff --git a/docs/guia.md b/docs/guia.md\n+++ b/docs/guia.md\n@@ -0,0 +1 @@\n+guia\n", nil
		case strings.HasPrefix(comando, "show "):
			return "", fmt.Errorf("sin .gitattributes")
		case strings.HasPrefix(comando, "log "):
			return "", nil
		default:
			return "", fmt.Errorf("git inesperado: %s", comando)
		}
	}

	var salida bytes.Buffer
	err := ejecutarExplainCon(&salida, []string{"BASE..HEAD", "--json"},
		func(base, head string) (change.ChangeProfile, error) { return perfil, nil }, lector)
	if err != nil {
		t.Fatalf("ejecutarExplainCon devolvió error: %v", err)
	}

	var got struct {
		Profile         change.ChangeProfile                     `json:"profile"`
		Characteristics []struct{ Name, State, Detector string } `json:"characteristics"`
		Risk            struct{ Level, Explain string }          `json:"risk"`
		Cohesion        struct {
			Clusters       int  `json:"clusters"`
			SuggestedSplit bool `json:"suggested_split"`
		} `json:"cohesion"`
	}
	if err := json.Unmarshal(salida.Bytes(), &got); err != nil {
		t.Fatalf("salida JSON inválida: %v\n%s", err, salida.String())
	}
	if got.Profile.Kind != "feature" || got.Profile.Symbols != (change.ChangeSymbols{}) {
		t.Errorf("perfil = %+v", got.Profile)
	}
	seguridadEncontrada := false
	for _, caracteristica := range got.Characteristics {
		if caracteristica.Name == "security_sensitive" {
			seguridadEncontrada = caracteristica.State == "present" && caracteristica.Detector != ""
		}
	}
	if !seguridadEncontrada {
		t.Errorf("security_sensitive no expone estado y detector: %+v", got.Characteristics)
	}
	if got.Risk.Level != "high" || !strings.Contains(got.Risk.Explain, "security_sensitive") {
		t.Errorf("riesgo = %+v", got.Risk)
	}
	if got.Cohesion.Clusters != 2 || !got.Cohesion.SuggestedSplit {
		t.Errorf("cohesión = %+v", got.Cohesion)
	}
}

func TestParsearExplainRechazaComponentesQueParecenOpciones(t *testing.T) {
	for _, rango := range []string{"--output=robo..HEAD", "HEAD..--output=robo"} {
		t.Run(rango, func(t *testing.T) {
			if _, _, _, err := parsearExplain([]string{rango}); err == nil {
				t.Fatalf("parsearExplain aceptó el rango peligroso %q", rango)
			}
		})
	}
}

func TestLineasAnadidasExplainUsaRutasNulasYSepardorGit(t *testing.T) {
	rutas := []string{"dir/ruta rara\t.go", "-opcion.go"}
	lector := func(args ...string) (string, error) {
		if len(args) < 3 || args[len(args)-2] != "--" {
			t.Fatalf("git diff no separó opciones de ruta: %v", args)
		}
		pathspec := args[len(args)-1]
		ruta, esLiteral := strings.CutPrefix(pathspec, ":(literal)")
		if !esLiteral {
			t.Fatalf("git diff no forzó pathspec literal (magia de ':' sin desactivar): %v", pathspec)
		}
		return "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -0,0 +1,2 @@\n+++ b/no-es-cabecera\n+contenido de " + ruta + "\n", nil
	}

	lineas, err := lineasAnadidasExplain(lector, "BASE..HEAD", rutas)
	if err != nil {
		t.Fatal(err)
	}
	for _, ruta := range rutas {
		esperado := []string{"++ b/no-es-cabecera", "contenido de " + ruta}
		if got := lineas[ruta]; !reflect.DeepEqual(got, esperado) {
			t.Fatalf("líneas de %q = %q, esperado %q", ruta, got, esperado)
		}
	}
}
