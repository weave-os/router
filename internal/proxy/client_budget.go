package proxy

import (
	"net/http"
	"strings"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/translate"
)

const (
	// Verified against the shipped client's custom-base-URL window calculation.
	claudeCodeBudgetVersion        = "2.1.257"
	claudeCodeDefaultWindow        = 200_000
	claudeCodeCompactOutputReserve = 20_000
	claudeCodeAutoCompactBuffer    = 13_000
)

func resolveClientBudget(clientIdentity ClientIdentity, headers http.Header, model string, hadVariant bool) router.ClientBudget {
	budget := router.ClientBudget{
		ModelVariant1M:   hadVariant,
		InboundContext1M: translate.HasContext1MBeta(headers),
	}
	if clientIdentity.ClientApp != ClientAppClaudeCode {
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
	// ANTHROPIC_BETAS can add this capability without changing the local window.
	// The shipped client also strips a real [1m] suffix before sending it.
	if budget.InboundContext1M && !hadVariant && router.Lookup(model).Supports(router.CapExtendedContext) {
		budget.Evidence = router.ClientBudgetAmbiguousLongContext
		return budget
	}
	budget.Evidence = router.ClientBudgetHarnessDefault
	budget.DefaultWindow = claudeCodeDefaultWindow
	if hadVariant && router.Lookup(model).Supports(router.CapExtendedContext) {
		budget.DefaultWindow = 1_000_000
	}
	budget.DefaultCompactThreshold = budget.DefaultWindow - claudeCodeCompactOutputReserve - claudeCodeAutoCompactBuffer
	return budget
}
