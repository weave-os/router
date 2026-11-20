package router

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsHMMStrategy(t *testing.T) {
	assert.True(t, IsHMMStrategy(StrategyHMM))
	assert.True(t, IsHMMStrategy(StrategyHMMEmbedding))
	assert.True(t, IsHMMStrategy(StrategyHMMBeta))
	assert.False(t, IsHMMStrategy(Strategy("cluster")))
	assert.False(t, IsHMMStrategy(StrategyRL))
}

func TestStrategySelectionCarriesStrategyAndRoutingModelInOneValue(t *testing.T) {
	ctx := context.Background()
	assert.Equal(t, StrategyHMMEmbedding, StrategyFromContext(ctx), "no selection keeps the default strategy")
	assert.False(t, StrategySelectionFromContext(ctx).RoutingModelSelected())

	assert.Equal(t, ctx, WithStrategy(ctx, ""), "an empty strategy leaves the selection untouched")

	selected := WithStrategySelection(ctx, StrategySelection{Strategy: StrategyHMM, RoutingModel: ModelHMM})
	assert.Equal(t, StrategyHMM, StrategyFromContext(selected))
	assert.True(t, StrategySelectionFromContext(selected).RoutingModelSelected())

	overridden := WithStrategy(selected, StrategyHMMBeta)
	assert.Equal(t, StrategyHMMBeta, StrategyFromContext(overridden))
	assert.Equal(t, ModelHMM, StrategySelectionFromContext(overridden).RoutingModel, "a strategy override keeps the selected routing model")

	modelOnly := WithStrategySelection(ctx, StrategySelection{RoutingModel: ModelAutomatic})
	assert.Equal(t, StrategyHMMEmbedding, StrategyFromContext(modelOnly), "a routing model without a strategy keeps the default strategy")
	assert.True(t, StrategySelectionFromContext(modelOnly).RoutingModelSelected())
}
