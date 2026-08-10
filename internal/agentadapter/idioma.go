package agentadapter

// IdiomaPorDefecto es el idioma de los mensajes de commit cuando el yml no
// dice otra cosa: el del historial de este repositorio.
const IdiomaPorDefecto = "es"

// instruccionPorIdioma es la frase que fija el idioma dentro del prompt.
// Existe por T0.13: `construirPromptAgente` pedía «un mensaje de commit
// semántico bajo el estándar Conventional Commits» sin decir en qué idioma,
// así que el modelo lo elegía al azar. La fragmentación de T0.1 salió con 11
// mensajes en inglés y 2 en castellano dentro de la misma ejecución.
var instruccionPorIdioma = map[string]string{
	"es": "Escribe la descripción en castellano, sin tildes ni caracteres especiales.",
	"en": "Write the description in English.",
}

// ejemploPorIdioma acompaña a la instrucción con un mensaje real del formato
// esperado. Decir el idioma sin enseñarlo deja margen a la interpretación.
var ejemploPorIdioma = map[string]string{
	"es": "feat(git): anadir validacion de rutas del plan",
	"en": "feat(git): add plan path validation",
}

// instruccionDeIdioma devuelve la instrucción y el ejemplo del idioma pedido.
// Un idioma no reconocido cae al de por defecto en vez de dejar el prompt sin
// instrucción, que es justo el estado que T0.13 corrige.
func instruccionDeIdioma(idioma string) (string, string) {
	instruccion, ok := instruccionPorIdioma[idioma]
	if !ok {
		idioma = IdiomaPorDefecto
		instruccion = instruccionPorIdioma[idioma]
	}
	return instruccion, ejemploPorIdioma[idioma]
}

// idiomaCommit devuelve el idioma configurado para este adaptador, o el de
// por defecto si no se fijó ninguno.
func (c *CLIAdapter) idiomaCommit() string {
	if c.CommitLanguage == "" {
		return IdiomaPorDefecto
	}
	return c.CommitLanguage
}
