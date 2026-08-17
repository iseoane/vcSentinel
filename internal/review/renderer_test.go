package review

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// fichaAyuda construye una ficha realista para los tests del renderer.
func fichaAyuda(sha, mensaje, modelo string, revs ...Revision) Ficha {
	return Ficha{SHA: sha, Message: mensaje, Model: modelo, Revisions: revs}
}

// revisionAyuda construye una revisión con el veredicto y hallazgos dados.
func revisionAyuda(resultado string, dims ...DimensionResult) Revision {
	return Revision{At: time.Now().UTC(), Result: resultado, Dims: dims}
}

func TestRenderMatrizBasica(t *testing.T) {
	fichas := []Ficha{
		fichaAyuda("6b127cd", "docs(review): concepto fase 2", "opencode.cheap",
			revisionAyuda("ok",
				DimensionResult{Dim: DimSpec, Verdict: VerdictOK},
				DimensionResult{Dim: DimSecurity, Verdict: VerdictBlock},
				DimensionResult{Dim: DimLogic, Verdict: VerdictOK},
			)),
		fichaAyuda("945b5b5", "feat(config): comandos de verificacion", "deepseek-v4-flash-free",
			revisionAyuda("warn",
				DimensionResult{Dim: DimSpec, Verdict: VerdictOK},
				DimensionResult{Dim: DimTests, Verdict: VerdictWarn},
			)),
	}

	salida := RenderMatriz(fichas)

	// Aserción exacta: cabecera completa canónica, filas en orden y celdas
	// con el veredicto de la última revisión (— para dimensiones ausentes).
	esperado := "| Commit | logic | style | design | tests | security | spec |\n" +
		"|---|---|---|---|---|---|---|\n" +
		"| `6b127cd` docs(review): concepto fase 2 | ✅ | — | — | — | 🚨 | ✅ |\n" +
		"| `945b5b5` feat(config): comandos de verificacion | — | — | — | ⚠️ | — | ✅ |\n"
	if salida != esperado {
		t.Errorf("matriz no coincide:\ngot:\n%s\nwant:\n%s", salida, esperado)
	}
}

func TestRenderMatrizVacia(t *testing.T) {
	if salida := RenderMatriz(nil); !strings.Contains(salida, "No hay commits auditados") {
		t.Errorf("matriz vacía = %q, esperado aviso de sin commits", salida)
	}
}

func TestRenderMatrizRevisionPasada(t *testing.T) {
	ficha := fichaAyuda("945b5b5", "feat(config): comandos", "opencode.cheap",
		revisionAyuda("block",
			DimensionResult{Dim: DimSpec, Verdict: VerdictBlock,
				Findings: []ReviewFinding{{Dimension: DimSpec, File: "a.go", Line: 1, Severity: SevCritical}}},
		),
		revisionAyuda("ok",
			DimensionResult{Dim: DimSpec, Verdict: VerdictOK},
		),
	)

	salida := RenderMatriz([]Ficha{ficha})

	// La última revisión manda y la pasada tras un block se marca.
	if !strings.Contains(salida, "✅ (2ª rev — CRITICAL superado)") {
		t.Errorf("falta la marca de revisión pasada:\n%s", salida)
	}
	if strings.Contains(salida, "🚨") {
		t.Errorf("la matriz no debe mostrar el block de la primera revisión:\n%s", salida)
	}
}

