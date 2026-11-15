package translate

var routerSessionTokens = [...]string{
	"/router-session",
	"$router-session",
}

func (env *RequestEnvelope) ExtractRouterSessionCommand() bool {
	return env.extractLeadingCommand(parseRouterSessionCommand)
}

func parseRouterSessionCommand(text string) (found bool, stripped string) {
	return parseBareDirective(text, routerSessionTokens[:])
}
