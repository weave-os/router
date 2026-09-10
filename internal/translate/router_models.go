package translate

import "strings"

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

// parseRouterModelsCommand matches a bare directive on the first non-empty
// line and returns the text with that line removed. Restricted to the leading
// line so a pasted transcript cannot fire one.
func parseRouterModelsCommand(text string) (found bool, stripped string) {
	prefixEnd := leadingInjectedPrefixEnd(text)
	prefix := text[:prefixEnd]
	body := text[prefixEnd:]

	lines := strings.Split(body, "\n")
	cmdIdx := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		for _, tok := range routerModelsTokens {
			if trimmed == tok {
				cmdIdx = i
				break
			}
		}
		break
	}
	if cmdIdx < 0 {
		return false, text
	}

	remaining := make([]string, 0, len(lines)-1)
	remaining = append(remaining, lines[:cmdIdx]...)
	remaining = append(remaining, lines[cmdIdx+1:]...)
	stripped = strings.TrimSpace(prefix + strings.Join(remaining, "\n"))
	// Nothing but the directive may remain. Matching the token alone and
	// discarding the rest would swallow whatever the user wrote under it --
	// and for router-models it would silently drop a mutating argument split
	// across lines ("$router-models\nenable gpt-5.5"), answering with a bare
	// listing instead of falling through to the skill that can apply it.
	if !isOnlyKnownInjectedText(stripped) {
		return false, text
	}
	return true, stripped
}
