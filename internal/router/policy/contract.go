package policy

import (
	"context"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/taskdomain"
)

const SchemaVersionV1 = "policy_router_v1"

const SchemaVersionV2 = "policy_router_v2"

const SchemaVersionV3 = "policy_router_v3"

const SchemaVersionV4 = "policy_router_v4"

const SchemaVersionV5 = "policy_router_v5"

type ClassifierAPIMajor int

const (
	ClassifierAPIMajorV4 ClassifierAPIMajor = 4
	ClassifierAPIMajorV5 ClassifierAPIMajor = 5
)

func SupportedClassifierAPIMajors() []ClassifierAPIMajor {
	return []ClassifierAPIMajor{ClassifierAPIMajorV4, ClassifierAPIMajorV5}
}

type ClassifierVariantID string

const (
	ClassifierVariantPrimary ClassifierVariantID = "primary"
	ClassifierVariantHMMBeta ClassifierVariantID = "hmm_beta"
	ClassifierVariantQwen    ClassifierVariantID = "qwen"
)

type ClassifierArtifact struct {
	ArtifactID      string `json:"artifact_id"`
	PackageSHA256   string `json:"package_sha256"`
	Endpoint        string `json:"endpoint,omitempty"`
	Audience        string `json:"audience,omitempty"`
	ServingRevision string `json:"serving_revision,omitempty"`
}

const (
	ExecutionModeServing = "serving"
	ExecutionModeShadow  = "shadow"
	ExecutionModePreview = "preview"
)

type Capabilities struct {
	SchemaVersion                  string   `json:"schema_version"`
	ReportsOutcomes                bool     `json:"reports_outcomes"`
	ReportsFeedback                bool     `json:"reports_feedback"`
	HonorsPreferredModels          bool     `json:"honors_preferred_models"`
	HonorsQualityPriceBias         bool     `json:"honors_quality_price_bias"`
	SupportsDebugRouteDetail       bool     `json:"supports_debug_route_detail"`
	SupportsPreview                bool     `json:"supports_preview"`
	SupportsRoutingDistribution    bool     `json:"supports_routing_distribution"`
	SupportsShadow                 bool     `json:"supports_shadow"`
	ReportsRankedFallback          bool     `json:"reports_ranked_fallback"`
	HonorsClusterModelLists        bool     `json:"honors_cluster_model_lists"`
	AuthoritativePerTurnSelection  bool     `json:"authoritative_per_turn_selection"`
	InteractionStatePackageSHA256s []string `json:"interaction_state_package_sha256s,omitempty"`
}

type CapabilitySource interface {
	CurrentCapabilities() Capabilities
}

type RequestCapabilitySource interface {
	CapabilitiesForRequest(context.Context) Capabilities
}

type AvailabilitySource interface {
	Available() bool
}

type StrategySpec struct {
	Strategy     router.Strategy
	Router       router.Router
	Unavailable  error
	Capabilities Capabilities
}

type Query struct {
	TaskDomain               *taskdomain.Input
	ClassifierPrediction     *router.ClassifierPrediction
	SchemaVersion            string
	Strategy                 router.Strategy
	ExecutionMode            string
	RouteID                  string
	OrganizationID           string
	InstallationID           string
	ClientApp                string
	RolloutID                string
	RequestedModel           string
	PromptText               string
	ConversationMessages     []router.ConversationMessage
	AvailableTools           []string
	Tools                    []router.ToolDescriptor
	FeedbackKey              string
	FeedbackRole             string
	ClientSessionID          string
	TurnContext              *router.PolicyTurnContext
	EstimatedInputTokens     int
	HasTools                 bool
	HasImages                bool
	RoutingIntent            string
	PreferredModels          []string
	RoutingKnobs             *router.Overrides
	TrainingAllowed          bool
	CaptureMode              string
	DebugEnabled             bool
	Candidates               []Candidate
	ArtifactSHA256           string
	ClassifierReleaseID      string
	ClassifierSelectionID    string
	ClassifierVariantID      ClassifierVariantID
	ClassifierArtifactSHA256 string
	InteractionStateInputs   bool
}

