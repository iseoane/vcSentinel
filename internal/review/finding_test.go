package review

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestParsearDimensionResultLimpio(t *testing.T) {
	salida := `{"dim":"logic","verdict":"warn","findings":[{"dimension":"logic","file":"internal/a.go","line":10,"severity":"WARNING","description":"condición redundante","suggestion":"simplifica"}]}`
	resultado, err := ParsearDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParsearDimensionResult devolvió error: %v", err)
	}
	if resultado.Dim != DimLogic {
		t.Errorf("dim = %q, esperado %q", resultado.Dim, DimLogic)
	}
	if resultado.Verdict != VerdictWarn {
		t.Errorf("verdict = %q, esperado %q", resultado.Verdict, VerdictWarn)
	}
	if len(resultado.Findings) != 1 {
		t.Fatalf("findings = %d, esperado 1", len(resultado.Findings))
	}
	if resultado.Findings[0].Severity != SevWarning {
		t.Errorf("severity = %q, esperado %q", resultado.Findings[0].Severity, SevWarning)
	}
}

func TestParsearDimensionResultConFencesYTexto(t *testing.T) {
	salida := "Analizando el diff...\n```json\nBEGIN_REVIEW\n{\"dim\":\"security\",\"verdict\":\"ok\"}\nEND_REVIEW\n```\nFin"
	resultado, err := ParsearDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParsearDimensionResult devolvió error: %v", err)
	}
	if resultado.Dim != DimSecurity || resultado.Verdict != VerdictOK {
		t.Errorf("esperado security/ok, obtenido %s/%s", resultado.Dim, resultado.Verdict)
	}
}

func TestParsearDimensionResultSinDelimitadores(t *testing.T) {
	salida := "{\"dim\":\"design\",\"verdict\":\"warn\",\"findings\":[]}\n"
	resultado, err := ParsearDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParsearDimensionResult devolvió error: %v", err)
	}
	if resultado.Dim != DimDesign || resultado.Verdict != VerdictWarn {
		t.Errorf("esperado design/warn, obtenido %s/%s", resultado.Dim, resultado.Verdict)
	}
}

func TestParsearDimensionResultVacia(t *testing.T) {
	_, err := ParsearDimensionResult("")
	if !errors.Is(err, ErrSalidaVacia) {
		t.Errorf("se esperaba ErrSalidaVacia, obtenido %v", err)
	}
}

func TestParsearDimensionResultInvalida(t *testing.T) {
	_, err := ParsearDimensionResult("esto no es json\nBEGIN_REVIEW\ntampoco\nEND_REVIEW\n")
	if !errors.Is(err, ErrJSONLInvalido) {
		t.Errorf("se esperaba ErrJSONLInvalido, obtenido %v", err)
	}
}

func TestParsearDimensionResultClassifiesDeterministicOutputErrors(t *testing.T) {
	longSecret := strings.Repeat("x", 300)
	tests := []struct {
		name     string
		output   string
		class    SemanticOutputClass
		legacy   error
		redacted string
	}{
		{name: "empty payload", output: "", class: SemanticOutputMissingPayload, legacy: ErrSalidaVacia},
		{name: "prose payload", output: "I cannot provide the requested review.", class: SemanticOutputMissingPayload, legacy: ErrJSONLInvalido},
		{name: "tool denial prose", output: "Permission denied: Read(/host/private.go)", class: SemanticOutputToolDenied, legacy: ErrJSONLInvalido},
		{name: "malformed JSON", output: `{"dim":"logic",`, class: SemanticOutputMalformedJSON, legacy: ErrJSONLInvalido},
		{name: "schema invalid result", output: `{"dim":"logic","verdict":false}`, class: SemanticOutputSchemaInvalid, legacy: ErrJSONLInvalido},
		{name: "redacts and bounds excerpt", output: "tool denied token=" + longSecret, class: SemanticOutputToolDenied, legacy: ErrJSONLInvalido, redacted: longSecret},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParsearDimensionResult(tt.output)
			if !errors.Is(err, tt.legacy) {
				t.Fatalf("errors.Is(%v, %v) = false", err, tt.legacy)
			}
			var outputErr *SemanticOutputError
			if !errors.As(err, &outputErr) {
				t.Fatalf("error = %T %v, expected SemanticOutputError", err, err)
			}
			if outputErr.Class != tt.class {
				t.Errorf("class = %q, expected %q", outputErr.Class, tt.class)
			}
			if len([]rune(outputErr.Evidence)) > maxSemanticOutputEvidenceRunes {
				t.Errorf("evidence length = %d, expected at most %d", len([]rune(outputErr.Evidence)), maxSemanticOutputEvidenceRunes)
			}
			if tt.redacted != "" && strings.Contains(outputErr.Evidence, tt.redacted) {
				t.Errorf("evidence leaks the supplied secret: %q", outputErr.Evidence)
			}
		})
	}
}

