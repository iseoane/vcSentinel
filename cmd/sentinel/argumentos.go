package main

import (
	"fmt"
	"strings"
)

// subcomandosSinArgumentos son los que no admiten ningún argumento. Hasta
// T0.5 el dispatcher ignoraba os.Args[2:] y `sentinel check --loquesea`
// salía 0 sin protestar (H1/B6): un flag mal escrito parecía funcionar.
//
// Riesgo asumido a propósito: esto rompe los scripts que hoy pasan basura sin
// enterarse. Enterarse es justamente el comportamiento deseado.
var subcomandosSinArgumentos = map[string]bool{
	"init":      true,
	"uninit":    true,
	"lint":      true,
	"rebase":    true,
	"install":   true,
	"upgrade":   true,
	"uninstall": true,
	"tui":       true,
}

// subcomandosDeSlice son las vías no interactivas de slice, que sí tienen
// flags propios y los validan ellas mismas.
var subcomandosDeSlice = map[string]bool{"plan": true, "apply": true}

type flagsCheck struct {
	jsonOut bool
	staged  bool
}

func parsearFlagsCheck(args []string) (flagsCheck, error) {
	flags := flagsCheck{}
	for _, arg := range args {
		switch arg {
		case "--json":
			flags.jsonOut = true
		case "--staged":
			flags.staged = true
		default:
			return flags, fmt.Errorf("sentinel check accepts --json and --staged, received %q", arg)
		}
	}
	return flags, nil
}

// validarArgumentos devuelve el mensaje de error si el subcomando recibió
// argumentos que no admite, o "" si son válidos. Los subcomandos con parser
// propio (review, status, pr) no se tocan aquí: validan sus flags ellos.
func validarArgumentos(subcomando string, extras []string) string {
	if len(extras) == 0 {
		return ""
	}
	if subcomando == "check" {
		if _, err := parsearFlagsCheck(extras); err == nil {
			return ""
		}
		return fmt.Sprintf(
			"❌ 'sentinel check' accepts '--json' and '--staged' and received: %s. Run 'sentinel help' to see the correct usage.",
			strings.Join(extras, " "))
	}
	if subcomando == "metrics" {
		if _, err := parseMetricsArgs(extras); err == nil {
			return ""
		}
		return fmt.Sprintf(
			"❌ 'sentinel metrics' accepts '--json' and received: %s. Run 'sentinel help metrics' to see the correct usage.",
			strings.Join(extras, " "))
	}
	if subcomando == "slice" {
		if subcomandosDeSlice[extras[0]] {
			return ""
		}
		return fmt.Sprintf(
			"❌ 'sentinel slice' no admite el argumento %q. Usa 'slice plan' o 'slice apply', o ejecuta 'sentinel help'.",
			extras[0])
	}
	if !subcomandosSinArgumentos[subcomando] {
		return ""
	}
	return fmt.Sprintf(
		"❌ 'sentinel %s' no admite argumentos y recibió: %s. Ejecuta 'sentinel help' para ver el uso correcto.",
		subcomando, strings.Join(extras, " "))
}
