package router

import (
	"slices"
	"time"

	"weave-os/router/internal/router/escalation"
)

// SiblingPreference remembers a successful replacement within one policy group.
// Displaced models remain eligible as a last resort when the replacement fails.
type SiblingPreference struct {
	Model           string    `json:"model"`
	DisplacedModels []string  `json:"displaced_models"`
	StartedAt       time.Time `json:"started_at"`
}

// SiblingPreferenceFromSuccess decides whether a served sibling becomes the
// group's remembered preference and which failed siblings it displaced. The
// served model must be a member of the decision's sibling set inside a ranked
// group, and at least one recorded sibling failure must have been displaced.
func SiblingPreferenceFromSuccess(metadata *RoutingMetadata, servedModel string, failedSiblings []string) (escalation.Group, []string, bool) {
	if metadata == nil || !slices.Contains(metadata.SiblingModels, servedModel) {
		return "", nil, false
	}
	group := escalation.Group(metadata.PolicyGroup)
	if escalation.Rank(group) < 0 {
		return "", nil, false
	}
	var displaced []string
	for _, model := range failedSiblings {
		if model != servedModel && slices.Contains(metadata.SiblingModels, model) {
			displaced = append(displaced, model)
		}
	}
	if len(displaced) == 0 {
		return "", nil, false
	}
	return group, displaced, true
}
