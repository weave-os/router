package proxy

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

// A cooling arm readmitted as the rescue's last resort must still honour the
// request's custom-tool exclusion; otherwise the rescue hands the projected
// tool to the model the scored pool kept away from it.
func TestRescueDecisions_CustomToolTurnNeverReadmitsUnsupportedCoolingArm(t *testing.T) {
	s := siblingService(providers.ProviderAnthropic, providers.ProviderDeepInfra)
	failed := overloadedDecision(&router.RoutingMetadata{})
	cooling := []string{catalog.ModelMiMoV26Pro}
	ctx := context.WithValue(context.Background(), SessionDemotedModelsContextKey{}, cooling)
	ctx = context.WithValue(ctx, SessionCooldownModelsContextKey{}, map[string]time.Time{catalog.ModelMiMoV26Pro: rateLimitTestNow.Add(30 * time.Second)})

	functionOnly := context.WithValue(ctx, responsesRequirementsContextKey{}, router.TranslationRequirements{FunctionTools: true})
	assert.Equal(t, cooling, siblingModels(s.siblingFailoverDecisions(functionOnly, failed, 1_000, 0, 0)))

	customTools := context.WithValue(ctx, responsesRequirementsContextKey{}, router.TranslationRequirements{CustomTools: true})
	assert.Empty(t, s.siblingFailoverDecisions(customTools, failed, 1_000, 0, 0))
}
