package router

import (
	"context"

	"weave-os/router/internal/router/eligibility"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/taskdomain"
)

type WireFormat string

const (
	WireFormatAnthropic WireFormat = "anthropic_messages"
	WireFormatOpenAI    WireFormat = "openai"
	WireFormatGemini    WireFormat = "gemini"
)

type TranslationEndpoint string

const (
	EndpointAnthropicMessages TranslationEndpoint = "messages"
	EndpointOpenAIChat        TranslationEndpoint = "chat_completions"
	EndpointOpenAIResponses   TranslationEndpoint = "responses"
	EndpointGeminiGenerate    TranslationEndpoint = "generate_content"
)

type TranslationRequirements struct {
	SourceFormat WireFormat
	Endpoint     TranslationEndpoint

	FunctionTools      bool
	CustomTools        bool
	ReasoningReplay    bool
	ReasoningSignature bool
	Images             bool
	Audio              bool
	Files              bool
	CitationsOrSearch  bool
	StructuredOutput   bool
	PromptCacheControl bool
	UsageDetail        bool

	NativeOnly bool
}

func (r TranslationRequirements) IsZero() bool {
	return r.SourceFormat == "" && r.Endpoint == "" && !r.FunctionTools && !r.CustomTools &&
		!r.ReasoningReplay && !r.ReasoningSignature && !r.Images && !r.Audio && !r.Files &&
		!r.CitationsOrSearch && !r.StructuredOutput && !r.PromptCacheControl && !r.UsageDetail && !r.NativeOnly
}

type Overrides struct {
	Alpha                *float64
	QualityBias          *float64
	SpeedWeight          *float64
	OutputCostRatio      *float64
	ExpectedOutputTokens *int
	PerModelVerbosity    *bool
	ForceEffort          string
}

type Request struct {
	TaskDomain               *taskdomain.Input
	ClassifierPrediction     *ClassifierPrediction `json:"-"`
	Escalation               *escalation.Constraint
	PreviousPolicyGroup      escalation.Group
	SlidingThresholdPinGroup escalation.Group

	RequestedModel               string
	ClientBudget                 ClientBudget
	ForceModel                   string
	ForceCluster                 string
	EstimatedInputTokens         int
	OrganizationID               string
	InstallationID               string
	ClientApp                    string
	RolloutID                    string
	HasTools                     bool
	HasImages                    bool
	ImageCount                   int
	TranslationRequirements      TranslationRequirements
	ReasoningConfigurationSHA256 string
	ToolConfigurationSHA256      string
	PromptText                   string
	ConversationMessages         []ConversationMessage
	Unscorable                   bool
	// Deprecated: use Tools for structural capability decisions.
	AvailableTools                []string
	Tools                         []ToolDescriptor
	PolicyTurnContext             *PolicyTurnContext
	HistoryTruncated              bool
	FeedbackKey                   string
	FeedbackRole                  string
	ClientSessionID               string
	EnabledProviders              map[string]struct{}
	CustomBindings                map[string][]string
	GatewayProviders              map[string]struct{}
	SubscriptionProviders         map[string]struct{}
	ExcludedModels                map[string]struct{}
	AllowedModels                 map[string]struct{}
	SafetyExcludedModels          map[string]struct{}
	ContextWindowExcludedModels   map[string]struct{}
	ImageLimitExcludedModels      map[string]struct{}
	UnsignedHistoryExcludedModels map[string]struct{}
	OverflowAdmittedModels        map[string]struct{}
	AutomaticExcludedModels       map[string]struct{}
	ProductEligibility            eligibility.Boundary
	PreferredModels               []string
	RoutingIntent                 string
	RoutingKnobs                  *Overrides
	TrainingAllowed               bool
	CaptureMode                   string
	DebugEnabled                  bool
	ShadowMode                    bool
	ClusterArmOverrides           map[string][]string
}

type ToolDescriptor struct {
	Name           string
	Type           string
	ServerExecuted bool
}

type ConversationMessage struct {
	Role        string                   `json:"role"`
	Text        string                   `json:"text,omitempty"`
	ToolCalls   []ConversationToolCall   `json:"tool_calls,omitempty"`
	ToolResults []ConversationToolResult `json:"tool_results,omitempty"`
}

type ConversationToolCall struct {
	Name      string   `json:"name,omitempty"`
	InputKeys []string `json:"input_keys,omitempty"`
	InputJSON string   `json:"input_json,omitempty"`
}

type ConversationToolResult struct {
	ToolUseID     string `json:"tool_use_id,omitempty"`
	IsError       bool   `json:"is_error,omitempty"`
	Text          string `json:"text,omitempty"`
	ResultPresent bool   `json:"result_present,omitempty"`
	CharCount     int    `json:"char_count,omitempty"`
	ByteCount     int    `json:"byte_count,omitempty"`
	ExitCategory  string `json:"exit_category,omitempty"`
}

type PolicyTurnContext struct {
	VisibleTurnIndex    int
	SessionTurnCount    int
	TurnType            string
	PreviousServedModel string
	PreviousProvider    string
	CacheState          string
	PriorOutputTokens   *int
	SessionEverSwitched bool
	HistoryTruncated    bool
}