func TestRenderResumenRiesgos(t *testing.T) {
	fichas := []Ficha{
		fichaAyuda("6b127cd", "docs(review): concepto fase 2", "opencode.cheap",
			revisionAyuda("block",
				DimensionResult{Dim: DimSecurity, Verdict: VerdictBlock,
					Findings: []ReviewFinding{
						{Dimension: DimSecurity, File: "a.go", Line: 42, Severity: SevCritical, Description: "dato expuesto"},
						{Dimension: DimSecurity, File: "a.go", Line: 10, Severity: SevAdvisory, Description: "sugerencia menor"},
					}},
			)),
		fichaAyuda("945b5b5", "feat(config): comandos", "deepseek-v4-flash-free",
			revisionAyuda("warn",
				DimensionResult{Dim: DimTests, Verdict: VerdictWarn,
					Findings: []ReviewFinding{
						{Dimension: DimTests, File: "z.go", Line: 10, Severity: SevWarning, Description: "test frágil"},
					}},
			)),
	}

	salida := RenderResumen(fichas)

	// Conteo global.
	if !strings.Contains(salida, "🟢 ok: 0 · 🟡 warn: 1 · 🚨 block: 1") {
		t.Errorf("conteo global incorrecto:\n%s", salida)
	}
	// Riesgos: CRITICAL y WARNING sí; ADVISORY no. Desde T6.5 riesgos() lee
	// HallazgosEfectivos() y renderiza siempre con renderMergedFinding, por
	// eso el formato incluye el label de Source (unknown: fixture v1 sin
	// Source) y la confidence, aunque venga de Dims convertido.
	if !strings.Contains(salida, "🚨 `6b127cd` [security] CRITICAL (unknown, confidence 0.00) — dato expuesto (a.go:42)") {
		t.Errorf("falta el riesgo CRITICAL:\n%s", salida)
	}
	if !strings.Contains(salida, "⚠️ `945b5b5` [tests] WARNING (unknown, confidence 0.00) — test frágil (z.go:10)") {
		t.Errorf("falta el riesgo WARNING:\n%s", salida)
	}
	if strings.Contains(salida, "sugerencia menor") {
		t.Errorf("los ADVISORY no son riesgos y no deben aparecer en el resumen:\n%s", salida)
	}
}

func TestRenderResumenCorregida(t *testing.T) {
	ficha := fichaAyuda("6b127cd", "docs(review): concepto", "opencode.cheap",
		revisionAyuda("block", DimensionResult{Dim: DimSpec, Verdict: VerdictBlock}),
	)
	ficha.FixedIn = "a1b2c3d"

	salida := RenderResumen([]Ficha{ficha})
	if !strings.Contains(salida, "🔧 corregida en `a1b2c3d`") {
		t.Errorf("falta la marca de corrección:\n%s", salida)
	}
}

func TestTruncarCuerpo(t *testing.T) {
	corto := "texto breve"
	if got := TruncarCuerpo(corto, 1024); got != corto {
		t.Errorf("TruncarCuerpo(corto) = %q, esperado sin cambios", got)
	}

	largo := strings.Repeat("x", 100)
	got := TruncarCuerpo(largo, 50)
	if len(got) > 50 {
		t.Errorf("TruncarCuerpo = %d bytes, esperado ≤50", len(got))
	}
	if !strings.Contains(got, "truncado") || !strings.Contains(got, "omitieron") {
		t.Errorf("el truncamiento debe estar marcado explícitamente, got: %q", got)
	}
}

// TestTruncarCuerpoLimiteNulo: sin límite (0 o negativo) el texto no cambia.
func TestTruncarCuerpoLimiteNulo(t *testing.T) {
	texto := "abc"
	if got := TruncarCuerpo(texto, 0); got != texto {
		t.Errorf("TruncarCuerpo(0) = %q, esperado sin cambios", got)
	}
	if got := TruncarCuerpo(texto, -5); got != texto {
		t.Errorf("TruncarCuerpo(-5) = %q, esperado sin cambios", got)
	}
}

// TestTruncarCuerpoMarcadorMayorQueLimite: cuando ni el marcador cabe, el
// resultado es solo el marcador recortado al límite, sin contenido del texto.
func TestTruncarCuerpoMarcadorMayorQueLimite(t *testing.T) {
	got := TruncarCuerpo(strings.Repeat("x", 500), 10)
	if len(got) > 10 {
		t.Errorf("TruncarCuerpo = %d bytes, esperado ≤10", len(got))
	}
	if strings.Contains(got, "x") {
		t.Errorf("el resultado no debe contener contenido del texto, got: %q", got)
	}
}

// TestTruncarCuerpoNoParteRunas: el corte nunca parte una runa UTF-8 y el
// resultado siempre es texto válido.
func TestTruncarCuerpoNoParteRunas(t *testing.T) {
	texto := "áéíóúüñ " + strings.Repeat("ñ", 200)
	got := TruncarCuerpo(texto, 57)
	if len(got) > 57 {
		t.Errorf("TruncarCuerpo = %d bytes, esperado ≤57", len(got))
	}
	if !utf8.ValidString(got) {
		t.Errorf("TruncarCuerpo partió una runa UTF-8: %q", got)
	}
}

