package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/change"
	"github.com/ISeoane-Quental/vas.sentinel/internal/risk"
	"github.com/ISeoane-Quental/vas.sentinel/internal/secret"
)

type caracteristicaExplicada struct {
	Name      string                      `json:"name"`
	State     change.EstadoCaracteristica `json:"state"`
	Heuristic bool                        `json:"heuristic,omitempty"`
	Detector  string                      `json:"detector"`
}

type salidaExplain struct {
	Profile         change.ChangeProfile      `json:"profile"`
	Characteristics []caracteristicaExplicada `json:"characteristics"`
	// FU-11: deterministic exposed-credential incidents, independent of the
	// change profile. Omitted when empty: absence is data, never verdict.
	ExposedCredentials []credencialExpuesta `json:"exposed_credentials,omitempty"`
	// Paths the credential scanner could not read. Omitted when empty.
	CredentialScanUnknown []string `json:"credential_scan_unknown,omitempty"`
	Risk                  struct {
		Level   risk.Nivel `json:"level"`
		Explain string     `json:"explain"`
	} `json:"risk"`
	Cohesion struct {
		Clusters       int     `json:"clusters"`
		Score          float64 `json:"score"`
		SuggestedSplit bool    `json:"suggested_split"`
	} `json:"cohesion"`
}

// credencialExpuesta names the path, shape and added-line location of one
// credential match. It never carries the matched value.
type credencialExpuesta struct {
	Path  string `json:"path"`
	Shape string `json:"shape"`
	Line  int    `json:"line"`
}

var detectoresExplain = map[string]string{
	"public_api":         "firma o declaración exportada cambiada en AST",
	"database":           "migración, SQL o ruta declarada de datos",
	"security_sensitive": "ruta sensible o identificador auth/token/crypto/password/secret",
	"concurrency":        "aparición de go, sync, chan o context en líneas añadidas",
	"behavior_change":    "código no test y no comentario añadido",
	"test_covered":       "tests del paquete confirmados por diff o mapa de tests",
	"cross_module":       "dos o más módulos de primer nivel tocados",
	"generated_code":     "clase generada, .gitattributes o marcador de generación",
	"ci_cd":              "ruta clasificada como CI/CD",
	"infrastructure":     "ruta clasificada como infraestructura",
}

func ejecutarExplain(salida io.Writer, args []string) error {
	return ejecutarExplainCon(salida, args, change.PerfilDeCambio, ejecutarGitParaChange)
}

