package translate

var routerModelsTokens = [...]string{
	"/router-models",
	"$router-models",
	"/models",
	"$models",
}

func (env *RequestEnvelope) ExtractRouterModelsCommand() bool {
	return env.extractLeadingCommand(parseRouterModelsCommand)
}

func parseRouterModelsCommand(text string) (found bool, stripped string) {
	return parseBareDirective(text, routerModelsTokens[:])
}