// TestRenderResumenVacio: sin fichas, el resumen avisa en lugar de inventar.
func TestRenderResumenVacio(t *testing.T) {
	if salida := RenderResumen(nil); !strings.Contains(salida, "No hay commits auditados") {
		t.Errorf("resumen vacío = %q, esperado aviso de sin commits", salida)
	}
}

// TestConteoQuestionUnavailable: el conteo global incluye question y
// unavailable solo cuando existen.
func TestConteoQuestionUnavailable(t *testing.T) {
	fichas := []Ficha{
		fichaAyuda("aaaaaaa", "feat(a): a", "m",
			revisionAyuda(VerdictQuestion, DimensionResult{Dim: DimSpec, Verdict: VerdictQuestion})),
		fichaAyuda("bbbbbbb", "feat(b): b", "m",
			revisionAyuda(VerdictUnavailable, DimensionResult{Dim: DimSpec, Verdict: VerdictUnavailable})),
	}

	salida := RenderResumen(fichas)
	if !strings.Contains(salida, "❓ question: 1") || !strings.Contains(salida, "⛔ unavailable: 1") {
		t.Errorf("faltan los conteos de question/unavailable:\n%s", salida)
	}
	if strings.Contains(salida, "🔧") {
		t.Errorf("no debe haber correcciones en este fixture:\n%s", salida)
	}
}

func TestLineaRiesgo(t *testing.T) {
	fichas := []Ficha{
		fichaAyuda("6b127cd", "feat(a)", "m",
			revisionAyuda("warn", DimensionResult{Dim: DimLogic, Verdict: VerdictWarn})),
		fichaAyuda("945b5b5", "feat(b)", "m",
			revisionAyuda("ok", DimensionResult{Dim: DimLogic, Verdict: VerdictOK})),
	}
	linea := lineaRiesgo(fichas)
	if !strings.Contains(linea, "warn") {
		t.Errorf("línea de riesgo no muestra el veredicto de rama: %s", linea)
	}
	if !strings.Contains(linea, "⚠️") {
		t.Errorf("la línea de riesgo debe llevar emoji del peor veredicto: %s", linea)
	}
	if !strings.Contains(linea, "ok: 1") || !strings.Contains(linea, "warn: 1") {
		t.Errorf("la línea de riesgo debe contar los resultados: %s", linea)
	}
}

func TestVeredictoDeRamaPonderado(t *testing.T) {
	casos := []struct {
		nombre   string
		fichas   []Ficha
		esperado string
	}{
		{
			"block manda sobre warn",
			[]Ficha{
				fichaAyuda("u1", "a", "m", revisionAyuda("warn")),
				fichaAyuda("u2", "b", "m", revisionAyuda("block")),
			},
			VerdictBlock,
		},
		{
			"warn sobre ok",
			[]Ficha{
				fichaAyuda("u1", "a", "m", revisionAyuda("ok")),
				fichaAyuda("u2", "b", "m", revisionAyuda("warn")),
			},
			VerdictWarn,
		},
		{
			"sin fichas = ok",
			nil,
			VerdictOK,
		},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := VeredictoDeRama(c.fichas); got != c.esperado {
				t.Errorf("VeredictoDeRama = %q, esperado %q", got, c.esperado)
			}
		})
	}
}

// TestVeredictoDeRamaCorregidaNoBloquea: un block corregido en un commit
// posterior (FixedIn) ya no aporta al veredicto de la rama.
func TestVeredictoDeRamaCorregidaNoBloquea(t *testing.T) {
	bloqueada := fichaAyuda("u1", "feat(a)", "m", revisionAyuda("block"))
	bloqueada.FixedIn = "a1b2c3d"
	fichas := []Ficha{bloqueada, fichaAyuda("u2", "feat(b)", "m", revisionAyuda("ok"))}
	if got := VeredictoDeRama(fichas); got != VerdictOK {
		t.Errorf("VeredictoDeRama = %q, esperado ok (block corregido)", got)
	}
}

