package agentadapter

import (
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
)

// TestPromptFijaElIdiomaPorDefecto cubre T0.13: sin decir el idioma, el
// modelo lo elegía al azar y la fragmentación de T0.1 salió con 11 mensajes
// en inglés y 2 en castellano dentro de la misma ejecución.
func TestPromptFijaElIdiomaPorDefecto(t *testing.T) {
	prompt := construirPromptAgente("backend", 1, []string{"a.go"}, "")

	if !strings.Contains(prompt, "castellano") {
		t.Errorf("el prompt no fija el idioma: %s", prompt)
	}
	if !strings.Contains(prompt, ejemploPorIdioma[IdiomaPorDefecto]) {
		t.Errorf("el prompt no trae el ejemplo del idioma: %s", prompt)
	}
}

// TestPromptRespetaElIdiomaConfigurado: no todos los repositorios escriben en
// castellano, así que el idioma es configurable.
func TestPromptRespetaElIdiomaConfigurado(t *testing.T) {
	prompt := construirPromptAgente("backend", 1, []string{"a.go"}, "en")

	if !strings.Contains(prompt, "English") {
		t.Errorf("el prompt no fija el inglés: %s", prompt)
	}
	if strings.Contains(prompt, "castellano") {
		t.Errorf("el prompt mezcla idiomas: %s", prompt)
	}
	if !strings.Contains(prompt, ejemploPorIdioma["en"]) {
		t.Errorf("el prompt no trae el ejemplo en inglés: %s", prompt)
	}
}

// TestIdiomaDesconocidoNoRompeElPrompt: un valor no reconocido en el yml debe
// degradar al comportamiento por defecto, nunca dejar el prompt sin idioma.
func TestIdiomaDesconocidoNoRompeElPrompt(t *testing.T) {
	prompt := construirPromptAgente("backend", 1, []string{"a.go"}, "klingon")

	if !strings.Contains(prompt, "castellano") {
		t.Errorf("un idioma desconocido debe caer al de por defecto: %s", prompt)
	}
}

// TestPromptConservaElContrato: fijar el idioma no puede romper lo que ya
// exigía el prompt — Conventional Commits y una sola línea sin markdown.
func TestPromptConservaElContrato(t *testing.T) {
	prompt := construirPromptAgente("config", 3, []string{"a.yml", "b.yml"}, "")

	for _, esperado := range []string{"Conventional Commits", "ÚNICAMENTE", "a.yml", "b.yml", "config", "#3"} {
		if !strings.Contains(prompt, esperado) {
			t.Errorf("el prompt perdió %q: %s", esperado, prompt)
		}
	}
}

// TestDefaultDeConfigCoincideConElDeAgentadapter ata los dos valores que el
// ciclo de imports obliga a duplicar: config no puede referenciar
// agentadapter.IdiomaPorDefecto porque agentadapter ya importa config.
func TestDefaultDeConfigCoincideConElDeAgentadapter(t *testing.T) {
	cfg := config.CargarConfiguracionLocal(t.TempDir())
	if cfg.CommitLanguage != IdiomaPorDefecto {
		t.Errorf("config.CommitLanguage = %q, agentadapter.IdiomaPorDefecto = %q: deben coincidir",
			cfg.CommitLanguage, IdiomaPorDefecto)
	}
}

// TestFactoryPropagaElIdiomaALaCadena: el idioma del yml debe llegar a cada
// adaptador, no quedarse en la configuración.
func TestFactoryPropagaElIdiomaALaCadena(t *testing.T) {
	cfg := config.CargarConfiguracionLocal(t.TempDir())
	cfg.CommitLanguage = "en"

	cadena, err := construirCadena(cfg, []string{"claude", "opencode"}, "")
	if err != nil {
		t.Fatalf("construirCadena devolvió error: %v", err)
	}
	if len(cadena.adaptadores) != 2 {
		t.Fatalf("adaptadores = %d, esperado 2", len(cadena.adaptadores))
	}
	for _, adaptador := range cadena.adaptadores {
		cli, ok := adaptador.(*CLIAdapter)
		if !ok {
			t.Fatalf("adaptador inesperado: %T", adaptador)
		}
		if cli.idiomaCommit() != "en" {
			t.Errorf("%s: idioma = %q, esperado en", cli.BinaryName, cli.idiomaCommit())
		}
	}
}

// TestCLIAdapterUsaSuIdioma: el adaptador propaga el idioma configurado al
// prompt en vez de recalcularlo o ignorarlo.
func TestCLIAdapterUsaSuIdioma(t *testing.T) {
	adapter := &CLIAdapter{BinaryName: "claude", CommitLanguage: "en"}
	if adapter.idiomaCommit() != "en" {
		t.Errorf("idiomaCommit = %q, esperado en", adapter.idiomaCommit())
	}

	sinIdioma := &CLIAdapter{BinaryName: "claude"}
	if sinIdioma.idiomaCommit() != IdiomaPorDefecto {
		t.Errorf("idiomaCommit = %q, esperado %q", sinIdioma.idiomaCommit(), IdiomaPorDefecto)
	}
}
