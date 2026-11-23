package credentials

import (
	"net/http"
	"strings"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/translate"
)

const (
	claudeCodeBudgetVersion        = "2.1.257"
	ClaudeCodeDefaultWindow        = 200_000
	claudeCodeCompactOutputReserve = 20_000
	claudeCodeAutoCompactBuffer    = 13_000
)

func ResolveClientBudget(clientIdentity requestcontext.ClientIdentity, headers http.Header, model string, hadVariant bool) router.ClientBudget {
	budget := router.ClientBudget{
		ModelVariant1M:   hadVariant,
		InboundContext1M: translate.HasContext1MBeta(headers),
	}
	if clientIdentity.ClientApp != requestcontext.ClientAppClaudeCode {
		return budget
	}
	versionAndMetadata, ok := strings.CutPrefix(clientIdentity.UserAgent, "claude-cli/")
	if !ok {
		return budget
	}
	budget.Version, _, _ = strings.Cut(versionAndMetadata, " ")
	if budget.Version != claudeCodeBudgetVersion {
		return budget
	}
	model = router.StripDateSuffix(model)
	if rest, ok := strings.CutPrefix(model, "anthropic/"); ok {
		model = rest
	}
	if _, known := catalog.ByID(model); !known || !strings.HasPrefix(model, "claude-") {
		return budget
	}
	if budget.InboundContext1M && !hadVariant && router.Lookup(model).Supports(router.CapExtendedContext) {
		budget.Evidence = router.ClientBudgetAmbiguousLongContext
		return budget
	}
	budget.Evidence = router.ClientBudgetHarnessDefault
	budget.DefaultWindow = ClaudeCodeDefaultWindow
	if hadVariant && router.Lookup(model).Supports(router.CapExtendedContext) {
		budget.DefaultWindow = 1_000_000
	}
	budget.DefaultCompactThreshold = budget.DefaultWindow - claudeCodeCompactOutputReserve - claudeCodeAutoCompactBuffer
	return budget
}
