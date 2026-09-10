package translate

// routerSessionTokens are the spellings of the router-session directive.
// Codex reserves `/…` for its own built-ins and exposes the directive as a
// `$name` skill, so both sigils are accepted here for the same reason
// parseForceModelCommand accepts both.
var routerSessionTokens = [...]string{
	"/router-session",
	"$router-session",
}

// ExtractRouterSessionCommand reports whether the trailing user message opens
// with a router-session directive, stripping it so no upstream ever sees it.
//
// The router is the authority on the answer: the id this returns is the one it
// recorded for the session, so it cannot disagree with what telemetry stored.
// The client-side skill this supersedes read $CODEX_SESSION_ID and reported
// whatever that held, which is a different value (or an error) whenever the
// environment does not export one -- and cost a model turn plus a tool exec to
// produce it.
//
// Tool-result turns are deliberately excluded (extractLeadingCommand drops
// them): an agent asking for the session id mid-turn wants it in context, not
// a synthetic response that ends the turn.
func (env *RequestEnvelope) ExtractRouterSessionCommand() bool {
	return env.extractLeadingCommand(parseRouterSessionCommand)
}

// parseRouterSessionCommand recognizes the bare directive; parseBareDirective holds the
// rules, so a fix there reaches every directive at once.
func parseRouterSessionCommand(text string) (found bool, stripped string) {
	return parseBareDirective(text, routerSessionTokens[:])
}
