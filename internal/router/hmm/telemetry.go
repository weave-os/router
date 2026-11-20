package hmm

import (
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/continuity"
)

type ClassifierTelemetry = continuity.ClassifierTelemetry

func ClassifierTelemetryOf(md *router.RoutingMetadata) ClassifierTelemetry {
	if md == nil {
		return ClassifierTelemetry{}
	}
	return ClassifierTelemetry{
		Margin:         md.ClassifierMargin,
		PolicyGroup:    md.PolicyGroup,
		ArtifactID:     md.ClassifierArtifactID,
		ArtifactSHA256: md.ClassifierArtifactSHA256,
		PredictedLabel: md.ClassifierPredictedLabel,
		ClassOrder:     md.ClassifierClassOrder,
		Probabilities:  md.ClassifierProbabilities,
		SelectionTrace: md.SelectionTrace,
	}
}
