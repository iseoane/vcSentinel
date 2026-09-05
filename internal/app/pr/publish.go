package pr

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/modelprobe"
	"github.com/ISeoane-Quental/vas.sentinel/internal/ops"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

// salirSiError centraliza el patrón de salida del CLI: imprime el error y
// abandona con código 1. Sin error no hace nada.
func salirSiError(err error) {
	if err != nil {
		fmt.Printf("? %v\n", err)
		os.Exit(1)
	}
}

// EscribirPlantillaPR guarda el cuerpo del PR en un archivo temporal y
// devuelve su ruta. El archivo temporal evita ensuciar el worktree: la
// plantilla es un artefacto efímero de publicación.
func EscribirPlantillaPR(cuerpo string) (string, error) {
	archivo, err := os.CreateTemp("", "sentinel_pr_*.md")
	if err != nil {
		return "", fmt.Errorf("no se pudo crear el archivo temporal de la plantilla: %w", err)
	}
	defer archivo.Close()
	if _, err := archivo.WriteString(cuerpo); err != nil {
		return "", fmt.Errorf("no se pudo escribir la plantilla: %w", err)
	}
	return archivo.Name(), nil
}

// CopiarPortapapelesCon es la versión inyectable de copiarPortapapeles:
// existe decide qué herramienta está disponible; ejecutar lanza la copia.
// Usa la PRIMERA herramienta del orden canónico (clip > wl-copy > xclip) que
// exista en el PATH: si esa ejecución falla, no se intenta la siguiente. Es
// una decisión deliberada: la primera disponible es la canónica de la
// plataforma y un fallo suyo casi siempre indica un entorno roto, no un
// fallo de la herramienta.
func CopiarPortapapelesCon(texto string, existe func(string) bool, ejecutar func(string, string) error) error {
	candidatos := []string{"clip", "wl-copy", "xclip"}
	for _, nombre := range candidatos {
		if !existe(nombre) {
			continue
		}
		if err := ejecutar(nombre, texto); err != nil {
			return fmt.Errorf("no se pudo copiar al portapapeles con %s: %w", nombre, err)
		}
		return nil
	}
	return errors.New("no se encontró ninguna herramienta de portapapeles (clip/wl-copy/xclip)")
}

// PublicarPRCon es la versión inyectable de publicarPR (seam de prueba). Los
// tres callbacks son los campos del struct opcionesPublicarPR que cmd/sentinel
// construye: ghDisponible decide si gh está en el PATH, ejecutarGh lanza gh y
// devuelve su salida (args completos, incluyendo el worktree como cwd), copiar
// se usa solo en el fallback (portapapeles).
func PublicarPRCon(worktree, rutaPlantilla, base string,
	ghDisponible func(string) bool,
	ejecutarGh func(worktree string, args ...string) ([]byte, error),
	copiar func(string) error) (string, bool, error) {
	if ghDisponible("gh") {
		args := []string{"pr", "create", "--draft"}
		if base != "" {
			// La PR debe targetear la MISMA base que se auditó: sin --base
			// explícito, la revisión y la PR podrían divergir en silencio.
			args = append(args, "--base", base)
		}
		args = append(args, "-F", rutaPlantilla)
		salida, err := ejecutarGh(worktree, args...)
		if err != nil {
			return "", false, err
		}
		return strings.TrimSpace(string(salida)), false, nil
	}

	cuerpo, err := os.ReadFile(rutaPlantilla)
	if err != nil {
		return "", true, fmt.Errorf("no se pudo releer la plantilla para el portapapeles: %w", err)
	}
	fmt.Printf("? gh no está en el PATH: la plantilla quedó en %s y se copia al portapapeles.\n", rutaPlantilla)
	if err := copiar(string(cuerpo)); err != nil {
		return "", true, err
	}
	return "", true, nil
}

// VerificarParaPlantillaCon es la versión inyectable de verificarParaPlantilla:
// verificar nil se sustituye por ops.Verificar en producción. El verificador
// debe ser el compartido por toda la invocación para no repetir el sondeo.
// nuevoVerificadorModelo is wired from package main as a closure over its
// nuevoVerificadorModelo var (tests swap it), so the var is read at call time.
func VerificarParaPlantillaCon(worktree, gitDir string, cfg config.Config, verificadorModelo *modelprobe.Verificador,
	verificar func(ops.OpcionesVerificar) (ops.ResultadoVerificacion, error),
	nuevoVerificadorModelo func(worktree string) *modelprobe.Verificador) review.VerificacionPlantilla {

	if verificar == nil {
		verificar = ops.Verificar
	}

	perfil := config.ResolverPerfil(cfg, "", "")
	adapter, err := agentadapter.NuevoAdaptadorConPerfil(cfg, perfil)
	if err != nil {
		// Sin agente no hay vía de delegación; la vía determinista sigue viva.
		// ops.Verificar acepta Agente nil (lo comprueba antes de usarlo):
		// la delegación se degrada a "sin_agente", nunca panic.
		adapter = nil
	}
	if verificadorModelo == nil {
		verificadorModelo = nuevoVerificadorModelo(worktree)
	}
	verificadorModelo.Verificar(perfil.Nombre, perfil.Modelo, adapter)
	verif, err := verificar(ops.OpcionesVerificar{
		Worktree: worktree,
		GitDir:   gitDir,
		Cfg:      cfg,
		Agente:   adapter,
		Preguntar: func(aviso string) (string, error) {
			fmt.Println(aviso)
			fmt.Print("> ")
			var respuesta string
			if _, err := fmt.Scanln(&respuesta); err != nil {
				return "", err
			}
			return respuesta, nil
		},
	})
	if err != nil {
		return review.VerificacionPlantilla{
			Modo:   ops.ModoOmitido,
			Motivo: fmt.Sprintf("error_de_verificacion: %v", err),
		}
	}
	plantilla := review.VerificacionPlantilla{
		Modo:   verif.Modo,
		Tested: verif.Tested,
		Motivo: verif.Motivo,
	}
	for _, c := range verif.Comandos {
		plantilla.Comandos = append(plantilla.Comandos, review.ComandoVerificado{Comando: c.Comando, Exit: c.Exit})
	}
	return plantilla
}
