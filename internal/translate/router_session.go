package translate

import "strings"

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

// parseRouterSessionCommand matches the directive on the first non-empty line
// and returns the text with that line removed. Restricted to the leading line
// for the same reason as the other directives: pasted transcripts must not be
// able to fire one. The directive takes no arguments, so a line carrying
// anything after the token is left alone as ordinary prompt text.
func parseRouterSessionCommand(text string) (found bool, stripped string) {
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
		for _, tok := range routerSessionTokens {
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
	return true, strings.TrimSpace(prefix + strings.Join(remaining, "\n"))
}
