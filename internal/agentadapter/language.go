package agentadapter

// DefaultLanguage is the language of commit messages when the yml does not
// say otherwise: English.
const DefaultLanguage = "en"

// instructionByLanguage is the phrase that fixes the language inside the
// prompt. It exists for T0.13: `buildAgentPrompt` asked for "a semantic commit
// message under the Conventional Commits standard" without saying in which
// language, so the model chose at random. The T0.1 fragmentation came out with
// 11 messages in English and 2 in Spanish within the same run.
var instructionByLanguage = map[string]string{
	"es": "Escribe la descripción en castellano, sin tildes ni caracteres especiales.",
	"en": "Write the description in English.",
}

// exampleByLanguage accompanies the instruction with a real message of the
// expected format. Stating the language without showing it leaves room for
// interpretation.
var exampleByLanguage = map[string]string{
	"es": "feat(git): anadir validacion de rutas del plan",
	"en": "feat(git): add plan path validation",
}

// instructionAndExample returns the instruction and example of the requested
// language. An unrecognized language falls back to the default instead of
// leaving the prompt without an instruction, which is exactly the state T0.13
// corrects.
func instructionAndExample(language string) (string, string) {
	instruction, ok := instructionByLanguage[language]
	if !ok {
		language = DefaultLanguage
		instruction = instructionByLanguage[language]
	}
	return instruction, exampleByLanguage[language]
}

// commitLanguage returns the language configured for this adapter, or the
// default one if none was set.
func (c *CLIAdapter) commitLanguage() string {
	if c.CommitLanguage == "" {
		return DefaultLanguage
	}
	return c.CommitLanguage
}