func TestParsearDimensionResultDesconocida(t *testing.T) {
	_, err := ParsearDimensionResult(`{"dim":"perf","verdict":"ok"}`)
	if !errors.Is(err, ErrDimensionInvalida) {
		t.Errorf("se esperaba ErrDimensionInvalida, obtenido %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "perf") {
		t.Errorf("el error debería nombrar la dimensión desconocida, obtenido %v", err)
	}
}

func TestParsearDimensionResultVeredictoDeFactoConHallazgos(t *testing.T) {
	// El agente real devuelve a veces "issues" como veredicto con hallazgos:
	// con hallazgos se deriva de las severidades, sin hallazgos es un error.
	salida := `{"dim":"logic","verdict":"issues","findings":[{"dimension":"logic","file":"a.go","line":1,"severity":"WARNING","description":"d"}]}`
	resultado, err := ParsearDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParsearDimensionResult devolvió error: %v", err)
	}
	if resultado.Verdict != VerdictWarn {
		t.Errorf("verdict = %q, esperado %q derivado de WARNING", resultado.Verdict, VerdictWarn)
	}
	if len(resultado.Advertencias) == 0 {
		t.Error("se esperaba advertencia por la normalización del veredicto")
	}

	if _, err := ParsearDimensionResult(`{"dim":"logic","verdict":"issues"}`); !errors.Is(err, ErrVeredictoInvalido) {
		t.Errorf("veredicto de facto sin hallazgos debería ser %v, obtenido %v", ErrVeredictoInvalido, err)
	}
}

func TestParsearDimensionResultOkConCriticoSubeABlock(t *testing.T) {
	salida := `{"dim":"security","verdict":"ok","findings":[{"dimension":"security","file":"a.go","line":2,"severity":"CRITICAL","description":"secreto expuesto"}]}`
	resultado, err := ParsearDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParsearDimensionResult devolvió error: %v", err)
	}
	if resultado.Verdict != VerdictBlock {
		t.Errorf("verdict = %q, esperado %q (los hallazgos mandan)", resultado.Verdict, VerdictBlock)
	}
}

func TestParsearDimensionResultOkConWarningSubeAWarn(t *testing.T) {
	salida := `{"dim":"style","verdict":"ok","findings":[{"dimension":"style","file":"a.go","line":3,"severity":"ADVISORY","description":"nombre confuso"}]}`
	resultado, err := ParsearDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParsearDimensionResult devolvió error: %v", err)
	}
	if resultado.Verdict != VerdictWarn {
		t.Errorf("verdict = %q, esperado %q (ADVISORY presente)", resultado.Verdict, VerdictWarn)
	}
}

func TestParsearDimensionResultQuestionSeRespeta(t *testing.T) {
	salida := `{"dim":"logic","verdict":"question","questions":[{"id":"Q1","text":"¿X?"}]}`
	resultado, err := ParsearDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParsearDimensionResult devolvió error: %v", err)
	}
	if resultado.Verdict != VerdictQuestion {
		t.Errorf("verdict = %q, esperado %q", resultado.Verdict, VerdictQuestion)
	}
}

