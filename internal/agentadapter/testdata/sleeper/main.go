package main

// Helper de pruebas para internal/agentadapter: duerme os.Args[1] segundos
// (0 si no es un número) y devuelve el último argumento recibido. Sirve para
// verificar el timeout del adaptador CLI sin depender de un agente real.

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

func main() {
	segundos := 0
	for _, arg := range os.Args[1:] {
		if n, err := strconv.Atoi(arg); err == nil {
			segundos = n
			break
		}
	}
	time.Sleep(time.Duration(segundos) * time.Second)
	if len(os.Args) > 0 {
		fmt.Print(os.Args[len(os.Args)-1])
	}
}