func TestSeccionVerificacion(t *testing.T) {
	t.Run("determinista con exit codes", func(t *testing.T) {
		salida := seccionVerificacion(VerificacionPlantilla{
			Modo: "determinista",
			Comandos: []ComandoVerificado{
				{Comando: "go vet ./...", Exit: 0},
				{Comando: "go test ./...", Exit: 1},
			},
		})
		if !strings.Contains(salida, "go vet ./...") || !strings.Contains(salida, "exit 0") {
			t.Errorf("faltan comandos ok: %s", salida)
		}
		if !strings.Contains(salida, "❌") || !strings.Contains(salida, "exit 1") {
			t.Errorf("el exit no nulo debe marcarse: %s", salida)
		}
	})
	t.Run("delegado con tested", func(t *testing.T) {
		salida := seccionVerificacion(VerificacionPlantilla{
			Modo:   "delegado",
			Tested: []string{"make test"},
		})
		if !strings.Contains(salida, "make test") {
			t.Errorf("falta el contrato tested: %s", salida)
		}
	})
	t.Run("omitido siempre honesto", func(t *testing.T) {
		salida := seccionVerificacion(VerificacionPlantilla{Modo: "omitido", Motivo: "no_configurado"})
		if !strings.Contains(salida, "no_configurado") {
			t.Errorf("el motivo debe mostrarse: %s", salida)
		}
		if strings.Contains(salida, "✅ ") {
			t.Errorf("un modo omitido nunca debe inyectar PASS: %s", salida)
		}
	})
	t.Run("sin evidencia no miente", func(t *testing.T) {
		salida := seccionVerificacion(VerificacionPlantilla{})
		if !strings.Contains(salida, "no ejecutados") && !strings.Contains(salida, "Tests no ejecutados") {
			t.Errorf("sin evidencia debe declarar tests no ejecutados: %s", salida)
		}
	})
}

// TestSeccionValidacion: exit codes reales de internal/validation (T1.8),
// distintos de seccionVerificacion — sin comandos, nunca inventa un PASS.
func TestSeccionValidacion(t *testing.T) {
	t.Run("con exit codes reales", func(t *testing.T) {
		salida := seccionValidacion([]ComandoVerificado{
			{Comando: "go vet ./...", Exit: 0},
			{Comando: "go test ./...", Exit: 1},
		})
		if !strings.Contains(salida, "go vet ./...") || !strings.Contains(salida, "exit 0") {
			t.Errorf("faltan los comandos en verde: %s", salida)
		}
		if !strings.Contains(salida, "❌") || !strings.Contains(salida, "exit 1") {
			t.Errorf("el exit no nulo debe marcarse: %s", salida)
		}
	})
	t.Run("sin comandos no inventa nada", func(t *testing.T) {
		salida := seccionValidacion(nil)
		if strings.Contains(salida, "✅") {
			t.Errorf("sin comandos no debe inventar un PASS: %s", salida)
		}
	})
}

// TestRenderPlantillaPrDistingueValidacionYVerificacion: la plantilla separa
// la validación previa (ValidationRun) de la verificación post-hoc
// (ops.Verificar) en dos secciones propias, sin mezclarlas (T1.8).
func TestRenderPlantillaPrDistingueValidacionYVerificacion(t *testing.T) {
	fichas := []Ficha{fichaAyuda("u1", "feat(a)", "m", revisionAyuda("ok"))}
	verificacion := VerificacionPlantilla{
		Modo:       "determinista",
		Comandos:   []ComandoVerificado{{Comando: "go test ./... (post-hoc)", Exit: 0}},
		Validacion: []ComandoVerificado{{Comando: "go build ./... (validacion previa)", Exit: 1}},
	}
	salida := RenderPlantillaPr(fichas, nil, verificacion, "0.2.0")
	if !strings.Contains(salida, "## Validación") {
		t.Fatalf("falta la sección de validación previa: %s", salida)
	}
	if !strings.Contains(salida, "go build ./... (validacion previa)") {
		t.Errorf("la validación previa debe listar su propio comando: %s", salida)
	}
	if !strings.Contains(salida, "go test ./... (post-hoc)") {
		t.Errorf("la verificación post-hoc debe seguir apareciendo: %s", salida)
	}
}

