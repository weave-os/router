package translate

var listModelsTokens = [...]string{
	"/list-models",
	"$list-models",
}

func (e *RequestEnvelope) ExtractListModelsCommand() bool {
	return e.extractLeadingCommand(parseListModelsCommand)
}

func parseListModelsCommand(text string) (found bool, stripped string) {
	return parseBareDirective(text, listModelsTokens[:])
}
