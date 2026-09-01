package sessionstrategy_test

import (
	"errors"
	"testing"

	"workweave/router/internal/router"
	"workweave/router/internal/router/sessionstrategy"

	"github.com/stretchr/testify/assert"
)

func TestPreferenceValidateAcceptsOnlyHMMBeta(t *testing.T) {
	t.Parallel()

	assert.NoError(t, (sessionstrategy.Preference{Strategy: router.StrategyHMMBeta}).Validate())
	for _, strategy := range []router.Strategy{"", "stable", router.StrategyHMM, router.StrategyCluster} {
		assert.ErrorIs(t, (sessionstrategy.Preference{Strategy: strategy}).Validate(), sessionstrategy.ErrInvalidStrategy)
	}
}

func TestInvalidStrategyErrorIsStable(t *testing.T) {
	t.Parallel()

	err := (sessionstrategy.Preference{Strategy: "stable"}).Validate()
	assert.True(t, errors.Is(err, sessionstrategy.ErrInvalidStrategy))
}
