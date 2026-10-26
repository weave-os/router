package proxy

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
)

func observedOverflowRequest() router.Request {
	return router.Request{
		GatewayProviders: map[string]struct{}{providers.ProviderAnthropicGateway: {}, providers.ProviderOpenAIGateway: {}},
		CustomBindings: map[string][]string{
			gatewayCompactionOpusModel: {providers.ProviderAnthropicGateway},
			catalog.ModelGPT6Luna:      {providers.ProviderOpenAIGateway},
		},
	}
}

// A thread GPT billed close to its cap is past Claude's 1M window even though
// the body-size estimate still fits Claude; routing there only buys a 400.
func TestExcludeObservedOverflowModelsDropsModelsPastTheThreadsUsage(t *testing.T) {
	svc, ctx, _, _ := gatewayCompactionFixture(nil)
	estimate := 900_000
	res := turnLoopResult{
		ContextEstimate: estimate,
		TokenizerCalibration: sessionpin.TokenizerCalibration{}.
			Observe(sessionpin.TokenizerFamilyOpenAI, 700_000, estimate, time.Now()),
	}
	req := observedOverflowRequest()
	req.OverflowAdmittedModels = map[string]struct{}{gatewayCompactionOpusModel: {}}

	got := svc.excludeObservedOverflowModels(ctx, req, res, contextWindowOutputReserve)

	assert.Contains(t, got.ExcludedModels, gatewayCompactionOpusModel)
	assert.Contains(t, got.ContextWindowExcludedModels, gatewayCompactionOpusModel)
	assert.NotContains(t, got.OverflowAdmittedModels, gatewayCompactionOpusModel)
	assert.NotContains(t, got.ExcludedModels, catalog.ModelGPT6Luna, "700k still fits Luna")
	assert.Empty(t, req.ExcludedModels, "the caller's exclusion set is not mutated")
}

// After compaction the body shrinks, so the same thread fits again.
func TestExcludeObservedOverflowModelsReadmitsACompactedThread(t *testing.T) {
	svc, ctx, _, _ := gatewayCompactionFixture(nil)
	res := turnLoopResult{
		ContextEstimate: 30_000,
		TokenizerCalibration: sessionpin.TokenizerCalibration{}.
			Observe(sessionpin.TokenizerFamilyOpenAI, 700_000, 900_000, time.Now()),
	}

	got := svc.excludeObservedOverflowModels(ctx, observedOverflowRequest(), res, contextWindowOutputReserve)

	assert.Empty(t, got.ExcludedModels)
}

// A context overflow names its own cause in the badge instead of
// "rejected the request (client issue)".
func TestRescueDetailLineNamesContextOverflow(t *testing.T) {
	attempt := tracedAttempt{
		Target:    inference.Target{Provider: providers.ProviderAnthropicGateway, CatalogID: gatewayCompactionOpusModel},
		Diagnosis: inference.AttemptDiagnosis{FailureOwner: string(FailureOwnerClient)},
		Err: &providers.UpstreamErrorResponse{
			Status: http.StatusBadRequest,
			Body:   []byte(`{"message":"prompt is too long: 1400000 tokens > 1000000 maximum"}`),
		},
	}
	line := rescueDetailLine(attempt)

	assert.Contains(t, line, "found the conversation too long for its context window")
	assert.NotContains(t, line, "rejected the request")
	assert.NotContains(t, line, "client issue")
}
