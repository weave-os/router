package translate

var listModelsTokens = [...]string{
	"/list-models",
	"$list-models",
}

func (env *RequestEnvelope) ExtractListModelsCommand() bool {
	return env.extractLeadingCommand(parseListModelsCommand)
}

func parseListModelsCommand(text string) (found bool, stripped string) {
	return parseBareDirective(text, listModelsTokens[:])
}
