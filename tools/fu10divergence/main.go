// Command fu10divergence measures what feeding the review planner the same
// change evidence as `sentinel explain` would cost, so ticket 03 of the FU-10
// sequence decides from a number rather than a preference.
//
// Both arms share one change profile per commit. Only the detector input
// varies: the planner arm is today's production plan derivation, and the shared
// arm is the input `explain` assembles. That assembly is duplicated here on
// purpose; ticket 04 is what makes it shared.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/change"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/risk"
)

// patronesSensiblesExplain replicates the literal that comandos_explain.go
// passes today. Moving it out of the CLI command is ticket 04's job.
var patronesSensiblesExplain = []string{"**/auth/**", "**/*auth*.go", "**/security/**"}

type medida struct {
	SHA                string   `json:"sha"`
	Subject            string   `json:"subject"`
	Kind               string   `json:"kind"`
	SourceBearing      bool     `json:"source_bearing"`
	PlannerRisk        string   `json:"planner_risk"`
	SharedRisk         string   `json:"shared_risk"`
	PlannerInvocations int      `json:"planner_invocations"`
	SharedInvocations  int      `json:"shared_invocations"`
	PlannerDimensions  []string `json:"planner_dimensions"`
	SharedDimensions   []string `json:"shared_dimensions"`
	UnlockedFeatures   []string `json:"unlocked_characteristics,omitempty"`
	RiskChanged        bool     `json:"risk_changed"`
}

type estrato struct {
	Commits            int `json:"commits"`
	RiskChanged        int `json:"risk_changed"`
	PlannerInvocations int `json:"planner_invocations"`
	SharedInvocations  int `json:"shared_invocations"`
}

type informe struct {
	Ref                string             `json:"ref"`
	RefSHA             string             `json:"ref_sha"`
	Window             int                `json:"window"`
	Requested          int                `json:"requested"`
	Failed             int                `json:"failed"`
	MergesExcluded     int                `json:"merges_excluded"`
	CountingConvention string             `json:"counting_convention"`
	Strata             map[string]estrato `json:"strata"`
	SourceBearing      estrato            `json:"source_bearing_headline"`
	ProseOnly          estrato            `json:"prose_only"`
	Commits            []medida           `json:"commits"`
}

func main() {
	ventana := flag.Int("n", 120, "how many non-merge commits to walk back from the tip")
	ref := flag.String("ref", "HEAD", "tip to walk back from")
	flag.Parse()

	// Resolve the tip to a SHA before walking, and walk from that SHA: reading
	// the identity afterwards would let a ref that moves in between label the
	// report with a tip it never measured.
	tip, err := git("rev-parse", *ref)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	tip = strings.TrimSpace(tip)

	shas, merges, err := commitsSinMerge(tip, *ventana)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	out := informe{
		Ref:            *ref,
		RefSHA:         tip,
		Requested:      len(shas),
		MergesExcluded: merges,
		// Bundle scheduling dedupes by bundle name, not by dimension, so at
		// high risk `spec` is scheduled by both the correctness and the
		// contracts bundle and `logic` by both correctness and
		// concurrency-data. Each of those is a separate agent invocation.
		CountingConvention: "agent invocations: the sum of bundle dimensions, without deduplicating a dimension scheduled by two bundles",
		Strata:             map[string]estrato{},
	}

	for _, sha := range shas {
		m, err := medir(sha)
		if err != nil {
			// An evidence harness must not present a short measurement as a
			// complete one: record the failure, keep going so the operator sees
			// every broken commit at once, and refuse to exit successfully.
			fmt.Fprintf(os.Stderr, "%s: %v\n", sha[:8], err)
			out.Failed++
			continue
		}
		out.Commits = append(out.Commits, m)
		acumular(out.Strata, m.Kind, m)
		if m.SourceBearing {
			sumar(&out.SourceBearing, m)
		} else {
			sumar(&out.ProseOnly, m)
		}
	}

	out.Window = len(out.Commits)
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	resumir(os.Stderr, out)
	if out.Failed > 0 {
		fmt.Fprintf(os.Stderr, "\nINCOMPLETE: %d of %d commits could not be measured; the aggregates above cover %d\n",
			out.Failed, out.Requested, out.Window)
		os.Exit(1)
	}
}

