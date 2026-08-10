package git

// Umbrales de volumen del guardián. Antes de T0.4 el 400 vivía en tres sitios
// sin relación entre sí —clasificarEstado, limiteLineasLote/limiteConfigGigante
// y review.LimiteDecisionChain—, así que recalibrar uno dejaba a los otros
// atrás en silencio (B4). Aquí está la única fuente; los demás derivan.
//
// Esta declaración unifica, NO recalibra: los valores son los de siempre.
const (
	// LimiteLineasRevisables es cuánto código puede revisarse de una sentada.
	// Es el umbral del guardián y, por la misma razón, el tamaño máximo de un
	// lote de fragmentación y el punto en que un archivo de configuración se
	// aísla en su propio commit.
	LimiteLineasRevisables = 400

	// UmbralPuntoOptimo es a partir de cuántas líneas el volumen pendiente ya
	// merece un commit: por debajo es PEQUENO, entre este valor y
	// LimiteLineasRevisables es PUNTO_OPTIMO.
	UmbralPuntoOptimo = 200

	// LimiteCodigoGigante es el máximo de líneas sugerido para un archivo de
	// código antes de ofrecer refactorizar, hacer bypass o abortar. Es mayor
	// que LimiteLineasRevisables a propósito: un archivo de 450 líneas cabe en
	// su propio lote, pero uno de 500 ya no se revisa de una sentada ni
	// aislado.
	LimiteCodigoGigante = 500
)

// «Líneas» significa tres cosas distintas en este proyecto y conviene no
// confundirlas al comparar contra estos umbrales (B4/O2):
//
//   - Añadidas: las que suma un diff (git numstat). Es lo que mide el
//     guardián en MedirVolumen y lo que compara clasificarEstado.
//   - Físicas: las que tiene un archivo en disco, para los no rastreados que
//     no aparecen en un diff (contarLineasFisicas).
//   - Añadidas+borradas: el churn de un rango de commits, que es lo que mide
//     review.LimiteDecisionChain sobre una rama entera.
//
// Los tres se comparan contra el mismo número porque miden la misma cosa —
// cuánto cambio puede revisar una persona— no porque sean intercambiables.
