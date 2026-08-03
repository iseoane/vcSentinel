package config

// PerfilResuelto es el resultado de resolver qué agente, modelo y esfuerzo
// usan una dimensión de auditoría concreta. Los campos vacíos indican
// "heredar del nivel inferior": Binario "" = active_agent (auto = primero
// disponible en el PATH); Modelo/Esfuerzo "" = los del agente elegido.
type PerfilResuelto struct {
	Nombre   string
	Binario  string
	Modelo   string
	Esfuerzo string
}

// ResolverPerfil decide el perfil de una dimensión: el override explícito
// gana; si no, el mapa review.dims; si la dimensión no está mapeada, el
// perfil "normal". El perfil resultante puede estar indefinido en
// cfg.Profiles (receta vacía), lo que se interpreta como heredar todo.
func ResolverPerfil(cfg Config, dimension, override string) PerfilResuelto {
	nombre := override
	if nombre == "" {
		nombre = cfg.Review.Dims[dimension]
	}
	if nombre == "" {
		nombre = "normal"
	}
	p := cfg.Profiles[nombre]
	return PerfilResuelto{Nombre: nombre, Binario: p.Agent, Modelo: p.Model, Esfuerzo: p.ReasoningEffort}
}
