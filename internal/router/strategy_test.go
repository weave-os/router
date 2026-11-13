package router

import (
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

func TestIsRetiredStrategy(t *testing.T) {
	assert.True(t, IsRetiredStrategy("cluster"))
	for _, strategy := range []Strategy{"", StrategyHMM, StrategyHMMEmbedding, StrategyHMMBeta, StrategyRL, "Cluster"} {
		assert.False(t, IsRetiredStrategy(strategy), strategy)
	}
}
