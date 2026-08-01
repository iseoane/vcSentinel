package git

import (
	"bytes"
	"os/exec"
	"strings"
)

func CheckDiffLimits() (int, string, error) {
	cmd := exec.Command("git", "diff", "HEAD")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return 0, "ERROR", err
	}

	addedLinesCount := contarLineasAnadidas(out.String())
	return addedLinesCount, clasificarEstado(addedLinesCount), nil
}

func contarLineasAnadidas(diff string) int {
	contador := 0
	for _, linea := range strings.Split(diff, "\n") {
		if strings.HasPrefix(linea, "+") && !strings.HasPrefix(linea, "+++") {
			contador++
		}
	}
	return contador
}

func clasificarEstado(lineas int) string {
	switch {
	case lineas >= 200 && lineas <= 400:
		return "PUNTO_OPTIMO"
	case lineas > 400:
		return "CRITICO"
	default:
		return "PEQUENO"
	}
}
