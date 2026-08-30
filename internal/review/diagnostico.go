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
		// El marcador se exige al PRINCIPIO de la línea ya limpia, no en
		// cualquier posición: "Error: " aparece dentro del código fuente y de
		// la evidencia que el revisor cita, y buscarlo suelto ascendería ese
		// texto a causa del fallo.
		desnuda := strings.TrimSpace(linea)
		if !strings.HasPrefix(desnuda, marcadorCausaProveedor) {
			continue
		}
		causa := recortarCausa(strings.TrimSpace(strings.TrimPrefix(desnuda, marcadorCausaProveedor)))
		// La deduplicación mira el valor YA recortado, que es el que se
		// publica. Comparar el original y guardar el recorte dejaría pasar
		// dos veces la misma causa larga y agotaría el cupo con repeticiones.
		if causa == "" || vistas[causa] {
			continue
		}
		vistas[causa] = true
		causas = append(causas, causa)
		if len(causas) == maxCausasProveedor {
			break
		}
	}
	return causas
}

// recortarCausa acota por runas, no por bytes: un corte a mitad de un carácter
// multibyte produciría texto inválido justo en lo primero que lee el operador.
func recortarCausa(causa string) string {
	runas := []rune(causa)
	if len(runas) <= maxLongitudCausa {
		return causa
	}
	return string(runas[:maxLongitudCausa]) + "…"
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

// CompactProviderCause returns the safe form for an operational event: it
// retains only the causes printed by the provider, without carrying the full
// trace, which can contain hundreds of lines and the resulting timeout.
// Reasons with no recognizable provider cause are retained so admission or
// configuration failures are not hidden.
func CompactProviderCause(message string) string {
	if causes := causasDelProveedor(message); len(causes) > 0 {
		return prefijoCausaProveedor + strings.Join(causes, "; ")
	}
	if !strings.HasPrefix(message, prefijoCausaProveedor) {
		return message
	}
	header := strings.TrimSpace(strings.TrimPrefix(message, prefijoCausaProveedor))
	if short, _, ok := strings.Cut(header, " | "); ok {
		header = strings.TrimSpace(short)
	}
	return prefijoCausaProveedor + recortarCausa(header)
}