func TestParsearDimensionResultSeveridadDesconocida(t *testing.T) {
	salida := `{"dim":"logic","verdict":"warn","findings":[{"dimension":"logic","file":"a.go","line":1,"severity":"FATAL","description":"d"}]}`
	resultado, err := ParsearDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParsearDimensionResult devolvió error: %v", err)
	}
	if len(resultado.Findings) != 1 {
		t.Fatalf("findings = %d, esperado 1", len(resultado.Findings))
	}
	if resultado.Findings[0].Severity != SevAdvisory {
		t.Errorf("severity = %q, esperado normalizada a %q", resultado.Findings[0].Severity, SevAdvisory)
	}
	if len(resultado.Advertencias) == 0 {
		t.Error("se esperaba una advertencia por la normalización")
	}
}

func TestParsearDimensionResultDescartaLineasBasura(t *testing.T) {
	// Las líneas que ni siquiera decodifican como JSON se descartan; la línea
	// válida gana. (Una línea JSON válida con dimensión desconocida, en cambio,
	// aborta con error explícito: TestParsearDimensionResultDesconocida.)
	salida := "texto del agente sin sentido\n```\n{\"dim\":\"tests\",\"verdict\":\"ok\"}\n```\n"
	resultado, err := ParsearDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParsearDimensionResult devolvió error: %v", err)
	}
	if resultado.Dim != DimTests || resultado.Verdict != VerdictOK {
		t.Errorf("esperado tests/ok, obtenido %s/%s", resultado.Dim, resultado.Verdict)
	}
	// La línea de texto y el fence no decodifican: deben quedar registrados.
	encontroAdvertencia := false
	for _, adv := range resultado.Advertencias {
		if len(adv) > 0 {
			encontroAdvertencia = true
		}
	}
	if !encontroAdvertencia {
		t.Errorf("se esperaba advertencia por líneas descartadas, advertencias = %v", resultado.Advertencias)
	}
}

func TestParsearDimensionResultPreguntas(t *testing.T) {
	salida := `{"dim":"logic","verdict":"question","questions":[{"id":"Q1","text":"¿El rebase debe abortar si hay cambios sin commitear?"}]}`
	resultado, err := ParsearDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParsearDimensionResult devolvió error: %v", err)
	}
	if resultado.Verdict != VerdictQuestion {
		t.Errorf("verdict = %q, esperado %q", resultado.Verdict, VerdictQuestion)
	}
	if len(resultado.Questions) != 1 || resultado.Questions[0].ID != "Q1" {
		t.Errorf("questions = %+v, esperado una con ID Q1", resultado.Questions)
	}
}

func TestHallazgoSerializacionIdaYVuelta(t *testing.T) {
	original := Hallazgo{
		ID:     "h-1",
		Source: SourceReview,
		Producer: Productor{
			Agente:           "claude",
			Binario:          "",
			Modelo:           "claude-sonnet-5",
			Esfuerzo:         "high",
			ModeloVerificado: true,
		},
		Dimension:   DimLogic,
		Severity:    SevCritical,
		Confidence:  0.87,
		Status:      StatusPending,
		Title:       "condición siempre verdadera",
		Description: "el condicional nunca evalúa a falso por el operador usado",
		Location: Ubicacion{
			Archivo:     "internal/a.go",
			Blob:        "deadbeef",
			LineaInicio: 10,
			LineaFin:    12,
			Simbolo:     "FuncionX",
		},
		Evidence:       "if x >= 0 || x < 0 {",
		Impact:         "la rama de error nunca se ejecuta",
		Recommendation: "usa un único operador de comparación",
		Fixable:        FixableNeedsReview,
		IntroducedBy:   "abc123",
		Fingerprint:    "sha256:xyz",
	}

	crudo, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal devolvió error: %v", err)
	}

	var reconstruido Hallazgo
	if err := json.Unmarshal(crudo, &reconstruido); err != nil {
		t.Fatalf("Unmarshal devolvió error: %v", err)
	}

	if reconstruido != original {
		t.Errorf("el hallazgo no sobrevivió el ciclo completo:\noriginal:      %+v\nreconstruido:  %+v", original, reconstruido)
	}
}