func TestRenderPlantillaPr(t *testing.T) {
	fichas := []Ficha{
		fichaAyuda("u1a", "feat(a)", "m",
			revisionAyuda("ok",
				DimensionResult{Dim: DimLogic, Verdict: VerdictOK},
				DimensionResult{Dim: DimTests, Verdict: VerdictOK})),
	}
	overview := &ResultadoOverview{
		Coherente: true,
		Rationale: "Cambio coherente\nque completa la fase\nen tres líneas\npara el rationale.",
	}
	verificacion := VerificacionPlantilla{
		Modo: "determinista",
		Comandos: []ComandoVerificado{
			{Comando: "go vet ./...", Exit: 0},
		},
	}

	salida := RenderPlantillaPr(fichas, overview, verificacion, "0.2.0")
	if !strings.Contains(salida, "Veredicto de auditoría") {
		t.Errorf("falta la línea de riesgo: %s", salida)
	}
	if !strings.Contains(salida, "Cambio coherente") {
		t.Errorf("falta el rationale del overview: %s", salida)
	}
	if !strings.Contains(salida, "Matriz de auditoría") {
		t.Errorf("falta la matriz: %s", salida)
	}
	if !strings.Contains(salida, "go vet ./...") {
		t.Errorf("falta la sección de verificación: %s", salida)
	}
	if !strings.Contains(salida, "Generated by VAS Sentinel 0.2.0") {
		t.Errorf("falta la firma con versión: %s", salida)
	}
	if !strings.Contains(salida, "no CI") {
		t.Errorf("la firma debe aclarar que no es CI: %s", salida)
	}
	if len(salida) > LimiteCuerpoPR {
		t.Errorf("plantilla supera el límite: %d", len(salida))
	}
}

func TestRenderPlantillaSinOverviewHonesta(t *testing.T) {
	fichas := []Ficha{
		fichaAyuda("u1", "feat(a)", "m", revisionAyuda("ok")),
	}
	salida := RenderPlantillaPr(fichas, nil, VerificacionPlantilla{Modo: "omitido"}, "0.2.0")
	if !strings.Contains(salida, "Sin overview") {
		t.Errorf("sin overview debe decirse, no omitirse en silencio: %s", salida)
	}
}

// TestBloqueantesDeRamaFiltraCriticos: solo los CRITICAL de la última
// revisión bloquean; vacíos, sin revisión y severidades menores no cuentan.
func TestBloqueantesDeRamaFiltraCriticos(t *testing.T) {
	critico := ReviewFinding{Dimension: DimSecurity, File: "a.go", Line: 42,
		Severity: SevCritical, Description: "dato expuesto"}
	casos := []struct {
		nombre string
		fichas []Ficha
		want   int
	}{
		{
			nombre: "sin fichas no bloquea nada",
			fichas: nil,
			want:   0,
		},
		{
			nombre: "ficha sin revisiones no bloquea",
			fichas: []Ficha{fichaAyuda("u1", "feat(a)", "m")},
			want:   0,
		},
		{
			nombre: "solo severidades menores no bloquea",
			fichas: []Ficha{fichaAyuda("u1", "feat(a)", "m",
				revisionAyuda("warn",
					DimensionResult{Dim: DimTests, Verdict: VerdictWarn,
						Findings: []ReviewFinding{{Dimension: DimTests, Severity: SevWarning, Description: "frágil"}}},
					DimensionResult{Dim: DimSpec, Verdict: VerdictOK,
						Findings: []ReviewFinding{{Dimension: DimSpec, Severity: SevAdvisory, Description: "scope"}}},
				))},
			want: 0,
		},
		{
			nombre: "mezcla con al menos un CRITICAL lista solo los criticos",
			fichas: []Ficha{
				fichaAyuda("u1", "feat(a)", "m",
					revisionAyuda("block",
						DimensionResult{Dim: DimSecurity, Verdict: VerdictBlock,
							Findings: []ReviewFinding{
								critico,
								{Dimension: DimSecurity, Severity: SevWarning, Description: "menor"},
							}},
					)),
				fichaAyuda("u2", "feat(b)", "m", revisionAyuda("ok")),
			},
			want: 1,
		},
		{
			nombre: "solo la ultima revision manda",
			fichas: []Ficha{fichaAyuda("u1", "feat(a)", "m",
				revisionAyuda("block",
					DimensionResult{Dim: DimSecurity, Verdict: VerdictBlock,
						Findings: []ReviewFinding{critico}}),
				revisionAyuda("ok",
					DimensionResult{Dim: DimSecurity, Verdict: VerdictOK}),
			)},
			want: 0,
		},
		{
			nombre: "block corregido no bloquea",
			fichas: func() []Ficha {
				corregida := fichaAyuda("f1", "feat(a)", "m",
					revisionAyuda("block",
						DimensionResult{Dim: DimSecurity, Verdict: VerdictBlock,
							Findings: []ReviewFinding{critico}}))
				corregida.FixedIn = "a1b2c3d"
				return []Ficha{corregida}
			}(),
			want: 0,
		},
	}
	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			got := BloqueantesDeRama(caso.fichas)
			if len(got) != caso.want {
				t.Errorf("BloqueantesDeRama() = %d hallazgos, esperado %d: %+v",
					len(got), caso.want, got)
			}
		})
	}
}