const (
	PolicyCacheStateUnknown = "unknown"
	PolicyCacheStateCold    = "cold"
	PolicyCacheStateWarm    = "warm"
)

type Decision struct {
	Provider string
	Model    string
	Effort   string
	Reason   string
	Metadata *RoutingMetadata
}

func (d Decision) ServedIdentity() string {
	if d.Effort == "" {
		return d.Model
	}
	return d.Model + ":" + d.Effort
}

type RoutingMetadata struct {
	Escalation *escalation.Decision

	Embedding                     []float32
	ClusterIDs                    []int
	CandidateModels               []string
	ScorerRescuePool              []string
	SidecarRescuePool             []string
	RescueModels                  []string
	RosterFailover                bool
	ChosenScore                   float32
	ClusterRouterVersion          string
	Strategy                      string
	RouteID                       string
	PolicyRouteKey                string
	PolicyGroup                   string
	PolicyArtifactID              string
	PolicyArtifactSHA256          string
	RosterVersion                 string
	PolicyPinHonoured             bool
	ClassifierArtifactID          string
	ClassifierArtifactSHA256      string
	ClassifierPredictedLabel      string
	ClassifierClassOrder          []string
	ClassifierProbabilities       map[string]float64
	ClassifierMargin              *float64
	SelectionPolicyReleaseID      string
	SelectionPolicySHA256         string
	SelectionHeadGeneration       int64
	SelectionTrace                *SelectionTrace
	SidecarTimings                *SidecarTimings
	SidecarStats                  *SidecarServingStats
	SelectedArmID                 string
	SelectedRosterArmID           string
	SelectedUpstreamID            string
	BindingIndex                  int
	CandidateArmIDs               []string
	SidecarSchemaVersion          string
	DebugRef                      string
	AuthoritativePerTurnSelection bool
	DisplayMarker                 string
	EffectiveKnobsHash            uint64
	CandidateScores               map[string]float32
	CandidateArmScores            map[string]float32
	CandidateProviders            map[string]string
	CandidateArmProviders         map[string]string
	Propensity                    float32
	PairedModel                   string
	PairedProvider                string
	PairedScore                   float32
	ArmScores                     map[string]float32
}

type SelectionOverrideReason string

const (
	SelectionOverrideNone             SelectionOverrideReason = ""
	SelectionOverrideSlidingThreshold SelectionOverrideReason = "sliding_threshold"
	SelectionOverrideForceCluster     SelectionOverrideReason = "force_cluster"
	SelectionOverrideCluster          SelectionOverrideReason = "cluster_override"
)

type SelectionTrace struct {
	TaskDomain              *taskdomain.Outcome                            `json:"task_domain,omitempty"`
	ClassifierRanking       []string                                       `json:"classifier_ranking"`
	Harness                 string                                         `json:"harness"`
	ForcedGroup             string                                         `json:"forced_group,omitempty"`
	CandidateRosterIDs      []string                                       `json:"candidate_roster_ids"`
	QualityBias             *float64                                       `json:"quality_bias,omitempty"`
	PreferredModels         []string                                       `json:"preferred_models,omitempty"`
	SubscriptionProviders   []string                                       `json:"subscription_providers,omitempty"`
	EffectiveOrders         map[string][]string                            `json:"effective_orders"`
	ScoresByGroup           map[string]map[string]float32                  `json:"scores_by_group,omitempty"`
	ScoreComponentsByGroup  map[string]map[string]SelectionScoreComponents `json:"score_components_by_group,omitempty"`
	ResolverExclusions      []SelectionExclusion                           `json:"resolver_exclusions,omitempty"`
	SelectedGroup           string                                         `json:"selected_group"`
	SelectedArm             string                                         `json:"selected_arm"`
	FallbackDepth           int                                            `json:"fallback_depth"`
	OverrideReason          SelectionOverrideReason                        `json:"override_reason,omitempty"`
	ClusterOverridesPresent bool                                           `json:"cluster_overrides_present,omitempty"`
}

type SelectionScoreComponents struct {
	BaseScore            float32 `json:"base_score"`
	TaskDomainCorrection float32 `json:"task_domain_correction,omitempty"`
	PreferredModelBonus  float32 `json:"preferred_model_bonus,omitempty"`
	TotalScore           float32 `json:"total_score"`
}

type SelectionExclusion struct {
	CatalogID string `json:"catalog_id"`
	RosterID  string `json:"roster_id,omitempty"`
	Reason    string `json:"reason"`
}

type SidecarTimings struct {
	EmbedMs  *float64
	SelectMs *float64
	OtherMs  *float64
}

type SidecarServingStats struct {
	EmbedCacheHits      *int64
	EmbedCacheMisses    *int64
	EmbedCacheEvictions *int64
	RoutesInflight      *int64
	OverrunsLive        *int64
}

type Router interface {
	Route(ctx context.Context, req Request) (Decision, error)
}

type routingKnobsContextKey struct{}

func WithRoutingKnobs(ctx context.Context, o *Overrides) context.Context {
	if o == nil {
		return ctx
	}
	return context.WithValue(ctx, routingKnobsContextKey{}, o)
}

func RoutingKnobsFromContext(ctx context.Context) *Overrides {
	o, _ := ctx.Value(routingKnobsContextKey{}).(*Overrides)
	return o
}