func TestHallazgoConviveConReviewFindingV1EnElMismoPaquete(t *testing.T) {
	// Una ficha v1 debe poder seguir deserializándose sin error en el mismo
	// paquete que ya define el nuevo tipo Hallazgo (v2): ambos conviven sin
	// que la existencia de v2 rompa la lectura de v1.
	crudoV1 := `{"dimension":"logic","file":"a.go","line":5,"severity":"WARNING","description":"d","suggestion":"s"}`
	var findingV1 ReviewFinding
	if err := json.Unmarshal([]byte(crudoV1), &findingV1); err != nil {
		t.Fatalf("Unmarshal de ReviewFinding v1 devolvió error: %v", err)
	}
	if findingV1.Dimension != DimLogic || findingV1.Line != 5 {
		t.Errorf("finding v1 mal deserializado: %+v", findingV1)
	}

	crudoV2 := `{"id":"h-2","source":"validation","producer":{"agent":"validation","model_verified":false},"dimension":"tests","severity":"CRITICAL","confidence":1.0,"status":"pending","title":"t","description":"d","location":{"file":"b.go","line_start":1},"evidence":"e","fixable":"manual","fingerprint":"f"}`
	var hallazgoV2 Hallazgo
	if err := json.Unmarshal([]byte(crudoV2), &hallazgoV2); err != nil {
		t.Fatalf("Unmarshal de Hallazgo v2 devolvió error: %v", err)
	}
	if hallazgoV2.Source != SourceValidation || hallazgoV2.Confidence != 1.0 {
		t.Errorf("hallazgo v2 mal deserializado: %+v", hallazgoV2)
	}
}

// hallazgoConEvidencia construye un Hallazgo mínimo válido salvo por
// Evidence/Location.Archivo, que ajusta cada test según lo que quiera probar.
func hallazgoConEvidencia(id, archivo, evidencia string) Hallazgo {
	return Hallazgo{
		ID:        id,
		Source:    SourceReview,
		Dimension: DimLogic,
		Severity:  SevWarning,
		Status:    StatusPending,
		Location:  Ubicacion{Archivo: archivo},
		Evidence:  evidencia,
		Fixable:   FixableManual,
	}
}

func TestHallazgosConEvidenciaValidaEvidenciaInventadaSeDescarta(t *testing.T) {
	leer := func(archivo string) (string, error) {
		return "func Real() {\n\treturn nil\n}\n", nil
	}
	h := hallazgoConEvidencia("h-1", "a.go", "esto no aparece en ningún lado")

	validos, descartes := HallazgosConEvidenciaValida([]Hallazgo{h}, leer)

	if len(validos) != 0 {
		t.Errorf("validos = %d, esperado 0 (evidencia inventada)", len(validos))
	}
	if len(descartes) != 1 || descartes[0].Motivo != MotivoEvidenciaNoEncontrada {
		t.Errorf("descartes = %+v, esperado 1 con motivo %q", descartes, MotivoEvidenciaNoEncontrada)
	}
}

func TestHallazgosConEvidenciaValidaToleraReindentacion(t *testing.T) {
	// El contenido real tiene la línea con una indentación distinta a la que
	// citó el LLM: la normalización (trim de bordes) debe tolerarlo.
	leer := func(archivo string) (string, error) {
		return "func F() {\n        if x >= 0 || x < 0 {\n            return true\n        }\n}\n", nil
	}
	h := hallazgoConEvidencia("h-2", "a.go", "if x >= 0 || x < 0 {")

	validos, descartes := HallazgosConEvidenciaValida([]Hallazgo{h}, leer)

	if len(descartes) != 0 {
		t.Errorf("descartes = %+v, esperado ninguno (solo cambia indentación)", descartes)
	}
	if len(validos) != 1 || validos[0].ID != "h-2" {
		t.Errorf("validos = %+v, esperado [h-2]", validos)
	}
}

