package proxy

import (
	"context"
	"testing"

	"workweave/router/internal/router"
	"workweave/router/internal/router/sessionpin"
	"workweave/router/internal/translate"

	"github.com/stretchr/testify/assert"
)

func TestPinMatchesEffectiveStrategy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		request  router.Strategy
		stored   router.Strategy
		expected bool
	}{
		{name: "stable exact", request: router.StrategyCluster, stored: router.StrategyCluster, expected: true},
		{name: "beta exact", request: router.StrategyHMMBeta, stored: router.StrategyHMMBeta, expected: true},
		{name: "beta rejects stable", request: router.StrategyHMMBeta, stored: router.StrategyCluster, expected: false},
		{name: "stable rejects beta", request: router.StrategyCluster, stored: router.StrategyHMMBeta, expected: false},
		{name: "other HMM rejects beta", request: router.StrategyHMM, stored: router.StrategyHMMBeta, expected: false},
		{name: "stable accepts legacy during rollout", request: router.StrategyCluster, stored: "", expected: true},
		{name: "non-beta opt-in accepts legacy during rollout", request: router.StrategyHMM, stored: "", expected: true},
		{name: "beta rejects legacy", request: router.StrategyHMMBeta, stored: "", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := router.WithStrategy(context.Background(), tt.request)
			assert.Equal(t, tt.expected, pinMatchesEffectiveStrategy(ctx, sessionpin.Pin{Strategy: tt.stored}))
		})
	}

	betaContext := router.WithStrategy(context.Background(), router.StrategyHMMBeta)
	assert.True(t, pinMatchesEffectiveStrategy(betaContext, sessionpin.Pin{
		Reason:   translate.ReasonUserForceModel,
		Strategy: router.StrategyCluster,
	}), "an explicit force must survive routing-strategy changes")
}
