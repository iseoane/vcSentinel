package main

import (
	"regexp"
	"strings"
)

// patronReglasVolumenMarcado reconoce el bloque delimitado por
// marcadorInicio/marcadorFin sea cual sea el texto interior (redacción
// distinta entre versiones) y sea cual sea la mezcla de finales de línea.
// Es la corrección de B12: antes de esto, init/uninit comparaban contra el
// texto literal exacto de reglasVolumen, así que un cambio de redacción entre
// versiones dejaba bloques huérfanos que uninit no podía retirar y provocaba
// que init inyectara un segundo bloque en vez de reconocer el existente.
var patronReglasVolumenMarcado = regexp.MustCompile(patronDeBloqueMarcado())

// patronReglasVolumenLegado reconoce el bloque EXACTO (sin marcadores) que
// las versiones anteriores a los marcadores inyectaban. Se conserva solo para
// detectar y retirar/migrar esos bloques ya existentes; ninguna versión desde
// esta vuelve a escribir en este formato.
var patronReglasVolumenLegado = regexp.MustCompile(patronDeBloque(reglasVolumenLegado))

func patronDeBloqueMarcado() string {
	cabecera := patronDeBloque("\n" + marcadorInicio + "\n")
	cola := patronDeBloque("\n" + marcadorFin + "\n")
	return cabecera + `(?s:.*?)` + cola
}

func patronDeBloque(bloque string) string {
	lineas := strings.Split(strings.ReplaceAll(bloque, "\r\n", "\n"), "\n")
	partes := make([]string, 0, len(lineas))
	for _, linea := range lineas {
		partes = append(partes, regexp.QuoteMeta(linea))
	}
	if len(partes) > 0 && partes[0] == "" {
		// El bloque empieza con salto de línea (todos los que maneja este
		// archivo lo hacen). Ese salto separa el bloque del contenido previo,
		// pero si el bloque es lo primero del archivo no hay nada antes que
		// lo preceda: versiones antiguas de init llegaron a escribirlo así en
		// archivos nuevos (p. ej. .claudecode.md de este propio repo, con dos
		// copias del bloque legado pegadas desde el byte 0). Exigir siempre
		// "\r?\n" antes dejaba esa primera copia sin reconocer y, por tanto,
		// sin poder retirarla ni migrarla.
		return `(?:\A|\r?\n)` + strings.Join(partes[1:], "\r?\n")
	}
	return strings.Join(partes, "\r?\n")
}

// contieneReglasVolumen indica si el archivo ya trae el bloque, marcado (de
// esta versión o de una futura que comparta los marcadores) o legado (de una
// versión anterior a los marcadores), sea cual sea la mezcla de finales de
// línea con la que esté escrito.
func contieneReglasVolumen(contenido string) bool {
	return patronReglasVolumenMarcado.MatchString(contenido) || patronReglasVolumenLegado.MatchString(contenido)
}

// quitarReglasVolumen retira TODAS las apariciones del bloque, marcado o
// legado, y deja el resto del archivo byte a byte como estaba, incluidos sus
// finales de línea.
func quitarReglasVolumen(contenido string) string {
	contenido = quitarTodasLasCoincidencias(patronReglasVolumenMarcado, contenido)
	return quitarTodasLasCoincidencias(patronReglasVolumenLegado, contenido)
}

// quitarTodasLasCoincidencias aplica el patrón hasta que deja de cambiar algo
// el resultado (punto fijo). Una sola pasada de ReplaceAllString no basta
// cuando dos copias del bloque están pegadas con un único salto de línea de
// separación (el caso real de .claudecode.md, escrito por un binario tan
// antiguo que ni siquiera anteponía su propio salto de línea): la primera
// copia consume ese único "\r?\n" como su propio final, y a la segunda copia
// ya no le queda un salto de línea delante para que su propio patrón la
// reconozca en la misma pasada. Repetir hasta el punto fijo lo resuelve: tras
// quitar la primera copia, la segunda queda al principio del texto resultante
// y la rama "\A" del patrón la reconoce en la siguiente vuelta.
func quitarTodasLasCoincidencias(patron *regexp.Regexp, contenido string) string {
	for {
		siguiente := patron.ReplaceAllString(contenido, "")
		if siguiente == contenido {
			return contenido
		}
		contenido = siguiente
	}
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
