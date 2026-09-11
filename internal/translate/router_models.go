package translate

// routerModelsTokens are the spellings of the router-models directive and its
// `models` alias, in both sigils. Codex reserves `/…` for its own built-ins
// and exposes the directive as a `$name` skill, so both are accepted for the
// same reason parseForceModelCommand accepts both.
var routerModelsTokens = [...]string{
	"/router-models",
	"$router-models",
	"/models",
	"$models",
}

// ExtractRouterModelsCommand reports whether the trailing user message is a
// bare router-models directive, stripping it so no upstream ever sees it.
//
// Bare only, and that restriction is load-bearing. Listing is a read the
// router can answer from the request it is already holding. Mutating the
// selection (`enable`/`disable`/`prefer`/`providers`) is an admin operation:
// those endpoints sit behind WithAdminOnly, which rejects the rk_ data-plane
// key a chat request carries precisely so a leaked key cannot rewrite routing
// config. Answering an argument form here would either be a no-op or a hole
// through that boundary, so anything with arguments falls through to the
// skill, which shells out to the installer and authenticates properly.
func (env *RequestEnvelope) ExtractRouterModelsCommand() bool {
	return env.extractLeadingCommand(parseRouterModelsCommand)
}

// parseRouterModelsCommand recognizes the bare directive; parseBareDirective holds the
// rules, so a fix there reaches every directive at once.
func parseRouterModelsCommand(text string) (found bool, stripped string) {
	return parseBareDirective(text, routerModelsTokens[:])
}
