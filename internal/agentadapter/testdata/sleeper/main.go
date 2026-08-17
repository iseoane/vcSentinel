package main

// Helper de pruebas para internal/agentadapter: duerme el primer argumento
// numérico en segundos (0 si no hay) y responde según el modo de invocación:
//
//   - "-p <prompt>" imprime el prompt recibido como argumento (compatibilidad
//     con el transporte antiguo);
//   - "-p" SIN argumento, o seguido de otra bandera ("--algo"), lee el
//     prompt completo de stdin y lo imprime (modo stdin de claude, incluidas
//     las variantes aisladas de revisión/commit que añaden más banderas tras
//     "-p", p. ej. "-p --safe-mode --tools ...");
//   - sin "-p" (p. ej. "run" de opencode) también lee el prompt de stdin.
//
// El modo de argumento gana sobre stdin: si "-p" lleva un valor que NO
// empieza por "-" y además hay entrada en stdin, se imprime el argumento,
// para que los tests distingan el transporte usado por el adaptador.
//
// Sirve para verificar el timeout y el transporte del prompt del adaptador
// CLI sin depender de un agente real.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

func main() {
	segundos := 0
	posicionP := -1
	for i, arg := range os.Args[1:] {
		if n, err := strconv.Atoi(arg); err == nil {
			segundos = n
			break
		}
		if arg == "-p" {
			posicionP = i + 1
		}
	}
	time.Sleep(time.Duration(segundos) * time.Second)

	prompt := ""
	if posicionP >= 0 && posicionP < len(os.Args)-1 && !strings.HasPrefix(os.Args[posicionP+1], "-") {
		prompt = os.Args[posicionP+1]
	} else if datos, err := io.ReadAll(os.Stdin); err == nil {
		prompt = string(datos)
	}

	if ruta := os.Getenv("VAS_SENTINEL_TEST_CAPTURE"); ruta != "" {
		dir, _ := os.Getwd()
		datos, _ := json.Marshal(struct {
			Args  []string `json:"args"`
			Dir   string   `json:"dir"`
			Stdin string   `json:"stdin"`
		}{Args: os.Args[1:], Dir: dir, Stdin: prompt})
		_ = os.WriteFile(ruta, datos, 0600)
	}
	// VAS_SENTINEL_TEST_FAIL simula un agente que falla con un mensaje de
	// error en stderr, para probar que el adaptador captura y propaga ese
	// detalle en vez de descartarlo.
	if fallo := os.Getenv("VAS_SENTINEL_TEST_FAIL"); fallo != "" {
		fmt.Fprint(os.Stderr, fallo)
		os.Exit(1)
	}
	if salida := os.Getenv("VAS_SENTINEL_TEST_OUTPUT"); salida != "" {
		fmt.Print(salida)
		return
	}
	fmt.Print(prompt)
}