// TestRiesgosRendersMergedFindingWithSourceAndEvidence: a merged Hallazgo
// (T6.1 aggregation + T6.2 supersede result, ResultadoAuditoria.Findings
// persisted on Revision.AggregatedFindings by T6.5) renders its distinguished
// Source (review vs validation) and every accumulated evidence from
// EvidenceSet instead of only the single legacy Evidence string.
func TestRiesgosRendersMergedFindingWithSourceAndEvidence(t *testing.T) {
	ficha := fichaAyuda("6b127cd", "fix(auth): tighten token check", "m", Revision{
		At:     time.Now().UTC(),
		Result: "block",
		AggregatedFindings: []Hallazgo{
			{
				Dimension:   DimSecurity,
				Severity:    SevCritical,
				Source:      SourceReview,
				Confidence:  0.9,
				Description: "token comparison is not constant-time",
				Location:    Ubicacion{Archivo: "auth.go", LineaInicio: 42},
				EvidenceSet: &FindingEvidenceSet{Values: []FindingEvidence{
					{Dimension: DimSecurity, Evidence: "token == expected", Confidence: 0.8},
					{Dimension: DimLogic, Evidence: "no hmac.Equal usage found", Confidence: 0.95},
				}},
			},
		},
	})

	salida := RenderPlantillaPr([]Ficha{ficha}, nil, VerificacionPlantilla{Modo: "omitido"}, "0.2.0")

	if !strings.Contains(salida, "review") {
		t.Errorf("merged finding must show its distinguished Source (review): %s", salida)
	}
	if !strings.Contains(salida, "token == expected") || !strings.Contains(salida, "no hmac.Equal usage found") {
		t.Errorf("merged finding must show every accumulated evidence from EvidenceSet: %s", salida)
	}
	if !strings.Contains(salida, "0.90") {
		t.Errorf("merged finding must show the combined confidence: %s", salida)
	}
}

// TestRiesgosMergedValidationSourceLabel: a merged Hallazgo sourced from
// deterministic validation (SourceValidation) is labeled distinctly from one
// sourced from semantic review (SourceReview).
func TestRiesgosMergedValidationSourceLabel(t *testing.T) {
	ficha := fichaAyuda("945b5b5", "fix(build): repair lint failure", "m", Revision{
		At:     time.Now().UTC(),
		Result: "block",
		AggregatedFindings: []Hallazgo{
			{
				Dimension:   DimStyle,
				Severity:    SevWarning,
				Source:      SourceValidation,
				Confidence:  1,
				Description: "gofmt reported an unformatted file",
				Location:    Ubicacion{Archivo: "main.go", LineaInicio: 1},
				Evidence:    "gofmt -l main.go",
			},
		},
	})

	salida := riesgos([]Ficha{ficha})
	if len(salida) != 1 {
		t.Fatalf("riesgos() = %d lines, expected 1: %v", len(salida), salida)
	}
	if !strings.Contains(salida[0], "validation") {
		t.Errorf("merged finding sourced from validation must be labeled distinctly: %s", salida[0])
	}
	if strings.Contains(salida[0], "review") {
		t.Errorf("a validation-sourced finding must not be mislabeled review: %s", salida[0])
	}
	if !strings.Contains(salida[0], "gofmt -l main.go") {
		t.Errorf("a non-merged Hallazgo (EvidenceSet nil) must fall back to its legacy Evidence string: %s", salida[0])
	}
}

