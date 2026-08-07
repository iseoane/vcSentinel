package main

// Helper de pruebas para internal/agentadapter: duerme el primer argumento
// numérico en segundos (0 si no hay) y responde según el modo de invocación:
//
//   - "-p <prompt>" imprime el prompt recibido como argumento (compatibilidad
//     con el transporte antiguo);
//   - "-p" SIN argumento lee el prompt completo de stdin y lo imprime (modo
//     stdin de claude);
//   - sin "-p" (p. ej. "run" de opencode) también lee el prompt de stdin.
//
// El modo de argumento gana sobre stdin: si "-p" lleva prompt y además hay
// entrada en stdin, se imprime el argumento, para que los tests distingan el
// transporte usado por el adaptador.
//
// Sirve para verificar el timeout y el transporte del prompt del adaptador
// CLI sin depender de un agente real.

import (
	"fmt"
	"io"
	"os"
	"strconv"
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

	if posicionP >= 0 && posicionP < len(os.Args)-1 {
		fmt.Print(os.Args[posicionP+1])
		return
	}
	if datos, err := io.ReadAll(os.Stdin); err == nil {
		fmt.Print(string(datos))
	}
}
