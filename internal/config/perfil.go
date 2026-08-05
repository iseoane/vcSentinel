package config

import "strings"

// ResolverPerfilAgente resuelve el perfil anidado de UN agente concreto:
// lee cfg.Agents[agente].Profiles[perfil] y hace fallback al modelo y esfuerzo
// base del agente cuando el perfil no los define. Devuelve modelo y esfuerzo.
func ResolverPerfilAgente(cfg Config, agente, perfil string) (modelo, esfuerzo string) {
	a := cfg.Agents[agente]
	p := a.Profiles[perfil]
	modelo = p.Model
	if modelo == "" {
		modelo = a.Model
	}
	esfuerzo = p.ReasoningEffort
	if esfuerzo == "" {
		esfuerzo = a.ReasoningEffort
	}
	return modelo, esfuerzo
}

// PerfilResuelto es el resultado de resolver qué agente, modelo y esfuerzo
// usan una dimensión de auditoría concreta. Los campos vacíos indican
// "heredar del nivel inferior": Binario "" = active_agent (auto = los agentes
// disponibles en el orden de configuración del yml, con fallback en cadena);
// Modelo/Esfuerzo "" = los del agente elegido.
type PerfilResuelto struct {
	Nombre   string
	Binario  string
	Modelo   string
	Esfuerzo string
}

// ResolverPerfil decide el perfil de una dimensión: el override explícito
// gana; si no, el mapa review.dims; si la dimensión no está mapeada, el
// perfil "normal". El nombre acepta dos sintaxis:
//
//   - "agente.perfil" (v2): busca en agents.<agente>.profiles.<perfil>; el
//     modelo/esfuerzo del perfil, si están, pisan los del agente.
//   - "perfil" (v1/compat): busca en profiles.<perfil>; si no existe, el
//     perfil anidado del agente activo con ese nombre.
//
// El perfil resultante puede estar indefinido (receta vacía), lo que se
// interpreta como heredar todo del agente elegido.
func ResolverPerfil(cfg Config, dimension, override string) PerfilResuelto {
	nombre := override
	if nombre == "" {
		nombre = cfg.Review.Dims[dimension]
	}
	if nombre == "" {
		nombre = "normal"
	}

	// v2: "agente.perfil"
	if agente, perfil, ok := strings.Cut(nombre, "."); ok {
		modelo, esfuerzo := ResolverPerfilAgente(cfg, agente, perfil)
		return PerfilResuelto{Nombre: nombre, Binario: agente, Modelo: modelo, Esfuerzo: esfuerzo}
	}

	// v1/compat: perfil global con agente propio, o perfil anidado del agente activo.
	p := cfg.Profiles[nombre]
	if p.Agent != "" {
		return PerfilResuelto{Nombre: nombre, Binario: p.Agent, Modelo: p.Model, Esfuerzo: p.ReasoningEffort}
	}
	agente := cfg.ActiveAgent
	if agente == "auto" || agente == "" {
		return PerfilResuelto{Nombre: nombre, Binario: "", Modelo: p.Model, Esfuerzo: p.ReasoningEffort}
	}
	a := cfg.Agents[agente]
	anidado := a.Profiles[nombre]
	modelo := anidado.Model
	if modelo == "" {
		modelo = a.Model
	}
	esfuerzo := anidado.ReasoningEffort
	if esfuerzo == "" {
		esfuerzo = a.ReasoningEffort
	}
	return PerfilResuelto{Nombre: nombre, Binario: agente, Modelo: modelo, Esfuerzo: esfuerzo}
}
