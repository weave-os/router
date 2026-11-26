package telemetry

import "context"

type PolicyShadowStore interface {
	InsertPolicyShadowDecision(ctx context.Context, event PolicyShadowDecision) error
}

type PolicyShadowDecision struct {
	InstallationID  string
	OrganizationID  string
	RolloutID       string
	ClientApp       string
	TrainingAllowed bool

	ServingTarget    string
	RequestedModel   string
	ServingReleaseID string

	ServingStrategy             string
	ServingModel                string
	ServingProvider             string
	ServingRouteID              string
	ServingPolicyRouteKey       string
	ServingPolicyArtifactID     string
	ServingPolicyArtifactSHA256 string

	ShadowStrategy             string
	ShadowModel                string
	ShadowProvider             string
	ShadowRouteID              string
	ShadowPolicyRouteKey       string
	ShadowPolicyArtifactID     string
	ShadowPolicyArtifactSHA256 string
	ShadowLatencyMs            int64
	ShadowError                string
	ModelsAgree                bool

	ShadowRoutingModel       string
	ServingProfileKey        string
	ShadowProfileKey         string
	ClassifierPredictedLabel string
	ServingPolicyGroup       string
	ShadowPolicyGroup        string
	ServingRosterArmID       string
	ShadowRosterArmID        string
}
