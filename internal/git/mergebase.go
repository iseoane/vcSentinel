package git

import (
	"fmt"
	"strconv"
	"strings"
)

// MergeBase devuelve el ancestro común más cercano de dos revisiones. Sin
// ancestro común es un error explícito: no hay base sobre la que analizar.
func MergeBase(a, b string) (string, error) {
	salida, err := ejecutarGitSalida("merge-base", a, b)
	if err != nil {
		return "", fmt.Errorf("no hay base común entre %q y %q: %w", a, b, err)
	}
	return strings.TrimSpace(salida), nil
}

// NumstatRango suma las líneas añadidas y borradas del diff entre dos
// revisiones (desde..hasta) para medir el volumen real de una rama. Los
// archivos binarios (sin conteo en el numstat) se ignoran.
func NumstatRango(desde, hasta string) (int, error) {
	salida, err := ejecutarGitSalida("diff", "--numstat", desde+".."+hasta)
	if err != nil {
		return 0, fmt.Errorf("no se pudo medir el volumen de %s..%s: %v", desde, hasta, err)
	}

	total := 0
	for _, linea := range strings.Split(salida, "\n") {
		campos := strings.Fields(linea)
		if len(campos) < 2 {
			continue
		}
		// Los binarios llegan como "-" en ambas columnas: no suman líneas.
		if campos[0] == "-" || campos[1] == "-" {
			continue
		}
		a, errA := strconv.Atoi(campos[0])
		b, errB := strconv.Atoi(campos[1])
		if errA != nil || errB != nil {
			continue
		}
		total += a + b
	}
	return total, nil
}
