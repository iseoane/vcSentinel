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
