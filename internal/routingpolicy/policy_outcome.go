package routingpolicy

import "weave-os/router/internal/router"

type PolicyOutcomeEffort struct {
	Arm      string
	Selected string
	Sent     string
	Source   string
	Mismatch bool
}

type PolicyOutcome struct {
	Route                    *router.RoutingMetadata
	Selected                 router.Decision
	ServedModel              string
	ServedProvider           string
	OrganizationID           string
	InstallationID           string
	ClientApp                string
	RolloutID                string
	CaptureMode              string
	RequestTrainingAllowed   bool
	ClassifierArtifactID     string
	ClassifierArtifactSHA256 string
	Effort                   PolicyOutcomeEffort
	Status                   int
	Err                      error
	EstimatedInputTokens     int
	InputTokens              int
	OutputTokens             int
	CacheCreationTokens      int
	CacheReadTokens          int
	RouteLatencyMs           int64
	UpstreamLatencyMs        int64
	TurnType                 string
	StickyHit                bool
}

func (o PolicyOutcome) SelectedServedModelMatch() bool {
	return o.Selected.Model == o.ServedModel
}

func (o PolicyOutcome) AuthoritativeModelMismatch() bool {
	return o.Route.AuthoritativePerTurnSelection && !o.SelectedServedModelMatch()
}

func (o PolicyOutcome) TrainingAllowed() bool {
	return o.RequestTrainingAllowed && !o.AuthoritativeModelMismatch() && !o.Effort.Mismatch
}

func (o PolicyOutcome) Payload() map[string]interface{} {
	route := o.Route
	payload := map[string]interface{}{
		"route_id":                         route.RouteID,
		"strategy":                         route.Strategy,
		"organization_id":                  o.OrganizationID,
		"installation_id":                  o.InstallationID,
		"client_app":                       o.ClientApp,
		"rollout_id":                       o.RolloutID,
		"training_allowed":                 o.TrainingAllowed(),
		"capture_mode":                     o.CaptureMode,
		"policy_route_key":                 route.PolicyRouteKey,
		"policy_artifact_id":               route.PolicyArtifactID,
		"policy_artifact_sha256":           route.PolicyArtifactSHA256,
		"roster_version":                   route.RosterVersion,
		"classifier_artifact_id":           o.ClassifierArtifactID,
		"classifier_artifact_sha256":       o.ClassifierArtifactSHA256,
		"selection_policy_release_id":      route.SelectionPolicyReleaseID,
		"selection_policy_sha256":          route.SelectionPolicySHA256,
		"selection_head_generation":        route.SelectionHeadGeneration,
		"sidecar_schema_version":           route.SidecarSchemaVersion,
		"selected_model":                   o.Selected.Model,
		"selected_provider":                o.Selected.Provider,
		"served_model":                     o.ServedModel,
		"served_provider":                  o.ServedProvider,
		"decision_model":                   o.Selected.Model,
		"decision_provider":                o.Selected.Provider,
		"selected_served_model_match":      o.SelectedServedModelMatch(),
		"arm_effort":                       o.Effort.Arm,
		"selected_effort":                  o.Effort.Selected,
		"sent_effort":                      o.Effort.Sent,
		"effort_source":                    o.Effort.Source,
		"selected_sent_effort_match":       !o.Effort.Mismatch,
		"authoritative_per_turn_selection": route.AuthoritativePerTurnSelection,
		"status":                           o.Status,
		"error":                            "",
		"estimated_input_tokens":           o.EstimatedInputTokens,
		"input_tokens":                     o.InputTokens,
		"output_tokens":                    o.OutputTokens,
		"cache_creation_tokens":            o.CacheCreationTokens,
		"cache_read_tokens":                o.CacheReadTokens,
		"route_latency_ms":                 o.RouteLatencyMs,
		"upstream_latency_ms":              o.UpstreamLatencyMs,
		"turn_type":                        o.TurnType,
		"sticky_hit":                       o.StickyHit,
	}
	switch {
	case o.AuthoritativeModelMismatch():
		payload["training_exclusion_reason"] = "selected_served_model_mismatch"
	case o.Effort.Mismatch:
		payload["training_exclusion_reason"] = "selected_sent_effort_mismatch"
	}
	if o.Err != nil {
		payload["error"] = o.Err.Error()
	}
	return payload
}
