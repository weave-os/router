package translate

// listModelsTokens are the spellings of the list-models directive in both
// sigils: Codex reserves `/…` for its own built-ins and exposes it as a
// `$list-models` skill.
var listModelsTokens = [...]string{
	"/list-models",
	"$list-models",
}

// ExtractListModelsCommand reports whether the trailing user message is a bare
// list-models directive, stripping it so no upstream ever sees it. The answer
// is a read of the force-model catalog and this request's policy, so the
// router answers it without a model turn.
func (env *RequestEnvelope) ExtractListModelsCommand() bool {
	return env.extractLeadingCommand(parseListModelsCommand)
}

func parseListModelsCommand(text string) (found bool, stripped string) {
	return parseBareDirective(text, listModelsTokens[:])
}