type Result struct {
	TaskDomain               *taskdomain.Outcome
	SchemaVersion            string
	RouteID                  string
	ArmID                    string
	Model                    string
	Provider                 string
	Score                    float64
	CandidateScores          map[string]float32
	ScoreKind                string
	Reason                   string
	PolicyState              string
	PolicyGroup              string
	PolicyLabel              string
	PolicyRouteKey           string
	Confidence               *float64
	Margin                   *float64
	Propensity               float64
	DisplayMarker            string
	PolicyArtifactID         string
	PolicyArtifactSHA256     string
	ClassifierReleaseID      string
	ClassifierSelectionID    string
	ClassifierVariantID      ClassifierVariantID
	ClassifierTaxonomySHA256 string
	Timings                  *router.SidecarTimings
	ServingStats             *router.SidecarServingStats
	RosterVersion            string
	DebugRef                 string
	Debug                    map[string]interface{}
	ArmScores                map[string]float32
	RankedFallback           []PreviewGroup
	PredictedLabel           string
	ClassProbabilities       map[string]float64
	ClassOrder               []string
	HMMStateID               int
	HMMStatePath             []int
	HMMStateProbabilities    []float64
}

type PreviewGroup struct {
	Group        string   `json:"group"`
	Probability  float64  `json:"probability"`
	RosterArms   []string `json:"roster_arms"`
	EligibleArms []string `json:"eligible_arms"`
}

type PreviewResult struct {
	SchemaVersion            string              `json:"schema_version"`
	RouteID                  string              `json:"route_id"`
	Strategy                 router.Strategy     `json:"strategy"`
	PolicyArtifactID         string              `json:"policy_artifact_id"`
	PolicyArtifactSHA256     string              `json:"policy_artifact_sha256"`
	ClassifierReleaseID      string              `json:"classifier_release_id,omitempty"`
	ClassifierSelectionID    string              `json:"classifier_selection_id,omitempty"`
	ClassifierVariantID      ClassifierVariantID `json:"classifier_variant_id,omitempty"`
	ClassifierTaxonomySHA256 string              `json:"classifier_taxonomy_sha256,omitempty"`
	RosterSHA256             string              `json:"roster_sha256"`
	HMMStateID               int                 `json:"hmm_state_id"`
	HMMStatePath             []int               `json:"hmm_state_path"`
	HMMStateProbabilities    []float64           `json:"hmm_state_probabilities"`
	ClassOrder               []string            `json:"class_order"`
	ClassProbabilities       map[string]float64  `json:"class_probabilities"`
	PredictedLabel           string              `json:"predicted_label"`
	RankedFallback           []PreviewGroup      `json:"ranked_fallback"`
	SelectedGroup            string              `json:"selected_group,omitempty"`
	EligibleRosterIDs        []string            `json:"eligible_roster_ids"`
	ResolverCandidates       []Candidate         `json:"resolver_candidates"`
	ResolverExclusions       []Diagnostic        `json:"resolver_exclusions"`
}

type RosterSnapshot struct {
	SchemaVersion            string                         `json:"schema_version"`
	ReleaseID                string                         `json:"release_id"`
	PolicySHA256             string                         `json:"policy_sha256"`
	Environment              string                         `json:"environment"`
	Lane                     string                         `json:"lane"`
	HeadGeneration           int64                          `json:"head_generation"`
	LatestObservedGeneration int64                          `json:"latest_observed_generation"`
	RejectedGeneration       int64                          `json:"rejected_generation,omitempty"`
	LastRejectionReason      string                         `json:"last_rejection_reason,omitempty"`
	Clusters                 map[string][]string            `json:"clusters"`
	Harnesses                map[string]map[string][]string `json:"harnesses,omitempty"`
	RosterSHA256             string                         `json:"roster_sha256"`
}

type RosterSource interface {
	ClusterRoster(ctx context.Context) (RosterSnapshot, error)
}

type Decider interface {
	Decide(ctx context.Context, query Query) (Result, error)
}

type PreviewDecider interface {
	Preview(ctx context.Context, query Query) (PreviewResult, error)
}

type RoutePreviewer interface {
	PreviewRoute(ctx context.Context, req router.Request) (PreviewResult, error)
}

type OutcomeReporter interface {
	ReportOutcome(ctx context.Context, payload map[string]interface{}) error
}

type FeedbackReporter interface {
	ReportFeedback(ctx context.Context, payload map[string]interface{}) error
}

const ClassifierTimeoutHeader = "X-Weave-Classifier-Timeout-Ms"

const MaxClassifierTimeoutMilliseconds = 600_000
