package main

import (
	"regexp"
	"strings"
)

// patronReglasVolumen reconoce el bloque reglasVolumen con cualquier mezcla de
// finales de línea. Nace de la propia constante, así que no puede
// desincronizarse de ella: si el bloque cambia, el patrón cambia con él.
//
// Es la corrección de B10. Comparar con strings.Contains contra una constante
// que solo usa \n fallaba en los archivos reales de Windows, que `* text=auto`
// deja con finales mixtos: init dejaba de ser idempotente y uninit no podía
// retirar un bloque que no reconocía. Se normaliza la COMPARACIÓN, nunca el
// archivo del usuario: reescribir sus finales de línea para poder compararlo
// sería un daño mayor que el defecto.
var patronReglasVolumen = regexp.MustCompile(patronDeBloque(reglasVolumen))

func patronDeBloque(bloque string) string {
	lineas := strings.Split(strings.ReplaceAll(bloque, "\r\n", "\n"), "\n")
	partes := make([]string, 0, len(lineas))
	for _, linea := range lineas {
		partes = append(partes, regexp.QuoteMeta(linea))
	}
	return strings.Join(partes, "\r?\n")
}

// contieneReglasVolumen indica si el archivo ya trae el bloque, sea cual sea
// la mezcla de finales de línea con la que esté escrito.
func contieneReglasVolumen(contenido string) bool {
	return patronReglasVolumen.MatchString(contenido)
}

// quitarReglasVolumen retira TODAS las apariciones del bloque y deja el resto
// del archivo byte a byte como estaba, incluidos sus finales de línea.
func quitarReglasVolumen(contenido string) string {
	return patronReglasVolumen.ReplaceAllString(contenido, "")
}

// reglasVolumenPara adapta el bloque al final de línea que ya domina en el
// archivo destino. Escribirlo siempre en LF es lo que iba sembrando los
// archivos mixtos que rompían la comparación.
func reglasVolumenPara(contenido string) string {
	if finalDeLineaDominante(contenido) == "\r\n" {
		return strings.ReplaceAll(reglasVolumen, "\n", "\r\n")
	}
	return reglasVolumen
}

// finalDeLineaDominante devuelve "\r\n" solo si el archivo usa CRLF más veces
// que LF a secas. Un archivo vacío o sin saltos se trata como LF, que es el
// formato de la constante y el de Debian.
func finalDeLineaDominante(contenido string) string {
	crlf := strings.Count(contenido, "\r\n")
	lf := strings.Count(contenido, "\n") - crlf
	if crlf > lf {
		return "\r\n"
	}
	return "\n"
}
