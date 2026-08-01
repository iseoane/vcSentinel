package agentadapter

type AgentAdapter interface {
	ObtenerMensajeCommit(rutasArchivos []string, capa string, batchNum int) (string, error)
}
