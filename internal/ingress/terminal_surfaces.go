package ingress

import (
	"context"
	"net/http"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/requestcontext"
)

const servingReleaseHeaderLen = 12

func TerminalSurfacesHidden(ctx context.Context) bool {
	if _, active := auth.BlindExperimentFrom(ctx); active {
		return true
	}
	hide, _ := ctx.Value(requestcontext.InstallationHideTerminalSurfacesContextKey{}).(bool)
	return hide
}

func ServingReleaseHeadersVisible(ctx context.Context) bool {
	optedIn, _ := ctx.Value(requestcontext.InstallationServingReleaseHeadersContextKey{}).(bool)
	return optedIn && !TerminalSurfacesHidden(ctx)
}

func SetServingReleaseHeaders(ctx context.Context, h http.Header) {
	if !ServingReleaseHeadersVisible(ctx) {
		return
	}
	identity, managed := requestcontext.ServingIdentityFromContext(ctx)
	if !managed {
		return
	}
	if release := shortServingDigest(identity.ReleaseID); release != "" {
		h.Set(requestcontext.HeaderServingReleaseSHA, release)
	}
	if policySHA := shortServingDigest(identity.Recipe.PolicySHA256); policySHA != "" {
		h.Set(requestcontext.HeaderServingPolicySHA, policySHA)
	}
}

func shortServingDigest(digest string) string {
	if len(digest) < servingReleaseHeaderLen {
		return digest
	}
	return digest[:servingReleaseHeaderLen]
}