// TestRiesgosFallsBackToLegacyDimsWithoutAggregatedFindings: a Revision saved
// before T6.5 (or by a caller that never propagated AggregatedFindings)
// still surfaces its Dims-based findings — HallazgosEfectivos (T6.5 review
// finding: design) converts them to Hallazgo so riesgos() keeps working
// unchanged from the caller's point of view.
func TestRiesgosFallsBackToLegacyDimsWithoutAggregatedFindings(t *testing.T) {
	ficha := fichaAyuda("aaaaaaa", "feat(a): legacy", "m",
		revisionAyuda("block",
			DimensionResult{Dim: DimSecurity, Verdict: VerdictBlock,
				Findings: []ReviewFinding{{Dimension: DimSecurity, File: "a.go", Line: 1, Severity: SevCritical, Description: "legacy finding"}}},
		))

	salida := riesgos([]Ficha{ficha})
	if len(salida) != 1 || !strings.Contains(salida[0], "legacy finding") {
		t.Errorf("legacy Dims-based rendering must still work when AggregatedFindings is empty: %v", salida)
	}
}

// TestRiesgosFiltersAdvisoryFromAggregatedFindings: the AggregatedFindings
// path must exclude ADVISORY the same way the legacy path always did — this
// path had no coverage of its own severity filter before (T6.5 review
// finding: tests WARNING).
func TestRiesgosFiltersAdvisoryFromAggregatedFindings(t *testing.T) {
	ficha := fichaAyuda("6b127cd", "fix(x): thing", "m", Revision{
		At:     time.Now().UTC(),
		Result: "block",
		AggregatedFindings: []Hallazgo{
			{Dimension: DimSecurity, Severity: SevCritical, Description: "critical one"},
			{Dimension: DimSpec, Severity: SevAdvisory, Description: "advisory one"},
		},
	})

	salida := riesgos([]Ficha{ficha})
	if len(salida) != 1 {
		t.Fatalf("riesgos() = %d lines, expected 1 (ADVISORY excluded): %v", len(salida), salida)
	}
	if !strings.Contains(salida[0], "critical one") {
		t.Errorf("missing the CRITICAL finding: %v", salida)
	}
	if strings.Contains(salida[0], "advisory one") {
		t.Errorf("ADVISORY must not appear in riesgos(): %v", salida)
	}
}

// TestRenderMergedFindingOmitsEmptyLocation: a Hallazgo without a resolved
// location must not render the placeholder "(:0)" — the location suffix is
// omitted entirely instead (T6.5 review finding: logic ADVISORY).
func TestRenderMergedFindingOmitsEmptyLocation(t *testing.T) {
	h := Hallazgo{Dimension: DimLogic, Severity: SevWarning, Description: "no location resolved"}
	got := renderMergedFinding("abc1234", h)
	esperado := "- ⚠️ `abc1234` [logic] WARNING (unknown, confidence 0.00) — no location resolved"
	if got != esperado {
		t.Errorf("renderMergedFinding() = %q, want %q", got, esperado)
	}
}

// TestMergedFindingEvidenceLinesFallsBackWhenEvidenceSetEmpty: an
// EvidenceSet that is non-nil but carries no Values (e.g. deserialized from
// {"evidence_set":{"values":[]}}) must still fall back to the legacy
// Evidence string instead of silently dropping it (T6.5 review finding:
// logic WARNING).
func TestMergedFindingEvidenceLinesFallsBackWhenEvidenceSetEmpty(t *testing.T) {
	h := Hallazgo{
		Dimension:   DimLogic,
		Evidence:    "legacy evidence text",
		Confidence:  0.5,
		EvidenceSet: &FindingEvidenceSet{},
	}
	lineas := mergedFindingEvidenceLines(h)
	if len(lineas) != 1 || !strings.Contains(lineas[0], "legacy evidence text") {
		t.Errorf("mergedFindingEvidenceLines() = %v, expected fallback to Evidence", lineas)
	}
}

// TestSanitizeEvidenceTruncatesLongEvidence: evidence text is bounded before
// it reaches the PR body — an oversized fragment (possibly an embedded
// secret in a security finding) must not be published in full on an
// external, indexable, cached surface (T6.5 review finding: security
// WARNING).
func TestSanitizeEvidenceTruncatesLongEvidence(t *testing.T) {
	larga := strings.Repeat("x", evidenceEmbedMaxBytes+100)
	got := sanitizeEvidence(larga)
	if len(got) > evidenceEmbedMaxBytes+2 { // +2: the wrapping backticks.
		t.Errorf("sanitizeEvidence did not bound the evidence length: %d bytes", len(got))
	}
}

