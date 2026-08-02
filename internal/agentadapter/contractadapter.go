package agentadapter

type AgentAdapter interface {
	ObtenerMensajeCommit(rutasArchivos []string, capa string, batchNum int) (string, error)
}

// AdapterConDiff es una interfaz opcional que un adaptador puede implementar
// para recibir el micro-diff exacto de la zona de preparación (git diff --cached)
// antes de generar el mensaje de commit. Si el adaptador no la implementa,
// el motor de slice usa la interfaz AgentAdapter base.
type AdapterConDiff interface {
	ObtenerMensajeCommitConDiff(rutasArchivos []string, capa string, batchNum int, diff string) (string, error)
}

// AdapterRefactor es una interfaz opcional que un adaptador puede implementar
// para refactorizar un archivo de código masivo (violación potencial de SRP):
// primero propone un plan de división y luego puede aplicarlo editando el
// working tree (sin hacer commits).
type AdapterRefactor interface {
	// ProponerPlanRefactor devuelve el plan de división en texto plano.
	ProponerPlanRefactor(rutaArchivo string) (string, error)
	// AplicarPlanRefactor ordena al agente ejecutar el plan directamente sobre
	// el working tree y devuelve un resumen breve de los cambios aplicados.
	AplicarPlanRefactor(rutaArchivo string, plan string) (string, error)
}