func medir(sha string) (medida, error) {
	// One profile per commit, shared by both arms: deriving it separately on
	// each side would let a kind or symbol disagreement land inside the delta.
	perfil, err := change.PerfilDeCommit(sha)
	if err != nil {
		return medida{}, err
	}
	rutas, err := rutasDe(sha)
	if err != nil {
		return medida{}, err
	}
	lineas, err := lineasAnadidas(sha, rutas)
	if err != nil {
		return medida{}, err
	}
	gitattributes, err := leerGitattributes(sha)
	if err != nil {
		return medida{}, err
	}

	plan := review.PlanForProfile(perfil, rutas)

	compartidas := change.DetectarCaracteristicas(change.EntradaCaracteristicas{
		Symbols: perfil.Symbols, Rutas: rutas, LineasAnadidas: lineas,
		Gitattributes: gitattributes, PatronesSensibles: patronesSensiblesExplain,
	})
	riesgoCompartido := risk.Evaluar(perfil, compartidas)
	bundlesCompartidos := review.BundlesForRisk(riesgoCompartido, compartidas)

	asunto, err := git("show", "-s", "--format=%s", sha)
	if err != nil {
		return medida{}, fmt.Errorf("reading the subject of %s: %w", sha, err)
	}
	invPlanner, dimPlanner := contar(plan.Bundles)
	invCompartido, dimCompartido := contar(bundlesCompartidos)

	return medida{
		SHA: sha, Subject: strings.TrimSpace(asunto), Kind: perfil.Kind,
		SourceBearing:      contieneFuente(rutas),
		PlannerRisk:        string(plan.Risk.Nivel),
		SharedRisk:         string(riesgoCompartido.Nivel),
		PlannerInvocations: invPlanner, SharedInvocations: invCompartido,
		PlannerDimensions: dimPlanner, SharedDimensions: dimCompartido,
		UnlockedFeatures: desbloqueadas(plan.Characteristics, compartidas),
		RiskChanged:      plan.Risk.Nivel != riesgoCompartido.Nivel,
	}, nil
}

// contar devuelve las invocaciones de agente y la lista de dimensiones tal y
// como se programan, con repeticiones. Las repeticiones son el punto:
// AuditarCommit deduplica por nombre de bundle, no por dimensión, así que una
// dimensión programada por dos bundles se audita dos veces.
func contar(bundles []review.ReviewBundle) (invocaciones int, dimensiones []string) {
	dimensiones = []string{} // an empty plan must serialize as [], not null
	for _, bundle := range bundles {
		invocaciones += len(bundle.Dimensions)
		dimensiones = append(dimensiones, bundle.Dimensions...)
	}
	sort.Strings(dimensiones)
	return invocaciones, dimensiones
}

func desbloqueadas(planner, compartidas []change.Caracteristica) []string {
	estado := map[string]change.EstadoCaracteristica{}
	for _, c := range planner {
		estado[c.Nombre] = c.Estado
	}
	var nombres []string
	for _, c := range compartidas {
		if c.Estado == change.CaracteristicaPresente && estado[c.Nombre] != change.CaracteristicaPresente {
			nombres = append(nombres, c.Nombre)
		}
	}
	sort.Strings(nombres)
	return nombres
}

func contieneFuente(rutas []string) bool {
	reglas := change.ReglasPorDefecto()
	for _, ruta := range rutas {
		if change.ClasificarPorRuta(ruta, reglas) == change.ClaseSource {
			return true
		}
	}
	return false
}

func acumular(strata map[string]estrato, clave string, m medida) {
	e := strata[clave]
	sumar(&e, m)
	strata[clave] = e
}

func sumar(e *estrato, m medida) {
	e.Commits++
	if m.RiskChanged {
		e.RiskChanged++
	}
	e.PlannerInvocations += m.PlannerInvocations
	e.SharedInvocations += m.SharedInvocations
}

// commitsSinMerge excluye los merges: bajo --no-ff los commits revisados
// conservan su SHA, así que los no-merge son la población que review audita.
func commitsSinMerge(ref string, n int) ([]string, int, error) {
	sinMerge, err := git("rev-list", "--no-merges", fmt.Sprintf("-n%d", n), ref)
	if err != nil {
		return nil, 0, err
	}
	lista := strings.Fields(sinMerge)
	if len(lista) == 0 {
		return nil, 0, fmt.Errorf("no non-merge commits reachable from %s", ref)
	}
	// Merges inside the same window: reachable from the tip and not from the
	// oldest commit walked. Counting a fixed multiple of n instead would report
	// the padding, not the merges.
	cuenta, err := git("rev-list", "--count", "--min-parents=2", lista[len(lista)-1]+".."+ref)
	if err != nil {
		return nil, 0, fmt.Errorf("counting merges in the window: %w", err)
	}
	merges := 0
	if _, err := fmt.Sscanf(strings.TrimSpace(cuenta), "%d", &merges); err != nil {
		return nil, 0, fmt.Errorf("unreadable merge count %q: %w", strings.TrimSpace(cuenta), err)
	}
	return lista, merges, nil
}

