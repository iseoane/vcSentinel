// Package agentshell reúne la lógica compartida entre internal/ops e
// internal/validation para delegar verificación/validación en un agente con
// shell libre: ejecutar un comando por la shell del sistema devolviendo exit
// code + salida combinada, y parsear el contrato "tested: ..." que el agente
// devuelve al terminar. Antes de esta extracción, ambos paquetes tenían una
// copia casi idéntica de este código (una de ellas, byte a byte); vivir aquí
// evita que un cambio de comportamiento (por ejemplo, cómo se detecta un
// exit code) deba replicarse a mano en los dos sitios.
//
// Deliberadamente sin dependencias de internal/ops ni internal/validation:
// así ambos pueden importar este paquete sin crear un ciclo.
package agentshell

import (
	"errors"
	"os/exec"
	"runtime"
	"strings"
)

// Ejecutar lanza comando a través de la shell del sistema (cmd /c en
// Windows, sh -c en el resto) dentro de worktree y devuelve el exit code y la
// salida combinada (stdout+stderr). Es el superset de lo que necesitan los
// dos llamadores: internal/validation usa la salida (fails_when=
// output_not_empty y evidencia de los hallazgos); internal/ops solo necesita
// el exit code y descarta la salida en su punto de llamada.
//
// Los comandos vienen del vassentinel.yml del usuario: ejecutar con shell es
// el diseño (confianza equivalente al propio yml); no se sanitizan aquí.
func Ejecutar(worktree, comando string) (exit int, salida string, err error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/c", comando)
	} else {
		cmd = exec.Command("sh", "-c", comando)
	}
	if worktree != "" {
		cmd.Dir = worktree
	}
	salidaBytes, err := cmd.CombinedOutput()
	salida = string(salidaBytes)
	if err == nil {
		return 0, salida, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), salida, nil
	}
	return -1, salida, err
}

// ParsearContratoTested extrae los comandos de la línea "tested: ..." de la
// salida de un agente delegado y rechaza unavailable / ausencia de contrato.
func ParsearContratoTested(salida string) ([]string, error) {
	if strings.Contains(strings.ToLower(salida), "unavailable") {
		return nil, errors.New("el agente no pudo ejecutar las pruebas (unavailable)")
	}
	for _, linea := range strings.Split(salida, "\n") {
		recortada := strings.TrimSpace(linea)
		idx := strings.Index(recortada, "tested:")
		if idx < 0 {
			continue
		}
		resto := strings.TrimSpace(recortada[idx+len("tested:"):])
		var comandos []string
		for _, c := range strings.Split(resto, ";") {
			c = strings.TrimSpace(c)
			if c != "" {
				comandos = append(comandos, c)
			}
		}
		if len(comandos) == 0 {
			return nil, errors.New("contrato tested vacío")
		}
		return comandos, nil
	}
	return nil, errors.New("la salida no contiene un contrato tested")
}
