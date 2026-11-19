package hmm_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm"
)

func TestClassifierTelemetryOfCopiesClassifierFields(t *testing.T) {
	margin := 0.4
	md := &router.RoutingMetadata{
		PolicyGroup: "g2", ClassifierMargin: &margin, ClassifierArtifactID: "a", ClassifierArtifactSHA256: "s",
		ClassifierPredictedLabel: "g3", ClassifierClassOrder: []string{"g1"}, ClassifierProbabilities: map[string]float64{"g1": 1},
		SelectionTrace: &router.SelectionTrace{},
	}
	got := hmm.ClassifierTelemetryOf(md)
	assert.Equal(t, hmm.ClassifierTelemetry{
		Margin: &margin, PolicyGroup: "g2", ArtifactID: "a", ArtifactSHA256: "s", PredictedLabel: "g3",
		ClassOrder: []string{"g1"}, Probabilities: map[string]float64{"g1": 1}, SelectionTrace: &router.SelectionTrace{},
	}, got)
	assert.Equal(t, hmm.ClassifierTelemetry{}, hmm.ClassifierTelemetryOf(nil))
}
