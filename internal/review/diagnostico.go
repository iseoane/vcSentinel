package review

import (
	"regexp"
	"strings"
)

// prefijoCausaProveedor encabeza una razón enriquecida. También sirve de
// guarda de idempotencia: un fallo ya enriquecido no se vuelve a enriquecer.
const prefijoCausaProveedor = "provider reported: "

const (
	maxCausasProveedor     = 3
	maxLongitudCausa       = 200
	marcadorCausaProveedor = "Error: "
)

// secuenciaANSI reconoce el coloreado que los CLI de agente emiten en su
// flujo. Sin quitarlo, una causa quedaría partida por bytes de control y no
// coincidiría con nada legible.
var secuenciaANSI = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// causasDelProveedor extrae las líneas de error que el proveedor imprimió
// dentro de su propio flujo.
//
// Por qué existe: cuando una herramienta del revisor falla, el revisor no se
// detiene. Sigue trabajando de la forma cara que le queda —leer ficheros
// enteros en vez de buscar— hasta agotar el presupuesto. Lo que llega al
// operador es entonces un timeout, que es la consecuencia, con la causa real
// enterrada cientos de líneas más abajo. Esto la saca al frente.
//
// Deliberadamente no reconoce herramientas concretas. Mantener una tabla de
// "el agente X necesita el binario Y" sería una lista que envejece con cada
// proveedor; lo que se reporta aquí es lo que el proveedor dijo que falló.
func causasDelProveedor(texto string) []string {
	limpio := secuenciaANSI.ReplaceAllString(texto, "")
	vistas := map[string]bool{}
	causas := make([]string, 0, maxCausasProveedor)
	for _, linea := range strings.Split(limpio, "\n") {
		indice := strings.Index(linea, marcadorCausaProveedor)
		if indice < 0 {
			continue
		}
		causa := strings.TrimSpace(linea[indice+len(marcadorCausaProveedor):])
		if causa == "" || vistas[causa] {
			continue
		}
		if len(causa) > maxLongitudCausa {
			causa = causa[:maxLongitudCausa] + "…"
		}
		vistas[causa] = true
		causas = append(causas, causa)
		if len(causas) == maxCausasProveedor {
			break
		}
	}
	return causas
}

// razonConCausa antepone las causas que el proveedor reportó, conservando el
// texto original detrás: la traza completa sigue siendo evidencia y no se
// descarta, solo deja de ser lo primero que se lee.
func razonConCausa(mensaje string) string {
	if strings.HasPrefix(mensaje, prefijoCausaProveedor) {
		return mensaje
	}
	causas := causasDelProveedor(mensaje)
	if len(causas) == 0 {
		return mensaje
	}
	return prefijoCausaProveedor + strings.Join(causas, "; ") + " | " + mensaje
}
