package sessionstrategy_test

import (
	"errors"
	"testing"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionstrategy"

	"github.com/stretchr/testify/assert"
)

func TestPreferenceValidateAcceptsBetaClassifiers(t *testing.T) {
	t.Parallel()

	assert.NoError(t, (sessionstrategy.Preference{Strategy: router.StrategyHMMBeta}).Validate())
	assert.NoError(t, (sessionstrategy.Preference{Strategy: router.StrategyHMMQwen}).Validate())
	for _, strategy := range []router.Strategy{"", "stable", router.StrategyHMM, router.StrategyCluster} {
		assert.ErrorIs(t, (sessionstrategy.Preference{Strategy: strategy}).Validate(), sessionstrategy.ErrInvalidStrategy)
	}
}

func TestInvalidStrategyErrorIsStable(t *testing.T) {
	t.Parallel()

	err := (sessionstrategy.Preference{Strategy: "stable"}).Validate()
	assert.True(t, errors.Is(err, sessionstrategy.ErrInvalidStrategy))
}