func TestHallazgosConEvidenciaValidaDescartaSoloElInvalido(t *testing.T) {
	contenidoA := "package a\n\nfunc A() {\n\treturn\n}\n"
	leer := func(archivo string) (string, error) {
		switch archivo {
		case "a.go":
			return contenidoA, nil
		default:
			return "", errors.New("archivo no encontrado en el commit")
		}
	}

	valido1 := hallazgoConEvidencia("h-ok-1", "a.go", "func A() {")
	invalido := hallazgoConEvidencia("h-malo", "a.go", "esto jamás apareció")
	valido2 := hallazgoConEvidencia("h-ok-2", "a.go", "package a")

	validos, descartes := HallazgosConEvidenciaValida([]Hallazgo{valido1, invalido, valido2}, leer)

	if len(validos) != 2 {
		t.Fatalf("validos = %d, esperado 2 (sobreviven los dos correctos)", len(validos))
	}
	if validos[0].ID != "h-ok-1" || validos[1].ID != "h-ok-2" {
		t.Errorf("validos = %+v, esperado [h-ok-1, h-ok-2] en orden", validos)
	}
	if len(descartes) != 1 || descartes[0].Hallazgo.ID != "h-malo" {
		t.Errorf("descartes = %+v, esperado solo h-malo", descartes)
	}
}

func TestHallazgosConEvidenciaValidaEvidenciaVacia(t *testing.T) {
	leer := func(archivo string) (string, error) { return "contenido", nil }
	h := hallazgoConEvidencia("h-3", "a.go", "")

	validos, descartes := HallazgosConEvidenciaValida([]Hallazgo{h}, leer)

	if len(validos) != 0 {
		t.Errorf("validos = %d, esperado 0 (evidencia vacía)", len(validos))
	}
	if len(descartes) != 1 || descartes[0].Motivo != MotivoSinEvidencia {
		t.Errorf("descartes = %+v, esperado 1 con motivo %q", descartes, MotivoSinEvidencia)
	}
}

func TestHallazgosConEvidenciaValidaArchivoNoResueltoNoPropagaError(t *testing.T) {
	// La lectura del contenido falla (archivo renombrado/borrado/ruta mal
	// escrita): el hallazgo se descarta, pero la función no debe propagar el
	// error ni entrar en panic.
	leer := func(archivo string) (string, error) {
		return "", errors.New("el archivo no existe en ese commit")
	}
	h := hallazgoConEvidencia("h-4", "no-existe.go", "algo")

	validos, descartes := HallazgosConEvidenciaValida([]Hallazgo{h}, leer)

	if len(validos) != 0 {
		t.Errorf("validos = %d, esperado 0 (archivo no resuelto)", len(validos))
	}
	if len(descartes) != 1 || descartes[0].Motivo != MotivoArchivoNoResuelto {
		t.Errorf("descartes = %+v, esperado 1 con motivo %q", descartes, MotivoArchivoNoResuelto)
	}
}

func TestHallazgosConEvidenciaValidaArchivoVacioEnLocation(t *testing.T) {
	leer := func(archivo string) (string, error) { return "contenido", nil }
	h := hallazgoConEvidencia("h-5", "", "algo")

	_, descartes := HallazgosConEvidenciaValida([]Hallazgo{h}, leer)

	if len(descartes) != 1 || descartes[0].Motivo != MotivoArchivoNoResuelto {
		t.Errorf("descartes = %+v, esperado 1 con motivo %q", descartes, MotivoArchivoNoResuelto)
	}
}

// hallazgoParaFingerprint construye un Hallazgo mínimo con los campos que
// intervienen en Fingerprint, dejando el resto en su cero-valor: los tests de
// esta sección solo varían Simbolo/Archivo, Evidence, Title y las líneas.
func hallazgoParaFingerprint(dimension, simbolo, archivo, evidencia, title string, lineaInicio, lineaFin int) Hallazgo {
	return Hallazgo{
		Dimension: dimension,
		Title:     title,
		Evidence:  evidencia,
		Location: Ubicacion{
			Archivo:     archivo,
			Simbolo:     simbolo,
			LineaInicio: lineaInicio,
			LineaFin:    lineaFin,
		},
	}
}

