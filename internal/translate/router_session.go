package translate

var routerSessionTokens = [...]string{
	"/router-session",
	"$router-session",
}

func (e *RequestEnvelope) ExtractRouterSessionCommand() bool {
	return e.extractLeadingCommand(parseRouterSessionCommand)
}

func parseRouterSessionCommand(text string) (found bool, stripped string) {
	return parseBareDirective(text, routerSessionTokens[:])
}
