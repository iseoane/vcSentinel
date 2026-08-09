package git

import (
	"strings"
)

// VolumenPendiente separa las líneas que frenan al guardián de las que solo se
// informan. La distinción es el núcleo de la regla: el guardián existe para
// que el código siga siendo revisable, y la documentación y los archivos
// generados no se revisan línea a línea.
type VolumenPendiente struct {
	// Bloqueante son las líneas de código, tests y configuración escrita a
	// mano. Es el número que decide PEQUENO / PUNTO_OPTIMO / CRITICO.
	Bloqueante int
	// Informativo son las líneas de documentación y de archivos generados.
	// Se muestran para que el usuario sepa cuánto lleva pendiente, pero nunca
	// disparan el freno.
	Informativo int
	Estado      string
}

// MedirVolumen mide el volumen pendiente del worktree reutilizando
// ObtenerArchivosModificados, que ya incluye tanto los archivos rastreados
// (git diff HEAD) como los no rastreados (git status --uall). Así check y
// slice comparten una única fuente de verdad sobre "líneas pendientes",
// repartidas por clase de archivo.
func MedirVolumen() (VolumenPendiente, error) {
	archivos, err := ObtenerArchivosModificados()
	if err != nil {
		return VolumenPendiente{Estado: "ERROR"}, err
	}

	var volumen VolumenPendiente
	for _, archivo := range archivos {
		if CuentaParaVolumen(ClaseArchivo(archivo.Ruta)) {
			volumen.Bloqueante += archivo.Lineas
			continue
		}
		volumen.Informativo += archivo.Lineas
	}
	volumen.Estado = clasificarEstado(volumen.Bloqueante)
	return volumen, nil
}

// CheckDiffLimits devuelve el volumen BLOQUEANTE y su estado. Se conserva como
// la vía corta para los llamadores que solo necesitan el veredicto; quien
// quiera mostrar también las líneas informativas usa MedirVolumen.
func CheckDiffLimits() (int, string, error) {
	volumen, err := MedirVolumen()
	if err != nil {
		return 0, "ERROR", err
	}
	return volumen.Bloqueante, volumen.Estado, nil
}

func contarLineasAnadidas(diff string) int {
	contador := 0
	for _, linea := range strings.Split(diff, "\n") {
		if strings.HasPrefix(linea, "+") && !strings.HasPrefix(linea, "+++") {
			contador++
		}
	}
	return contador
}

func clasificarEstado(lineas int) string {
	switch {
	case lineas >= 200 && lineas <= 400:
		return "PUNTO_OPTIMO"
	case lineas > 400:
		return "CRITICO"
	default:
		return "PEQUENO"
	}
}