func TestFingerprintEstableAnteReindentadoYCambioDeLinea(t *testing.T) {
	// Mismo hallazgo lógico, pero el LLM lo citó con otra indentación y en
	// otras líneas (p. ej. el archivo se reformateó entre dos ejecuciones):
	// el fingerprint no debe depender de espacios de borde ni de la línea.
	a := hallazgoParaFingerprint(DimLogic, "FuncionX", "a.go",
		"if x >= 0 || x < 0 {", "condición siempre verdadera", 10, 12)
	b := hallazgoParaFingerprint(DimLogic, "FuncionX", "a.go",
		"    if x >= 0 || x < 0 {   ", "condición siempre verdadera", 45, 47)

	if Fingerprint(a) != Fingerprint(b) {
		t.Errorf("fingerprints distintos ante reindentado/renumeración: %q vs %q", Fingerprint(a), Fingerprint(b))
	}
}

func TestFingerprintEstableAnteRenumeracionPorEdicionEnOtraParteDelArchivo(t *testing.T) {
	// El archivo creció o perdió líneas ANTES del hallazgo (p. ej. se añadió
	// un import): la evidencia y el símbolo no cambian, solo la numeración.
	a := hallazgoParaFingerprint(DimSecurity, "ValidarToken", "auth.go",
		"if token == \"\" { return nil }", "validación de entrada ausente", 20, 20)
	b := hallazgoParaFingerprint(DimSecurity, "ValidarToken", "auth.go",
		"if token == \"\" { return nil }", "validación de entrada ausente", 63, 63)

	if Fingerprint(a) != Fingerprint(b) {
		t.Errorf("fingerprints distintos tras renumeración pura: %q vs %q", Fingerprint(a), Fingerprint(b))
	}
}

func TestFingerprintDistintoParaMismoDefectoEnDosSimbolos(t *testing.T) {
	// Mismo defecto (misma evidencia y regla) pero copiado/pegado en dos
	// funciones distintas: son dos instancias del defecto, no la misma.
	a := hallazgoParaFingerprint(DimLogic, "FuncionA", "a.go",
		"if x >= 0 || x < 0 {", "condición siempre verdadera", 1, 1)
	b := hallazgoParaFingerprint(DimLogic, "FuncionB", "a.go",
		"if x >= 0 || x < 0 {", "condición siempre verdadera", 1, 1)

	if Fingerprint(a) == Fingerprint(b) {
		t.Errorf("se esperaban fingerprints distintos para símbolos distintos, ambos %q", Fingerprint(a))
	}
}

func TestFingerprintDistintoParaDosDefectosEnMismoSimbolo(t *testing.T) {
	// Misma función, pero dos defectos independientes dentro de ella: deben
	// poder rastrearse por separado en revisiones sucesivas.
	a := hallazgoParaFingerprint(DimLogic, "FuncionX", "a.go",
		"if x >= 0 || x < 0 {", "condición siempre verdadera", 1, 1)
	b := hallazgoParaFingerprint(DimLogic, "FuncionX", "a.go",
		"return nil // TODO", "retorno sin manejar el error", 5, 5)

	if Fingerprint(a) == Fingerprint(b) {
		t.Errorf("se esperaban fingerprints distintos para defectos distintos, ambos %q", Fingerprint(a))
	}
}

func TestFingerprintUsaArchivoCuandoNoHaySimbolo(t *testing.T) {
	// Sin símbolo (el agente no siempre lo resuelve), el fingerprint debe
	// caer en Location.Archivo en vez de quedar vacío o colisionar con todo.
	a := hallazgoParaFingerprint(DimStyle, "", "b.go",
		"var x int", "variable sin usar", 1, 1)
	b := hallazgoParaFingerprint(DimStyle, "", "c.go",
		"var x int", "variable sin usar", 1, 1)

	if Fingerprint(a) == Fingerprint(b) {
		t.Errorf("se esperaban fingerprints distintos para archivos distintos sin símbolo, ambos %q", Fingerprint(a))
	}
}

