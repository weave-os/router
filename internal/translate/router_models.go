package translate

var routerModelsTokens = [...]string{
	"/router-models",
	"$router-models",
	"/models",
	"$models",
}

func (e *RequestEnvelope) ExtractRouterModelsCommand() bool {
	return e.extractLeadingCommand(parseRouterModelsCommand)
}

func parseRouterModelsCommand(text string) (found bool, stripped string) {
	return parseBareDirective(text, routerModelsTokens[:])
}
