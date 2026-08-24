package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/consent"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
)

// codigoSalidaDecisionesPendientes es el exit de `slice plan` cuando quedan
// decisiones que solo el usuario puede responder. Se distingue del 1 (error)
// para que un orquestador sepa que debe preguntar, no que algo falló.
const codigoSalidaDecisionesPendientes = 3

var nuevoAgentAdapterParaMensaje = agentadapter.NewAgentAdapterParaMensaje

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

	raiz, err := git.ObtenerRaizWorktree()
	if err != nil {
		fmt.Fprintf(salida, "❌ %v\n", err)
		return 1
	}
	var adapter agentadapter.AgentAdapter
	// El micro-diff contiene código fuente: requiere solicitud versionada y
	// consentimiento local del usuario para este repositorio.
	if permiteDiffAgenteExterno(raiz) {
		adapter, _ = nuevoAgentAdapterParaMensaje(raiz)
	}
	plan, err := git.ConstruirPlanParaAgenteConAdapter(adapter)
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

func permiteDiffAgenteExterno(raiz string) bool {
	if !config.RepositorioSolicitaDiffAgenteExterno(raiz) {
		return false
	}
	estado, err := consent.EstadoDiffExterno(raiz)
	return err == nil && estado.Otorgado
}

// ejecutarSliceApply ejecuta un plan previamente emitido, solo con las
// respuestas explícitas del usuario. Devuelve 0 si commiteó, 1 si se negó.
func ejecutarSliceApply(salida io.Writer, args []string) int {
	rutaPlan, rutaRespuestas := "", ""
	for i := 0; i < len(args); i++ {
		valor := ""
		if i+1 < len(args) {
			valor = args[i+1]
		}
		switch args[i] {
		case "--plan":
			rutaPlan, i = valor, i+1
		case "--answers":
			rutaRespuestas, i = valor, i+1
		default:
			fmt.Fprintf(salida, "❌ Opción desconocida para 'slice apply': %s\n", args[i])
			return 1
		}
	}
	if rutaPlan == "" || rutaRespuestas == "" {
		fmt.Fprintln(salida, "❌ "+usoSliceApply)
		return 1
	}

	var plan git.PlanSerializado
	if err := leerJSON(rutaPlan, &plan); err != nil {
		fmt.Fprintf(salida, "❌ No se pudo leer el plan: %v\n", err)
		return 1
	}
	var respuestas git.RespuestasPlan
	if err := leerJSON(rutaRespuestas, &respuestas); err != nil {
		fmt.Fprintf(salida, "❌ No se pudieron leer las respuestas: %v\n", err)
		return 1
	}

	resultados, err := git.AplicarPlanAprobado(&plan, respuestas)
	if err != nil {
		fmt.Fprintf(salida, "❌ %v\n", err)
		return 1
	}
	for _, resultado := range resultados {
		fmt.Fprintf(salida, "✅ %s [%s] %s (%d archivos)\n", resultado.Hash, resultado.Capa, resultado.Mensaje, resultado.Archivos)
	}
	fmt.Fprintf(salida, "\n🎉 %d commits creados a partir del plan aprobado.\n", len(resultados))
	return 0
}

func leerJSON(ruta string, destino any) error {
	contenido, err := os.ReadFile(ruta)
	if err != nil {
		return err
	}
	return json.Unmarshal(contenido, destino)
}

func imprimirPlanSerializado(salida io.Writer, plan *git.PlanSerializado) {
	fmt.Fprintf(salida, "🧭 Plan %s (árbol %s)\n", plan.PlanID[:12], plan.EstadoWorktree[:12])
	if plan.Explanation != "" {
		fmt.Fprintf(salida, "ℹ️ %s\n", plan.Explanation)
	}
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
