package proxy

import (
	"context"
	"net/http"
	"testing"

	"weave-os/router/internal/auth"

	"github.com/stretchr/testify/assert"
)

func blindExperimentCtx(arm auth.BlindExperimentArm) context.Context {
	return context.WithValue(context.Background(), auth.BlindExperimentContextKey{}, auth.BlindExperimentState{Active: true, Arm: arm})
}

func TestRoutingMarkerHiddenForBlindExperimentSubjects(t *testing.T) {
	const marker = "✦ **Weave Router** → deepseek/deepseek-v4-flash · best pick for this turn\n\n"
	for _, arm := range []auth.BlindExperimentArm{auth.BlindExperimentArmRouterOn, auth.BlindExperimentArmPassthrough} {
		assert.Empty(t, suppressMarkerIfRequested(blindExperimentCtx(arm), http.Header{}, marker), "arm %s", arm)
	}

	inactive := context.WithValue(context.Background(), auth.BlindExperimentContextKey{}, auth.BlindExperimentState{Arm: auth.BlindExperimentArmRouterOn})
	assert.Equal(t, marker, suppressMarkerIfRequested(inactive, http.Header{}, marker))
}
