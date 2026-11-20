package proxy

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
)

func TestRouteLogsAStrategyDecisionOutsideTheFilteredCandidatesAndServesIt(t *testing.T) {
	served := router.Decision{Model: "deepseek/deepseek-v4-pro", Provider: providers.ProviderFireworks}
	svc := serviceWithDefaultRouter(&registryRouter{decision: served})

	for violated, candidates := range map[string]router.Candidates{
		"excluded_models":                  {ExcludedModels: map[string]struct{}{served.Model: {}}},
		"context_window_excluded_models":   {ContextWindowExcludedModels: map[string]struct{}{served.Model: {}}},
		"unsigned_history_excluded_models": {UnsignedHistoryExcludedModels: map[string]struct{}{served.Model: {}}},
		"allowed_models":                   {AllowedModels: map[string]struct{}{"claude-haiku-4-5": {}}},
	} {
		t.Run(violated, func(t *testing.T) {
			var output bytes.Buffer
			ctx := observability.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&output, nil)))

			decision, err := svc.Route(ctx, router.Request{Candidates: candidates})

			require.NoError(t, err)
			assert.Equal(t, served.Model, decision.Model)
			assert.Contains(t, output.String(), `"level":"WARN"`)
			assert.Contains(t, output.String(), `"violated_set":"`+violated+`"`)
			assert.Contains(t, output.String(), `"model":"`+served.Model+`"`)
		})
	}
}

func TestRouteDoesNotLogAStrategyDecisionInsideTheFilteredCandidates(t *testing.T) {
	svc := serviceWithDefaultRouter(&registryRouter{decision: router.Decision{Model: "deepseek/deepseek-v4-pro", Provider: providers.ProviderFireworks}})
	var output bytes.Buffer
	ctx := observability.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&output, nil)))

	_, err := svc.Route(ctx, router.Request{Candidates: router.Candidates{
		ExcludedModels: map[string]struct{}{"claude-haiku-4-5": {}},
		AllowedModels:  map[string]struct{}{"deepseek/deepseek-v4-pro": {}},
	}})

	require.NoError(t, err)
	assert.NotContains(t, output.String(), "outside the filtered candidate set")
}
