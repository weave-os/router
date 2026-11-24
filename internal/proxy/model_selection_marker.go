package proxy

import (
	"context"
	"strings"
	"weave-os/router/internal/routingpolicy"

	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/turntype"
)

func modelSelectionMarkerForRequest(ports continuity.StrategyPorts, ctx context.Context, res routingpolicy.TurnRoute, marker, servedModel, failoverReason string) string {
	if res.TurnType == turntype.SubAgentDispatch {
		return ""
	}
	show, _ := ctx.Value(requestcontext.InstallationShowModelSelectionReasoningContextKey{}).(bool)
	if !show || marker == "" || servedModel == "" || res.SuggestionMode || res.HardPinned ||
		isUnpinnedScoredTurn(res.TurnType) || candidates.BaseModelOf(res.PriorServedModel) == servedModel {
		return marker
	}
	lineEnd := strings.IndexByte(marker, '\n')
	if lineEnd == -1 {
		return marker
	}
	reason := modelSelectionReason(ports, res, failoverReason)
	if strings.Contains(marker[:lineEnd], " · "+reason) {
		return marker
	}
	return marker[:lineEnd] + " · " + reason + marker[lineEnd:]
}
