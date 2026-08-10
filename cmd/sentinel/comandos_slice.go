package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
)

// codigoSalidaDecisionesPendientes es el exit de `slice plan` cuando quedan
// decisiones que solo el usuario puede responder. Se distingue del 1 (error)
// para que un orquestador sepa que debe preguntar, no que algo falló.
const codigoSalidaDecisionesPendientes = 3

// ejecutarSlicePlan emite el plan de fragmentación sin commitear nada y
// devuelve el código de salida: 0 si no hay decisiones pendientes, 3 si las
// hay. El modo interactivo de `sentinel slice` queda intacto: esta es una vía
// añadida, no un reemplazo.
func ejecutarSlicePlan(salida io.Writer, args []string) int {
	comoJSON := false
	for _, arg := range args {
		if arg == "--json" {
			comoJSON = true
			continue
		}
		fmt.Fprintf(salida, "❌ Opción desconocida para 'slice plan': %s\n", arg)
		return 1
	}

	plan, err := git.ConstruirPlanParaAgente()
	if err != nil {
		fmt.Fprintf(salida, "❌ %v\n", err)
		return 1
	}

	if comoJSON {
		codificador := json.NewEncoder(salida)
		codificador.SetIndent("", "  ")
		if err := codificador.Encode(plan); err != nil {
			fmt.Fprintf(salida, "❌ No se pudo serializar el plan: %v\n", err)
			return 1
		}
	} else {
		imprimirPlanSerializado(salida, plan)
	}

	if len(plan.DecisionesPendientes) > 0 {
		return codigoSalidaDecisionesPendientes
	}
	return 0
}

func imprimirPlanSerializado(salida io.Writer, plan *git.PlanSerializado) {
	fmt.Fprintf(salida, "🧭 Plan %s (árbol %s)\n", plan.PlanID[:12], plan.EstadoWorktree[:12])
	if len(plan.Lotes) == 0 {
		fmt.Fprintln(salida, "📭 No hay modificaciones pendientes para procesar.")
	}
	for _, lote := range plan.Lotes {
		fmt.Fprintf(salida, "\n📦 Lote #%d [%s] — %d líneas\n", lote.Numero, lote.Capa, lote.Lineas)
		fmt.Fprintf(salida, "   💬 %s\n", lote.Mensaje)
		for _, ruta := range lote.Rutas {
			fmt.Fprintf(salida, "   • %s\n", ruta)
		}
	}
	for _, decision := range plan.DecisionesPendientes {
		fmt.Fprintf(salida, "\n❓ Decisión pendiente %s\n   %s\n   Opciones: %v\n", decision.ID, decision.Pregunta, decision.Opciones)
	}
	if len(plan.DecisionesPendientes) > 0 {
		fmt.Fprintln(salida, "\n⚠️ El plan no se puede aplicar hasta que el usuario responda cada decisión.")
	}
}
