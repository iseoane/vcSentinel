package main

import (
	"fmt"
	"io"

	"github.com/ISeoane-Quental/vas.sentinel/internal/consent"
)

func ejecutarConsentimientoDiff(salida io.Writer, path string, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(salida, "❌ Uso: sentinel consentimiento-diff otorgar|revocar|estado")
		return 1
	}
	switch args[0] {
	case "otorgar":
		estado, err := consent.OtorgarDiffExterno(path)
		if err != nil {
			fmt.Fprintf(salida, "❌ No se pudo otorgar el consentimiento: %v\n", err)
			return 1
		}
		imprimirEstadoConsentimiento(salida, estado)
	case "revocar":
		if err := consent.RevocarDiffExterno(path); err != nil {
			fmt.Fprintf(salida, "❌ No se pudo revocar el consentimiento: %v\n", err)
			return 1
		}
		estado, err := consent.EstadoDiffExterno(path)
		if err != nil {
			fmt.Fprintf(salida, "❌ No se pudo leer el estado: %v\n", err)
			return 1
		}
		imprimirEstadoConsentimiento(salida, estado)
	case "estado":
		estado, err := consent.EstadoDiffExterno(path)
		if err != nil {
			fmt.Fprintf(salida, "❌ No se pudo leer el estado: %v\n", err)
			return 1
		}
		imprimirEstadoConsentimiento(salida, estado)
	default:
		fmt.Fprintf(salida, "❌ Acción desconocida %q. Usa otorgar, revocar o estado.\n", args[0])
		return 1
	}
	return 0
}

func imprimirEstadoConsentimiento(salida io.Writer, estado consent.EstadoDiff) {
	valor := "revocado"
	if estado.Otorgado {
		valor = "otorgado"
	}
	fmt.Fprintf(salida, "Consentimiento de diff externo: %s\nRepositorio: %s\nUsuario local: %s\n", valor, estado.Repositorio, estado.Usuario)
	if estado.Otorgado {
		fmt.Fprintf(salida, "Otorgado en: %s\n", estado.OtorgadoEn.Format("2006-01-02T15:04:05Z"))
	}
}