// rutasDe reads the changed paths NUL-delimited: Git quotes and escapes paths
// holding spaces or non-ASCII bytes in its default line output, and a quoted
// path fed back to a pathspec silently matches nothing. --root makes a root
// commit report its files instead of none.
func rutasDe(sha string) ([]string, error) {
	salida, err := git("diff-tree", "--no-commit-id", "--name-only", "-r", "-z", "--root", sha)
	if err != nil {
		return nil, err
	}
	var rutas []string
	for _, ruta := range strings.Split(salida, "\x00") {
		if ruta != "" {
			rutas = append(rutas, ruta)
		}
	}
	return rutas, nil
}

// lineasAnadidas replica la lectura por ruta que hace explain hoy. El parser en
// memoria que la sustituye es trabajo del ticket 04.
//
// It reads through `git show` rather than a two-dot diff so a root commit,
// which has no `sha^`, reports its files as wholly added instead of failing. A
// path that cannot be read is an error: silently contributing no lines would
// understate the characteristics and bias the measurement toward the planner.
func lineasAnadidas(sha string, rutas []string) (map[string][]string, error) {
	resultado := make(map[string][]string)
	for _, ruta := range rutas {
		salida, err := git("show", "--no-color", "--unified=0", "--format=", sha, "--", ":(literal)"+ruta)
		if err != nil {
			return nil, fmt.Errorf("reading added lines of %s in %s: %w", ruta, sha, err)
		}
		enHunk := false
		for _, linea := range strings.Split(salida, "\n") {
			if strings.HasPrefix(linea, "@@") {
				enHunk = true
				continue
			}
			if enHunk && strings.HasPrefix(linea, "+") {
				resultado[ruta] = append(resultado[ruta], strings.TrimPrefix(linea, "+"))
			}
		}
	}
	return resultado, nil
}

// leerGitattributes distingue ausencia de fallo. Ni `git show
// <sha>:.gitattributes` ni `git cat-file -e` sirven para eso: ambos salen con
// código no cero tanto si la ruta no está como si el repositorio o el objeto no
// se pueden leer, así que cualquiera de los dos convertiría un fallo real en
// "no hay atributos" y degradaría la evidencia en silencio.
//
// `git ls-tree` sí los separa: una ruta ausente es salida vacía con código
// cero, y solo un fallo real sale con código no cero.
func leerGitattributes(sha string) (string, error) {
	listado, err := git("ls-tree", "--name-only", sha, "--", ".gitattributes")
	if err != nil {
		return "", fmt.Errorf("looking for .gitattributes at %s: %w", sha, err)
	}
	if strings.TrimSpace(listado) == "" {
		return "", nil // absent in this tree, which is the common case
	}
	contenido, err := git("show", sha+":.gitattributes")
	if err != nil {
		return "", fmt.Errorf("reading .gitattributes at %s: %w", sha, err)
	}
	return contenido, nil
}

func git(args ...string) (string, error) {
	salida, err := exec.Command("git", args...).Output()
	return string(salida), err
}

func resumir(w *os.File, out informe) {
	fmt.Fprintf(w, "\n%s (%s)\n", out.Ref, out.RefSHA)
	fmt.Fprintf(w, "window=%d of %d non-merge commits measured, %d failed (%d merges excluded)\n",
		out.Window, out.Requested, out.Failed, out.MergesExcluded)
	fmt.Fprintf(w, "counting: %s\n\n", out.CountingConvention)
	fmt.Fprintf(w, "%-16s %8s %14s %12s %12s\n", "stratum", "commits", "risk changed", "inv today", "inv shared")
	claves := make([]string, 0, len(out.Strata))
	for k := range out.Strata {
		claves = append(claves, k)
	}
	sort.Strings(claves)
	for _, k := range claves {
		e := out.Strata[k]
		fmt.Fprintf(w, "%-16s %8d %14d %12d %12d\n", k, e.Commits, e.RiskChanged, e.PlannerInvocations, e.SharedInvocations)
	}
	e := out.SourceBearing
	fmt.Fprintf(w, "\nHEADLINE source-bearing: %d commits, %d change risk, %d -> %d agent invocations\n",
		e.Commits, e.RiskChanged, e.PlannerInvocations, e.SharedInvocations)
	p := out.ProseOnly
	fmt.Fprintf(w, "prose-only:              %d commits, %d change risk, %d -> %d agent invocations\n",
		p.Commits, p.RiskChanged, p.PlannerInvocations, p.SharedInvocations)
}
