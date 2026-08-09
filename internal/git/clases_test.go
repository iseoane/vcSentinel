package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestClaseArchivo fija la taxonomía por clase de archivo, independiente de la
// capa (que solo ordena los lotes de slice). Incluye los falsos positivos
// reales de ClasificarCapa, que trata "latest" o "contest" como tests por
// contener la subcadena "test".
func TestClaseArchivo(t *testing.T) {
	tests := []struct {
		ruta     string
		esperado string
	}{
		// Documentación: no bloquea el guardián.
		{ruta: "README.md", esperado: ClaseDocs},
		{ruta: "docs/arquitectura/replanteamiento-objetivo.md", esperado: ClaseDocs},
		{ruta: filepath.Join("docs", "reingenieria", "f1-gate.md"), esperado: ClaseDocs},
		{ruta: "CHANGELOG.rst", esperado: ClaseDocs},

		// Generado: nadie lo revisa línea a línea, no bloquea.
		{ruta: "go.sum", esperado: ClaseGenerada},
		{ruta: "package-lock.json", esperado: ClaseGenerada},
		{ruta: "yarn.lock", esperado: ClaseGenerada},
		{ruta: "api/v1/user.pb.go", esperado: ClaseGenerada},
		{ruta: "internal/store/modelo_gen.go", esperado: ClaseGenerada},

		// Configuración escrita a mano: es código que se ejecuta, sí bloquea.
		{ruta: "go.mod", esperado: ClaseConfig},
		{ruta: "docker-compose.yml", esperado: ClaseConfig},
		{ruta: ".github/workflows/ci.yml", esperado: ClaseConfig},
		{ruta: "requirements.txt", esperado: ClaseConfig},

		// Tests: por sufijo y por directorio, nunca por subcadena suelta.
		{ruta: "internal/git/diff_test.go", esperado: ClaseTest},
		{ruta: filepath.Join("internal", "agentadapter", "testdata", "sleeper", "main.go"), esperado: ClaseTest},
		{ruta: "web/componente.spec.ts", esperado: ClaseTest},

		// Código: incluidos los falsos positivos históricos de ClasificarCapa.
		{ruta: "internal/git/diff.go", esperado: ClaseSource},
		{ruta: "latest/version.go", esperado: ClaseSource},
		{ruta: "contest.go", esperado: ClaseSource},
		{ruta: "internal/setup/install.go", esperado: ClaseSource},
	}

	for _, tt := range tests {
		t.Run(tt.ruta, func(t *testing.T) {
			if obtenido := ClaseArchivo(tt.ruta); obtenido != tt.esperado {
				t.Errorf("ClaseArchivo(%q) = %q, esperado %q", tt.ruta, obtenido, tt.esperado)
			}
		})
	}
}

// TestCuentaParaVolumen fija qué clases frenan al guardián. El guardián mide
// revisabilidad de código, no bytes: la documentación y lo generado se
// informan pero no bloquean.
func TestCuentaParaVolumen(t *testing.T) {
	tests := []struct {
		clase    string
		esperado bool
	}{
		{clase: ClaseSource, esperado: true},
		{clase: ClaseTest, esperado: true},
		{clase: ClaseConfig, esperado: true},
		{clase: ClaseDocs, esperado: false},
		{clase: ClaseGenerada, esperado: false},
	}

	for _, tt := range tests {
		t.Run(tt.clase, func(t *testing.T) {
			if obtenido := CuentaParaVolumen(tt.clase); obtenido != tt.esperado {
				t.Errorf("CuentaParaVolumen(%q) = %v, esperado %v", tt.clase, obtenido, tt.esperado)
			}
		})
	}
}

// TestMedirVolumenExcluyeDocumentacionYGenerado es el test de la regla: un
// documento enorme se reporta como informativo y NO pone el guardián en
// CRITICO. Sin esto, escribir documentación de arquitectura bloquea el
// desarrollo sin aportar ninguna seguridad.
func TestMedirVolumenExcluyeDocumentacionYGenerado(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioPrueba(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	escribirLineas(t, filepath.Join(dir, "ARQUITECTURA.md"), 1000)
	escribirLineas(t, filepath.Join(dir, "go.sum"), 500)
	escribirLineas(t, filepath.Join(dir, "nuevo.go"), 10)

	volumen, err := MedirVolumen()
	if err != nil {
		t.Fatalf("MedirVolumen devolvió error: %v", err)
	}
	if volumen.Bloqueante != 10 {
		t.Errorf("Bloqueante = %d, esperado 10 (solo nuevo.go)", volumen.Bloqueante)
	}
	if volumen.Informativo != 1500 {
		t.Errorf("Informativo = %d, esperado 1500 (documento + generado)", volumen.Informativo)
	}
	if volumen.Estado != "PEQUENO" {
		t.Errorf("Estado = %q, esperado PEQUENO: 1500 líneas de documento no pueden frenar el guardián", volumen.Estado)
	}
}

// TestArchivoGiganteDeDocumentacionNoOfreceRefactor comprueba que un documento
// largo no entra por la rama de código masivo, que ofrece dividirlo con IA
// aplicando SRP. Proponer un refactor SRP sobre prosa no tiene sentido.
func TestArchivoGiganteDeDocumentacionNoOfreceRefactor(t *testing.T) {
	documento := ArchivoModificado{Ruta: "docs/arquitectura/objetivo.md", Lineas: 1792, Capa: "backend"}
	if esCodigoGigante(documento) {
		t.Error("un documento de 1792 líneas no debe tratarse como código masivo")
	}
	if !esAisladoEnSuLote(documento) {
		t.Error("un documento largo debe aislarse en su propio lote, como los archivos de config")
	}

	generado := ArchivoModificado{Ruta: "go.sum", Lineas: 900, Capa: "config"}
	if esCodigoGigante(generado) {
		t.Error("un archivo generado no debe tratarse como código masivo")
	}

	codigo := ArchivoModificado{Ruta: "internal/git/slice.go", Lineas: 501, Capa: "backend"}
	if !esCodigoGigante(codigo) {
		t.Error("el código sobre el límite debe seguir ofreciendo la refactorización")
	}
}

// escribirLineas crea un archivo con la cantidad de líneas indicada.
func escribirLineas(t *testing.T, ruta string, cantidad int) {
	t.Helper()
	var b strings.Builder
	for i := 0; i < cantidad; i++ {
		b.WriteString("linea\n")
	}
	if err := os.WriteFile(ruta, []byte(b.String()), 0644); err != nil {
		t.Fatalf("no se pudo crear %s: %v", ruta, err)
	}
}