func TestParsearDimensionResultFindingSoloV1NoGeneraHallazgo(t *testing.T) {
	// Contrato de hoy (F1-F4): el agente solo manda campos v1 dentro de
	// "findings". Hallazgos debe quedar vacío: no se detecta nada v2 porque
	// no hay ningún campo exclusivo de v2 presente.
	salida := `{"dim":"logic","verdict":"warn","findings":[{"dimension":"logic","file":"a.go","line":10,"severity":"WARNING","description":"d","suggestion":"s"}]}`
	resultado, err := ParsearDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParsearDimensionResult devolvió error: %v", err)
	}
	if len(resultado.Findings) != 1 {
		t.Fatalf("Findings = %d, esperado 1", len(resultado.Findings))
	}
	if len(resultado.Hallazgos) != 0 {
		t.Errorf("Hallazgos = %+v, esperado vacío (finding solo v1)", resultado.Hallazgos)
	}
}

// TestParsearDimensionResultLocationVaciaUsaRespaldoV1 reproduce B16: un
// "location": {} explícito (presente, pero sin Archivo ni Simbolo) no debe
// pisar el respaldo File/Line de v1 con una ubicación vacía, porque eso
// reintroduce la colisión de Fingerprint entre archivos distintos que el
// respaldo existe para evitar.
func TestParsearDimensionResultLocationVaciaUsaRespaldoV1(t *testing.T) {
	salida := `{"dim":"security","verdict":"warn","findings":[{"file":"a.go","line":7,"severity":"WARNING","description":"x","evidence":"y","location":{}}]}`
	resultado, err := ParsearDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParsearDimensionResult devolvió error: %v", err)
	}
	if len(resultado.Hallazgos) != 1 {
		t.Fatalf("Hallazgos = %d, esperado 1", len(resultado.Hallazgos))
	}
	loc := resultado.Hallazgos[0].Location
	if loc.Archivo != "a.go" || loc.LineaInicio != 7 {
		t.Errorf("Location = %+v, esperado el respaldo v1 (Archivo=a.go, LineaInicio=7), no una ubicación vacía", loc)
	}
}

func TestParsearDimensionResultFindingV1YV2GeneraAmbos(t *testing.T) {
	// Un finding con campos v1 (file/line/severity/description) Y algunos
	// campos v2 (evidence/confidence) debe aparecer en ambos lados: Findings
	// (v1, compatibilidad) y Hallazgos (v2, adelantándose a F5).
	salida := `{"dim":"security","verdict":"warn","findings":[{"file":"a.go","line":7,"severity":"WARNING","description":"posible fuga","evidence":"token := req.Header.Get(\"X\")","confidence":0.6}]}`
	resultado, err := ParsearDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParsearDimensionResult devolvió error: %v", err)
	}
	if len(resultado.Findings) != 1 {
		t.Fatalf("Findings = %d, esperado 1", len(resultado.Findings))
	}
	if resultado.Findings[0].File != "a.go" || resultado.Findings[0].Severity != SevWarning {
		t.Errorf("Findings[0] = %+v, no coincide con v1 esperado", resultado.Findings[0])
	}
	if len(resultado.Hallazgos) != 1 {
		t.Fatalf("Hallazgos = %d, esperado 1", len(resultado.Hallazgos))
	}
	h := resultado.Hallazgos[0]
	if h.Dimension != DimSecurity {
		t.Errorf("Hallazgos[0].Dimension = %q, esperado %q (dimensión de la línea)", h.Dimension, DimSecurity)
	}
	if h.Confidence != 0.6 {
		t.Errorf("Hallazgos[0].Confidence = %v, esperado 0.6", h.Confidence)
	}
	if h.Evidence == "" {
		t.Error("Hallazgos[0].Evidence vacío, esperado el valor del finding")
	}
	if h.Fingerprint == "" {
		t.Error("Hallazgos[0].Fingerprint vacío, esperado calculado")
	}
}