// TestSanitizeEvidenceCollapsesNewlinesAndEscapesBackticks: evidence text is
// untrusted (an LLM inference or a command's literal output); an embedded
// newline or backtick must not be able to break or forge the surrounding
// Markdown list structure sent to GitHub (T6.5 review finding: security
// WARNING).
func TestSanitizeEvidenceCollapsesNewlinesAndEscapesBackticks(t *testing.T) {
	got := sanitizeEvidence("line one\nline two\r\nwith a ` backtick")
	if strings.Contains(got, "\n") || strings.Contains(got, "\r") {
		t.Errorf("sanitizeEvidence must collapse internal line breaks: %q", got)
	}
	if !strings.HasPrefix(got, "`") || !strings.HasSuffix(got, "`") {
		t.Fatalf("sanitized evidence must be wrapped in inline code: %q", got)
	}
	interior := got[1 : len(got)-1]
	if strings.Contains(interior, "`") {
		t.Errorf("a literal backtick inside the evidence must not terminate the code span early: %q", got)
	}
}

// TestRenderPlantillaSeccionRiesgos: la sección "## Riesgos" aparece con el
// placeholder cuando no hay riesgos y con las líneas renderizadas cuando hay
// hallazgos CRITICAL/WARNING pendientes (los ADVISORY no se listan).
func TestRenderPlantillaSeccionRiesgos(t *testing.T) {
	fichasSinRiesgos := []Ficha{fichaAyuda("u1", "feat(a)", "m", revisionAyuda("ok"))}
	salidaVacia := RenderPlantillaPr(fichasSinRiesgos, nil, VerificacionPlantilla{Modo: "omitido"}, "0.2.0")
	if !strings.Contains(salidaVacia, "## Riesgos") {
		t.Fatalf("falta la sección Riesgos: %s", salidaVacia)
	}
	if !strings.Contains(salidaVacia, "Sin riesgos pendientes") {
		t.Errorf("sin riesgos debe mostrar el placeholder honesto: %s", salidaVacia)
	}

	fichasConRiesgos := []Ficha{fichaAyuda("u1", "feat(a)", "m",
		revisionAyuda("warn",
			DimensionResult{Dim: DimSecurity, Verdict: VerdictWarn,
				Findings: []ReviewFinding{
					{Dimension: DimSecurity, File: "a.go", Line: 7,
						Severity: SevCritical, Description: "dato expuesto"},
					{Dimension: DimSpec, File: "b.go", Line: 1,
						Severity: SevAdvisory, Description: "scope amplio"},
				}},
		))}
	salidaCon := RenderPlantillaPr(fichasConRiesgos, nil, VerificacionPlantilla{Modo: "omitido"}, "0.2.0")
	if !strings.Contains(salidaCon, "dato expuesto") {
		t.Errorf("el CRITICAL debe listarse en Riesgos: %s", salidaCon)
	}
	if !strings.Contains(salidaCon, "CRITICAL") {
		t.Errorf("la línea de riesgo debe citar la severidad: %s", salidaCon)
	}
	if strings.Contains(salidaCon, "scope amplio") {
		t.Errorf("los ADVISORY no son riesgos y no deben listarse: %s", salidaCon)
	}
	if strings.Contains(salidaCon, "Sin riesgos pendientes") {
		t.Errorf("con riesgos no debe mostrarse el placeholder: %s", salidaCon)
	}

	// Un block corregido (FixedIn) no es un riesgo pendiente.
	corregida := fichaAyuda("f1", "feat(a)", "m",
		revisionAyuda("block",
			DimensionResult{Dim: DimSecurity, Verdict: VerdictBlock,
				Findings: []ReviewFinding{{Dimension: DimSecurity, Severity: SevCritical, Description: "dato expuesto"}}},
		))
	corregida.FixedIn = "a1b2c3d"
	salidaCorregida := RenderPlantillaPr([]Ficha{corregida}, nil, VerificacionPlantilla{Modo: "omitido"}, "0.2.0")
	if strings.Contains(salidaCorregida, "dato expuesto") {
		t.Errorf("los hallazgos de una ficha corregida no son riesgos pendientes: %s", salidaCorregida)
	}
	if !strings.Contains(salidaCorregida, "Sin riesgos pendientes") {
		t.Errorf("con todo corregido debe mostrarse el placeholder: %s", salidaCorregida)
	}
}