func ejecutarExplainCon(salida io.Writer, args []string, perfil func(string, string) (change.ChangeProfile, error), lector change.LectorGit) error {
	base, head, jsonOut, err := parsearExplain(args)
	if err != nil {
		return err
	}
	perfilCambio, err := perfil(base, head)
	if err != nil {
		return fmt.Errorf("no se pudo calcular el perfil: %w", err)
	}
	rango := base + ".." + head
	rutas, err := rutasExplain(lector, rango)
	if err != nil {
		return err
	}
	diff, err := lector(argumentosDiffExplain(rango)...)
	if err != nil {
		return fmt.Errorf("could not read unified diff for %s: %w", rango, err)
	}
	gitattributes, _ := lector("show", head+":.gitattributes")
	entrada := change.NewCharacteristicsInput(perfilCambio, rutas, diff, gitattributes)
	caracteristicas := change.DetectarCaracteristicas(entrada)
	resultadoRiesgo := risk.Evaluar(perfilCambio, caracteristicas)
	cohesion, err := change.Cohesion(rutas, lector)
	if err != nil {
		return err
	}

	resultado := salidaExplain{Profile: perfilCambio}
	// FU-11: credential scan over the same diff, independent of the change
	// profile and of every class filter. No agent involved.
	incidentesSecreto, desconocidasSecreto := secret.Scan(rutas, diff)
	for _, incidente := range incidentesSecreto {
		resultado.ExposedCredentials = append(resultado.ExposedCredentials, credencialExpuesta{
			Path: incidente.Path, Shape: incidente.Shape, Line: incidente.Line,
		})
	}
	resultado.CredentialScanUnknown = desconocidasSecreto

	for _, caracteristica := range caracteristicas {
		resultado.Characteristics = append(resultado.Characteristics, caracteristicaExplicada{
			Name: caracteristica.Nombre, State: caracteristica.Estado,
			Heuristic: caracteristica.Heuristica, Detector: detectoresExplain[caracteristica.Nombre],
		})
	}
	resultado.Risk.Level, resultado.Risk.Explain = resultadoRiesgo.Nivel, resultadoRiesgo.Explicacion
	resultado.Cohesion.Clusters, resultado.Cohesion.Score = cohesion.Clusters, cohesion.Puntuacion
	resultado.Cohesion.SuggestedSplit = cohesion.SugerenciaSplit

	if jsonOut {
		encoder := json.NewEncoder(salida)
		encoder.SetIndent("", "  ")
		return encoder.Encode(resultado)
	}
	fmt.Fprintf(salida, "Perfil: kind=%s, files=%d, +%d/-%d, modules=%s\n", perfilCambio.Kind, perfilCambio.Size.Files, perfilCambio.Size.Added, perfilCambio.Size.Deleted, strings.Join(perfilCambio.Modules, ", "))
	fmt.Fprintln(salida, "Características:")
	for _, caracteristica := range resultado.Characteristics {
		fmt.Fprintf(salida, "- %s=%s — %s\n", caracteristica.Name, caracteristica.State, caracteristica.Detector)
	}
	fmt.Fprintf(salida, "Riesgo: %s — %s\n", resultado.Risk.Level, resultado.Risk.Explain)
	fmt.Fprintf(salida, "Cohesión: clusters=%d score=%.2f suggested_split=%t\n", resultado.Cohesion.Clusters, resultado.Cohesion.Score, resultado.Cohesion.SuggestedSplit)
	for _, incidente := range resultado.ExposedCredentials {
		fmt.Fprintf(salida, "  ⚠️ exposed credential: %s:%d %s (value withheld)\n", incidente.Path, incidente.Line, incidente.Shape)
	}
	if len(resultado.CredentialScanUnknown) > 0 {
		fmt.Fprintf(salida, "  ⚠️ credential scan unavailable for: %s\n", strings.Join(resultado.CredentialScanUnknown, ", "))
	}
	return nil
}

func parsearExplain(args []string) (base, head string, jsonOut bool, err error) {
	rango := "HEAD^..HEAD"
	for _, arg := range args {
		if arg == "--json" {
			jsonOut = true
			continue
		}
		if strings.HasPrefix(arg, "-") || rango != "HEAD^..HEAD" {
			return "", "", false, fmt.Errorf("%s", usoExplain)
		}
		rango = arg
	}
	base, head, ok := strings.Cut(rango, "..")
	if !ok || base == "" || head == "" || strings.Contains(head, "..") ||
		strings.HasPrefix(base, "-") || strings.HasPrefix(head, "-") {
		return "", "", false, fmt.Errorf("rango inválido %q: usa <base>..<head>", rango)
	}
	return base, head, jsonOut, nil
}

// argumentosDiffExplain pins the diff invocation the added-line parser depends
// on. The prefixes are forced rather than left to configuration: diff.noprefix,
// diff.mnemonicPrefix and diff.srcPrefix/dstPrefix each change the header
// format, and a header the parser does not recognise loses its added lines with
// no error. Exposed as one function so the test that exercises real Git cannot
// drift from the invocation it claims to cover.
func argumentosDiffExplain(rango string) []string {
	return []string{"diff", "--no-color", "--unified=0", "--src-prefix=a/", "--dst-prefix=b/", "-M", rango}
}

func rutasExplain(lector change.LectorGit, rango string) ([]string, error) {
	salida, err := lector("diff", "--name-only", "-z", "-M", rango)
	if err != nil {
		return nil, fmt.Errorf("no se pudieron listar las rutas de %s: %w", rango, err)
	}
	var rutas []string
	for _, ruta := range strings.Split(strings.TrimSuffix(salida, "\x00"), "\x00") {
		if ruta != "" {
			rutas = append(rutas, filepath.ToSlash(ruta))
		}
	}
	return rutas, nil
}

func ejecutarGitParaChange(args ...string) (string, error) {
	salida, err := exec.Command("git", args...).Output()
	return string(salida), err
}