// TestParsearDimensionResultAcceptsCategoricalConfidence is a regression test
// for a real gate failure: the review prompt (T5.6) instructs the model to
// report confidence as "high"/"medium"/"low", but the parser only accepted a
// raw float. Any finding with a categorical confidence made the whole JSONL
// line fail json.Unmarshal, which made every line look invalid and produced
// ErrJSONLInvalido even though the model's output was well-formed.
func TestParsearDimensionResultAcceptsCategoricalConfidence(t *testing.T) {
	casos := []struct {
		nivel    string
		esperado float64
	}{
		{"high", 0.9},
		{"medium", 0.6},
		{"low", 0.3},
	}
	for _, caso := range casos {
		salida := `{"dim":"tests","verdict":"fail","findings":[{"dimension":"tests","file":"a_test.go","line":1,"severity":"WARNING","description":"d","evidence":"e","confidence":"` + caso.nivel + `"}]}`
		resultado, err := ParsearDimensionResult(salida)
		if err != nil {
			t.Fatalf("nivel %q: ParsearDimensionResult devolvió error: %v", caso.nivel, err)
		}
		if len(resultado.Hallazgos) != 1 {
			t.Fatalf("nivel %q: Hallazgos = %d, esperado 1", caso.nivel, len(resultado.Hallazgos))
		}
		if resultado.Hallazgos[0].Confidence != caso.esperado {
			t.Errorf("nivel %q: Confidence = %v, esperado %v", caso.nivel, resultado.Hallazgos[0].Confidence, caso.esperado)
		}
	}
}

func TestParsearDimensionResultRejectsUnknownConfidenceLevel(t *testing.T) {
	salida := `{"dim":"tests","verdict":"fail","findings":[{"dimension":"tests","file":"a_test.go","line":1,"severity":"WARNING","description":"d","evidence":"e","confidence":"certain"}]}`
	_, err := ParsearDimensionResult(salida)
	if err == nil {
		t.Fatal("ParsearDimensionResult() error = nil, expected an explicit error for an unknown confidence level")
	}
}

func TestParsearDimensionResultSeveridadV2DesconocidaSeNormaliza(t *testing.T) {
	// Misma regla de normalización de severidad que v1 (T2.4: un solo
	// criterio), aplicada también al Hallazgo v2 derivado del mismo finding.
	salida := `{"dim":"logic","verdict":"warn","findings":[{"file":"a.go","line":1,"severity":"FATAL","description":"d","evidence":"e"}]}`
	resultado, err := ParsearDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParsearDimensionResult devolvió error: %v", err)
	}
	if len(resultado.Hallazgos) != 1 {
		t.Fatalf("Hallazgos = %d, esperado 1", len(resultado.Hallazgos))
	}
	if resultado.Hallazgos[0].Severity != SevAdvisory {
		t.Errorf("Hallazgos[0].Severity = %q, esperado normalizada a %q", resultado.Hallazgos[0].Severity, SevAdvisory)
	}
	if resultado.Findings[0].Severity != SevAdvisory {
		t.Errorf("Findings[0].Severity = %q, esperado normalizada a %q", resultado.Findings[0].Severity, SevAdvisory)
	}
	if len(resultado.Advertencias) == 0 {
		t.Error("se esperaba advertencia por la normalización de severidad v2")
	}
}

func TestParsearDimensionResultUnavailable(t *testing.T) {
	salida := `{"dim":"security","verdict":"unavailable","reason":"rate_limit"}`
	resultado, err := ParsearDimensionResult(salida)
	if err != nil {
		t.Fatalf("ParsearDimensionResult devolvió error: %v", err)
	}
	if resultado.Verdict != VerdictUnavailable || resultado.Reason != "rate_limit" {
		t.Errorf("esperado unavailable/rate_limit, obtenido %s/%s", resultado.Verdict, resultado.Reason)
	}
}
