package git

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
)

type ArchivoModificado struct {
	Ruta   string
	Lineas int
	Capa   string
}

func ObtenerArchivosModificados() ([]ArchivoModificado, error) {
	cmd := exec.Command("git", "diff", "HEAD", "--numstat")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}

	var resultado []ArchivoModificado
	lineas := strings.Split(out.String(), "\n")

	for _, linea := range lineas {
		linea = strings.TrimSpace(linea)
		if linea == "" {
			continue
		}
		campos := strings.Fields(linea)
		if len(campos) < 3 {
			continue
		}

		addCount, err := strconv.Atoi(campos[0])
		if err != nil {
			continue // Ignora binarios marcados con "-"
		}

		ruta := campos[2]
		capa := clasificarCapa(ruta)

		resultado = append(resultado, ArchivoModificado{Ruta: ruta, Lineas: addCount, Capa: capa})
	}
	return resultado, nil
}

func clasificarCapa(ruta string) string {
	ext := filepath.Ext(ruta)
	base := filepath.Base(ruta)
	rutaLower := strings.ToLower(ruta)

	if strings.Contains(rutaLower, "test") || strings.Contains(base, "spec") {
		return "test"
	} else if strings.Contains(rutaLower, "frontend") || ext == ".tsx" || ext == ".jsx" {
		return "frontend"
	} else if ext == ".json" || ext == ".yaml" || ext == ".toml" || base == "requirements.txt" {
		return "config"
	}
	return "backend"
}

func FragmentarYCommitear(archivos []ArchivoModificado, adapter agentadapter.AgentAdapter) error {
	porCapas := map[string][]ArchivoModificado{"config": {}, "backend": {}, "frontend": {}, "test": {}}
	for _, f := range archivos {
		porCapas[f.Capa] = append(porCapas[f.Capa], f)
	}

	orden := []string{"config", "backend", "frontend", "test"}
	batchNumero := 1

	for _, capa := range orden {
		var loteActual []string
		lineasAcumuladas := 0

		for _, f := range porCapas[capa] {
			if lineasAcumuladas+f.Lineas > 400 && len(loteActual) > 0 {
				if err := consolidarCommitLocal(loteActual, capa, batchNumero, adapter); err != nil {
					return err
				}
				batchNumero++
				loteActual = []string{}
				lineasAcumuladas = 0
			}
			loteActual = append(loteActual, f.Ruta)
			lineasAcumuladas += f.Lineas
		}

		if len(loteActual) > 0 {
			if err := consolidarCommitLocal(loteActual, capa, batchNumero, adapter); err != nil {
				return err
			}
			batchNumero++
		}
	}
	return nil
}

func consolidarCommitLocal(rutas []string, capa string, numero int, adapter agentadapter.AgentAdapter) error {
	argsAdd := append([]string{"add"}, rutas...)
	if err := exec.Command("git", argsAdd...).Run(); err != nil {
		return err
	}

	mensajeCommit, err := adapter.ObtenerMensajeCommit(rutas, capa, numero)
	if err != nil {
		mensajeCommit = fmt.Sprintf("chore(slice): auto-fragmented %s batch #%d", capa, numero)
	}

	return exec.Command("git", "commit", "-m", mensajeCommit).Run()
}
