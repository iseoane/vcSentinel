package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/change"
)

func TestEjecutarExplainJSONExponePerfilDetectoresRiesgoYCohesion(t *testing.T) {
	perfil := change.ChangeProfile{
		Base: "BASE", Head: "HEAD", Kind: "feature",
		Size:    change.ChangeSize{Files: 2, Added: 2, Hunks: 1},
		Symbols: change.ChangeSymbols{ExportedTouched: 1, Complete: true}, Modules: []string{"internal/auth"},
		FileClasses: map[string]int{"source": 1, "docs": 1},
	}
	diffReads := 0
	lector := func(args ...string) (string, error) {
		comando := strings.Join(args, " ")
		switch {
		case strings.Contains(comando, "diff --name-only"):
			return "internal/auth/login.go\x00docs/guia.md\x00", nil
		case strings.Contains(comando, "diff --no-color --unified=0"):
			diffReads++
			if !strings.Contains(comando, "--src-prefix=a/") || !strings.Contains(comando, "--dst-prefix=b/") {
				t.Errorf("unified diff command does not force stable prefixes: %s", comando)
			}
			return "diff --git a/internal/auth/login.go b/internal/auth/login.go\n+++ b/internal/auth/login.go\n@@ -0,0 +1 @@\n+func ValidateToken() { go process() }\ndiff --git a/docs/guia.md b/docs/guia.md\n+++ b/docs/guia.md\n@@ -0,0 +1 @@\n+guide\n", nil
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
	if diffReads != 1 {
		t.Fatalf("unified diff reads = %d, want 1", diffReads)
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
	if got.Profile.Kind != "feature" || got.Profile.Symbols != perfil.Symbols {
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
	concurrencyFound := false
	for _, caracteristica := range got.Characteristics {
		if caracteristica.Name == "concurrency" {
			concurrencyFound = caracteristica.State == "present" && caracteristica.Detector != ""
		}
	}
	if !concurrencyFound {
		t.Errorf("concurrency did not observe the added source content: %+v", got.Characteristics)
	}
	if got.Risk.Level != "high" || !strings.Contains(got.Risk.Explain, "security_sensitive") {
		t.Errorf("riesgo = %+v", got.Risk)
	}
	if got.Cohesion.Clusters != 2 || !got.Cohesion.SuggestedSplit {
		t.Errorf("cohesión = %+v", got.Cohesion)
	}
}

func TestEjecutarExplainKeepsAddedLinesForPathStartingWithB(t *testing.T) {
	const path = "b/cmd/sentinel/x.go"
	perfil := change.ChangeProfile{
		Base: "BASE", Head: "HEAD", Kind: "feature",
		Size:        change.ChangeSize{Files: 1, Added: 1, Hunks: 1},
		FileClasses: map[string]int{"source": 1},
	}
	lector := func(args ...string) (string, error) {
		comando := strings.Join(args, " ")
		switch {
		case strings.Contains(comando, "diff --name-only"):
			return path + "\x00", nil
		case strings.Contains(comando, "diff --no-color --unified=0"):
			prefix := path
			if strings.Contains(comando, "--src-prefix=a/") && strings.Contains(comando, "--dst-prefix=b/") {
				prefix = "b/" + path
			}
			return "diff --git a/" + path + " b/" + path + "\n+++ " + prefix + "\n@@ -0,0 +1 @@\n+func Added() { go process() }\n", nil
		case strings.HasPrefix(comando, "show "):
			return "", fmt.Errorf("missing .gitattributes")
		case strings.HasPrefix(comando, "log "):
			return "", nil
		default:
			return "", fmt.Errorf("unexpected git command: %s", comando)
		}
	}

	var salida bytes.Buffer
	err := ejecutarExplainCon(&salida, []string{"BASE..HEAD", "--json"},
		func(base, head string) (change.ChangeProfile, error) { return perfil, nil }, lector)
	if err != nil {
		t.Fatalf("ejecutarExplainCon returned error: %v", err)
	}

	var got struct {
		Characteristics []struct {
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"characteristics"`
	}
	if err := json.Unmarshal(salida.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, salida.String())
	}
	for _, caracteristica := range got.Characteristics {
		if caracteristica.Name == "concurrency" && caracteristica.State == "present" {
			return
		}
	}
	t.Fatalf("concurrency did not observe added lines for %q: %+v", path, got.Characteristics)
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
