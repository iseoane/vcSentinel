package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

// patronUbicacionSalida reconoce el prefijo "ruta:línea[:columna]:" que ya
// usan go vet, go build y el propio compilador Go en su salida de error.
var patronUbicacionSalida = regexp.MustCompile(`^(\S+\.\w+):(\d+)(?::\d+)?:`)

// patronArchivoSuelto reconoce una línea que es solo una ruta de archivo, sin
// más texto: el formato de `gofmt -l`, que lista archivos mal formateados sin
// indicar línea.
var patronArchivoSuelto = regexp.MustCompile(`^(\S+\.\w+)$`)

// proyectarHallazgosValidacion convierte hallazgos deterministas de
// validación (lint/build/test fallido) en review.Hallazgo{Source:
// SourceValidation}, para que AuditarCommit pueda aplicar el supersede de
// T6.2 sobre el hallazgo semántico equivalente. Cada línea de Evidencia con
// un prefijo "archivo:línea" reconocible produce un hallazgo con esa
// ubicación exacta; una línea que es solo una ruta de archivo (gofmt -l)
// produce un hallazgo sin línea, que cubre el archivo entero. Si ninguna
// línea es reconocible, se conserva un único hallazgo sin ubicación en vez de
// descartar la evidencia en silencio.
func proyectarHallazgosValidacion(hallazgos []validation.Hallazgo) []review.Hallazgo {
	var proyectados []review.Hallazgo
	for _, h := range hallazgos {
		ubicaciones := ubicacionesDeEvidencia(h.Evidencia)
		if len(ubicaciones) == 0 {
			proyectados = append(proyectados, nuevoHallazgoDeterminista(h, "", 0, strings.TrimSpace(h.Evidencia)))
			continue
		}
		for _, u := range ubicaciones {
			proyectados = append(proyectados, nuevoHallazgoDeterminista(h, u.archivo, u.linea, u.evidencia))
		}
	}
	return proyectados
}

type ubicacionEvidencia struct {
	archivo   string
	linea     int
	evidencia string
}

func ubicacionesDeEvidencia(evidencia string) []ubicacionEvidencia {
	var ubicaciones []ubicacionEvidencia
	archivosVistos := make(map[string]bool)
	for _, linea := range strings.Split(evidencia, "\n") {
		linea = strings.TrimSpace(linea)
		if linea == "" {
			continue
		}
		if m := patronUbicacionSalida.FindStringSubmatch(linea); m != nil {
			if n, err := strconv.Atoi(m[2]); err == nil {
				ubicaciones = append(ubicaciones, ubicacionEvidencia{archivo: m[1], linea: n, evidencia: linea})
				continue
			}
		}
		if m := patronArchivoSuelto.FindStringSubmatch(linea); m != nil && !archivosVistos[m[1]] {
			archivosVistos[m[1]] = true
			ubicaciones = append(ubicaciones, ubicacionEvidencia{archivo: m[1], evidencia: linea})
		}
	}
	return ubicaciones
}

func nuevoHallazgoDeterminista(h validation.Hallazgo, archivo string, linea int, evidencia string) review.Hallazgo {
	hallazgo := review.Hallazgo{
		Source:      review.SourceValidation,
		Severity:    h.Severity,
		Title:       h.Capability,
		Description: fmt.Sprintf("%s: %s", h.Capability, h.Comando),
		Evidence:    evidencia,
		Confidence:  1.0,
		Status:      review.StatusConfirmed,
		Fixable:     review.FixableManual,
		Location:    review.Ubicacion{Archivo: archivo, LineaInicio: linea},
	}
	hallazgo.Fingerprint = review.Fingerprint(hallazgo)
	return hallazgo
}
