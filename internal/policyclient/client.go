package policyclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/taskdomain"
)

const DefaultTimeout = 4500 * time.Millisecond

const (
	defaultRouteAttempts = 3
	routeRetryBackoff    = 50 * time.Millisecond
)

const (
	defaultAttemptFraction = 0.4
	minAttemptTimeout      = 500 * time.Millisecond
	sidecarInferenceFloor  = 1800 * time.Millisecond
)

type Option func(*Client)

func WithAttemptTimeout(timeout time.Duration) Option {
	return func(c *Client) { c.attemptTimeout = timeout }
}

func DeriveAttemptTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	attemptTimeout := time.Duration(float64(timeout) * defaultAttemptFraction)
	if attemptTimeout < sidecarInferenceFloor && timeout >= sidecarInferenceFloor+minAttemptTimeout {
		attemptTimeout = sidecarInferenceFloor
	}
	if attemptTimeout < minAttemptTimeout {
		attemptTimeout = minAttemptTimeout
	}
	if attemptTimeout > timeout {
		attemptTimeout = timeout
	}
	return attemptTimeout
}

const (
	maxRouteMessages                       = 96
	maxRouteMessageTextChars               = 3000
	maxRouteMessageTotalChars              = 48000
	maxRouteTools                          = 96
	maxRouteToolCallInputKeys              = 24
	maxRouteToolCallInputChars             = 80
	interactionStateRouteMessageTextChars  = 8000
	interactionStateRouteMessageTotalChars = 128000
)

type Client struct {
	taskDomain           taskdomain.Resolver
	baseURL              string
	client               *http.Client
	timeout              time.Duration
	attemptTimeout       time.Duration
	expectedPolicySHA256 string
}

func validClassifierSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func taxonomyDigest(classOrder []string) string {
	encoded, _ := json.Marshal(classOrder)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func decodeStrictJSON(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON response contains trailing data")
		}
		return err
	}
	return nil
}

func New(baseURL string, client *http.Client, timeout time.Duration, opts ...Option) *Client {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	client = withSidecarTransport(client, timeout)
	client = observability.WrapHTTPClient(client)
	sidecar := &Client{
		baseURL:        strings.TrimRight(baseURL, "/"),
		client:         client,
		timeout:        timeout,
		attemptTimeout: DeriveAttemptTimeout(timeout),
	}
	for _, opt := range opts {
		opt(sidecar)
	}
	return sidecar
}

type ClassifierHealth struct {
	SchemaVersion         string                                        `json:"schema_version"`
	ClassifierArtifactID  string                                        `json:"classifier_artifact_id"`
	ClassifierSHA256      string                                        `json:"classifier_artifact_sha256"`
	ClassifierImageDigest string                                        `json:"classifier_image_digest"`
	ClassifierClassOrder  []string                                      `json:"classifier_class_order"`
	ClassifierTaxonomySHA string                                        `json:"classifier_taxonomy_sha256"`
	ClassifierVariants    map[router.Strategy]policy.ClassifierArtifact `json:"classifier_variants,omitempty"`
}

type ClassifierCapabilitiesV5 struct {
	SchemaVersion         string                       `json:"schema_version"`
	APIMajor              policy.ClassifierAPIMajor    `json:"api_major"`
	ClassifierSelectionID string                       `json:"classifier_selection_id"`
	ClassifierReleaseID   string                       `json:"classifier_release_id"`
	WireSchema            string                       `json:"wire_schema"`
	ClassOrder            []string                     `json:"class_order"`
	TaxonomySHA256        string                       `json:"classifier_taxonomy_sha256"`
	ClassifierVariants    []policy.ClassifierVariantID `json:"classifier_variants"`
	SupportsPreview       bool                         `json:"supports_preview"`
	ReportsOutcomes       bool                         `json:"-"`
	ReportsFeedback       bool                         `json:"-"`
}

type classifierResponseV5 struct {
	SchemaVersion            string                     `json:"schema_version"`
	RequestID                string                     `json:"request_id"`
	ClassifierSelectionID    string                     `json:"classifier_selection_id"`
	ClassifierReleaseID      string                     `json:"classifier_release_id"`
	ClassifierVariantID      policy.ClassifierVariantID `json:"classifier_variant_id"`
	ClassifierArtifactID     string                     `json:"classifier_artifact_id"`
	ClassifierArtifactSHA256 string                     `json:"classifier_artifact_sha256"`
	ClassifierTaxonomySHA256 string                     `json:"classifier_taxonomy_sha256"`
	PredictedLabel           string                     `json:"predicted_label"`
	ClassOrder               []string                   `json:"class_order"`
	ClassProbabilities       map[string]float64         `json:"class_probabilities"`
	ClassifierConfidence     float64                    `json:"classifier_confidence"`
	ClassifierMargin         float64                    `json:"classifier_margin"`
	HMMStateID               *int                       `json:"hmm_state_id"`
	HMMStatePath             []int                      `json:"hmm_state_path"`
	HMMStateProbabilities    []float64                  `json:"hmm_state_probabilities"`
	Error                    string                     `json:"error"`
	Detail                   json.RawMessage            `json:"detail"`
}

func (c *Client) ReadClassifierHealth(ctx context.Context) (ClassifierHealth, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/readyz", nil)
	if err != nil {
		return ClassifierHealth{}, fmt.Errorf("build policy readiness request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return ClassifierHealth{}, fmt.Errorf("call policy readiness endpoint: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ClassifierHealth{}, fmt.Errorf("read policy readiness response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return ClassifierHealth{}, fmt.Errorf("policy readiness status %d", resp.StatusCode)
	}
	var health ClassifierHealth
	if err := json.Unmarshal(payload, &health); err != nil {
		return ClassifierHealth{}, fmt.Errorf("decode policy readiness response: %w", err)
	}
	if health.SchemaVersion != policy.SchemaVersionV4 || health.ClassifierArtifactID == "" || health.ClassifierSHA256 == "" || health.ClassifierImageDigest == "" || len(health.ClassifierClassOrder) == 0 || health.ClassifierTaxonomySHA == "" {
		return ClassifierHealth{}, errors.New("policy readiness response has incomplete classifier identity")
	}
	return health, nil
}

func (c *Client) CheckHealth(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/readyz", nil)
	if err != nil {
		return fmt.Errorf("build policy readiness request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("call policy readiness endpoint: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("policy readiness status %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) ReportOutcome(ctx context.Context, payload map[string]interface{}) error {
	return c.post(ctx, "/outcome", payload, "outcome")
}

func (c *Client) ReportFeedback(ctx context.Context, payload map[string]interface{}) error {
	return c.post(ctx, "/feedback", payload, "feedback")
}

func (c *Client) Capabilities(ctx context.Context) (policy.Capabilities, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/capabilities", nil)
	if err != nil {
		return policy.Capabilities{}, fmt.Errorf("build policy capabilities request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return policy.Capabilities{}, fmt.Errorf("call policy capabilities endpoint: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return policy.Capabilities{}, fmt.Errorf("read policy capabilities response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return policy.Capabilities{}, fmt.Errorf("policy capabilities status %d", resp.StatusCode)
	}
	var capabilities policy.Capabilities
	if err := json.Unmarshal(payload, &capabilities); err != nil {
		return policy.Capabilities{}, fmt.Errorf("decode policy capabilities response: %w", err)
	}
	if !supportedSchema(capabilities.SchemaVersion) {
		return policy.Capabilities{}, fmt.Errorf("unsupported policy capabilities schema %q", capabilities.SchemaVersion)
	}
	return capabilities, nil
}

func (c *Client) CapabilitiesForClassifierRelease(ctx context.Context, selectionID, releaseID string) (ClassifierCapabilitiesV5, error) {
	if c == nil {
		return ClassifierCapabilitiesV5{}, errors.New("policy client is required")
	}
	if !validClassifierSHA256(selectionID) || !validClassifierSHA256(releaseID) {
		return ClassifierCapabilitiesV5{}, errors.New("classifier capabilities require exact selection and release digests")
	}
	endpoint := c.baseURL + "/v5/capabilities?classifier_selection_id=" + selectionID + "&classifier_release_id=" + releaseID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ClassifierCapabilitiesV5{}, fmt.Errorf("build v5 classifier capabilities request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return ClassifierCapabilitiesV5{}, fmt.Errorf("call v5 classifier capabilities endpoint: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ClassifierCapabilitiesV5{}, fmt.Errorf("read v5 classifier capabilities response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return ClassifierCapabilitiesV5{}, fmt.Errorf("v5 classifier capabilities status %d", resp.StatusCode)
	}
	var capabilities ClassifierCapabilitiesV5
	if err := decodeStrictJSON(payload, &capabilities); err != nil {
		return ClassifierCapabilitiesV5{}, fmt.Errorf("decode v5 classifier capabilities: %w", err)
	}
	if capabilities.SchemaVersion != policy.SchemaVersionV5 || capabilities.APIMajor != policy.ClassifierAPIMajorV5 ||
		capabilities.WireSchema != policy.SchemaVersionV5 || capabilities.ClassifierSelectionID != selectionID ||
		capabilities.ClassifierReleaseID != releaseID || len(capabilities.ClassOrder) == 0 ||
		!validClassifierSHA256(capabilities.TaxonomySHA256) || taxonomyDigest(capabilities.ClassOrder) != capabilities.TaxonomySHA256 ||
		!slices.Contains(capabilities.ClassifierVariants, policy.ClassifierVariantPrimary) {
		return ClassifierCapabilitiesV5{}, errors.New("v5 classifier capabilities do not match the selected release")
	}
	return capabilities, nil
}

func (c *Client) post(ctx context.Context, path string, payload map[string]interface{}, label string) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal policy %s request: %w", label, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build policy %s request: %w", label, err)
	}
	req.Header.Set("content-type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("call policy %s endpoint: %w", label, err)
	}
	defer func() { _ = resp.Body.Close() }()
	payloadBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return fmt.Errorf("read policy %s response: %w", label, readErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var parsed struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(payloadBytes, &parsed)
		if parsed.Error != "" {
			return fmt.Errorf("policy %s status %d: %s", label, resp.StatusCode, parsed.Error)
		}
		return fmt.Errorf("policy %s status %d", label, resp.StatusCode)
	}
	return nil
}

type routeRequest struct {
	SchemaVersion             string            `json:"schema_version"`
	ArtifactSHA256            string            `json:"artifact_sha256,omitempty"`
	Strategy                  string            `json:"strategy"`
	ExecutionMode             string            `json:"execution_mode"`
	RouteID                   string            `json:"route_id"`
	OrganizationID            string            `json:"organization_id,omitempty"`
	InstallationID            string            `json:"installation_id,omitempty"`
	ClientApp                 string            `json:"client_app,omitempty"`
	Harness                   string            `json:"harness"`
	RolloutID                 string            `json:"rollout_id,omitempty"`
	RequestedModel            string            `json:"requested_model,omitempty"`
	PromptText                string            `json:"prompt_text"`
	LatestUserText            string            `json:"latest_user_text"`
	TurnIndex                 int               `json:"turn_index"`
	IsSubagent                bool              `json:"is_subagent"`
	VisibleTurnIndex          *int              `json:"visible_turn_index,omitempty"`
	SessionTurnCount          *int              `json:"session_turn_count,omitempty"`
	TurnType                  string            `json:"turn_type,omitempty"`
	PreviousServedModel       string            `json:"previous_served_model,omitempty"`
	PreviousProvider          string            `json:"previous_provider,omitempty"`
	CacheState                string            `json:"cache_state,omitempty"`
	PriorOutputTokens         *int              `json:"prior_output_tokens,omitempty"`
	SessionEverSwitched       *bool             `json:"session_ever_switched,omitempty"`
	HistoryTruncated          *bool             `json:"history_truncated,omitempty"`
	ConversationMessages      []routeMessage    `json:"conversation_messages,omitempty"`
	TrainingConversationDelta []routeMessage    `json:"training_conversation_delta,omitempty"`
	AvailableTools            []string          `json:"available_tools,omitempty"`
	InvokedTools              []string          `json:"invoked_tools,omitempty"`
	Tools                     []routeTool       `json:"tools,omitempty"`
	FeedbackKey               string            `json:"feedback_key,omitempty"`
	FeedbackRole              string            `json:"feedback_role,omitempty"`
	ClientSessionID           string            `json:"client_session_id,omitempty"`
	EstimatedInputTokens      int               `json:"estimated_input_tokens"`
	HasTools                  bool              `json:"has_tools"`
	HasImages                 bool              `json:"has_images"`
	RoutingIntent             string            `json:"routing_intent,omitempty"`
	PreferredModels           []string          `json:"preferred_models,omitempty"`
	RoutingKnobs              *routingKnobs     `json:"routing_knobs,omitempty"`
	QualityBias               *float64          `json:"quality_bias,omitempty"`
	TrainingAllowed           bool              `json:"training_allowed"`
	CaptureMode               string            `json:"capture_mode,omitempty"`
	DebugEnabled              bool              `json:"debug_enabled"`
	Candidates                []routeCandidate  `json:"candidates"`
	CandidateModels           []string          `json:"candidate_models"`
	CandidateProviders        map[string]string `json:"candidate_providers"`
}

type classifierRequestV4 struct {
	SchemaVersion             string         `json:"schema_version"`
	Strategy                  string         `json:"strategy"`
	ExecutionMode             string         `json:"execution_mode"`
	RouteID                   string         `json:"route_id"`
	OrganizationID            string         `json:"organization_id,omitempty"`
	InstallationID            string         `json:"installation_id,omitempty"`
	ClientApp                 string         `json:"client_app,omitempty"`
	Harness                   string         `json:"harness"`
	RolloutID                 string         `json:"rollout_id,omitempty"`
	PromptText                string         `json:"prompt_text"`
	LatestUserText            string         `json:"latest_user_text"`
	TurnIndex                 int            `json:"turn_index"`
	IsSubagent                bool           `json:"is_subagent"`
	VisibleTurnIndex          *int           `json:"visible_turn_index,omitempty"`
	SessionTurnCount          *int           `json:"session_turn_count,omitempty"`
	TurnType                  string         `json:"turn_type,omitempty"`
	CacheState                string         `json:"cache_state,omitempty"`
	PriorOutputTokens         *int           `json:"prior_output_tokens,omitempty"`
	SessionEverSwitched       *bool          `json:"session_ever_switched,omitempty"`
	HistoryTruncated          *bool          `json:"history_truncated,omitempty"`
	ConversationMessages      []routeMessage `json:"conversation_messages,omitempty"`
	TrainingConversationDelta []routeMessage `json:"training_conversation_delta,omitempty"`
	AvailableTools            []string       `json:"available_tools,omitempty"`
	InvokedTools              []string       `json:"invoked_tools,omitempty"`
	Tools                     []routeTool    `json:"tools,omitempty"`
	FeedbackKey               string         `json:"feedback_key,omitempty"`
	FeedbackRole              string         `json:"feedback_role,omitempty"`
	ClientSessionID           string         `json:"client_session_id,omitempty"`
	EstimatedInputTokens      int            `json:"estimated_input_tokens"`
	HasTools                  bool           `json:"has_tools"`
	HasImages                 bool           `json:"has_images"`
	TrainingAllowed           bool           `json:"training_allowed"`
	CaptureMode               string         `json:"capture_mode,omitempty"`
	DebugEnabled              bool           `json:"debug_enabled"`
	OmittedUserTurnCount      *int           `json:"omitted_user_turn_count,omitempty"`
}

type routeCandidate struct {
	RosterID                  string                       `json:"roster_id"`
	CatalogID                 string                       `json:"catalog_id"`
	Provider                  string                       `json:"provider"`
	UpstreamID                string                       `json:"upstream_id"`
	PreferenceRank            *int                         `json:"preference_rank,omitempty"`
	InputUSDPer1M             float64                      `json:"input_usd_per_1m"`
	OutputUSDPer1M            float64                      `json:"output_usd_per_1m"`
	EstimatedCostUSD          float64                      `json:"estimated_cost_usd"`
	CacheReadMultiplier       float64                      `json:"cache_read_multiplier"`
	MarginalCostFactor        float64                      `json:"marginal_cost_factor"`
	EffectiveInputUSDPer1M    float64                      `json:"effective_input_usd_per_1m"`
	EffectiveOutputUSDPer1M   float64                      `json:"effective_output_usd_per_1m"`
	EffectiveEstimatedCostUSD float64                      `json:"effective_estimated_cost_usd"`
	Capabilities              policy.CandidateCapabilities `json:"capabilities"`

	ArmID                        string `json:"arm_id,omitempty"`
	BindingIndex                 *int   `json:"binding_index,omitempty"`
	Endpoint                     string `json:"endpoint,omitempty"`
	ModelRevision                string `json:"model_revision,omitempty"`
	ReasoningConfigurationSHA256 string `json:"reasoning_configuration_sha256,omitempty"`
	ToolConfigurationSHA256      string `json:"tool_configuration_sha256,omitempty"`
}

type routingKnobs struct {
	QualityBias          *float64 `json:"quality_bias,omitempty"`
	SpeedWeight          *float64 `json:"speed_weight,omitempty"`
	OutputCostRatio      *float64 `json:"output_cost_ratio,omitempty"`
	ExpectedOutputTokens *int     `json:"expected_output_tokens,omitempty"`
}

type routeMessage struct {
	Role        string            `json:"role"`
	Text        string            `json:"text,omitempty"`
	ToolCalls   []routeToolCall   `json:"tool_calls,omitempty"`
	ToolResults []routeToolResult `json:"tool_results,omitempty"`
}

type routeTool struct {
	Name           string `json:"name"`
	Type           string `json:"type,omitempty"`
	ServerExecuted bool   `json:"server_executed,omitempty"`
}

type routeToolCall struct {
	Name      string   `json:"name,omitempty"`
	InputKeys []string `json:"input_keys,omitempty"`
	InputJSON string   `json:"input_json,omitempty"`
}

type routeToolResult struct {
	ToolUseID     string `json:"tool_use_id,omitempty"`
	IsError       bool   `json:"is_error,omitempty"`
	Text          string `json:"text,omitempty"`
	ResultPresent bool   `json:"result_present,omitempty"`
	CharCount     int    `json:"char_count,omitempty"`
	ByteCount     int    `json:"byte_count,omitempty"`
	ExitCategory  string `json:"exit_category,omitempty"`
}

type classifierResponseV4 struct {
	SchemaVersion         string                 `json:"schema_version"`
	RouteID               string                 `json:"route_id"`
	ClassifierArtifactID  string                 `json:"classifier_artifact_id"`
	ClassifierSHA256      string                 `json:"classifier_artifact_sha256"`
	PredictedLabel        string                 `json:"predicted_label"`
	ClassOrder            []string               `json:"class_order"`
	ClassProbabilities    map[string]float64     `json:"class_probabilities"`
	ClassifierConfidence  *float64               `json:"classifier_confidence"`
	ClassifierMargin      *float64               `json:"classifier_margin"`
	HMMStateID            int                    `json:"hmm_state_id"`
	HMMStatePath          []int                  `json:"hmm_state_path"`
	HMMStateProbabilities []float64              `json:"hmm_state_probabilities"`
	DebugRef              string                 `json:"debug_ref,omitempty"`
	Debug                 map[string]interface{} `json:"debug,omitempty"`
	Timings               *routeTimings          `json:"timings,omitempty"`
	Error                 string                 `json:"error,omitempty"`
}

type routeTimings struct {
	RouteMs             *float64 `json:"route_ms"`
	SelectMs            *float64 `json:"select_ms"`
	EmbedMs             *float64 `json:"embed_ms"`
	EmbedCacheHits      *int64   `json:"embed_cache_hits"`
	EmbedCacheMisses    *int64   `json:"embed_cache_misses"`
	EmbedCacheEvictions *int64   `json:"embed_cache_evictions"`
	RoutesInflight      *int64   `json:"routes_inflight"`
	OverrunsLive        *int64   `json:"overruns_live"`
}

func (t *routeTimings) UnmarshalJSON(payload []byte) error {
	type lenientRouteTimings routeTimings
	return json.Unmarshal(payload, (*lenientRouteTimings)(t))
}

func decomposeTimings(wire *routeTimings) *router.SidecarTimings {
	if wire == nil || (wire.RouteMs == nil && wire.SelectMs == nil && wire.EmbedMs == nil) {
		return nil
	}
	timings := &router.SidecarTimings{
		EmbedMs:  wire.EmbedMs,
		SelectMs: wire.SelectMs,
	}
	if wire.RouteMs != nil {
		other := *wire.RouteMs - floatOrZero(wire.EmbedMs) - floatOrZero(wire.SelectMs)
		if other < 0 {
			return nil
		}
		timings.OtherMs = &other
	}
	return timings
}

func floatOrZero(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

func extractServingStats(wire *routeTimings) *router.SidecarServingStats {
	if wire == nil {
		return nil
	}
	if wire.EmbedCacheHits == nil && wire.EmbedCacheMisses == nil && wire.EmbedCacheEvictions == nil &&
		wire.RoutesInflight == nil && wire.OverrunsLive == nil {
		return nil
	}
	return &router.SidecarServingStats{
		EmbedCacheHits:      wire.EmbedCacheHits,
		EmbedCacheMisses:    wire.EmbedCacheMisses,
		EmbedCacheEvictions: wire.EmbedCacheEvictions,
		RoutesInflight:      wire.RoutesInflight,
		OverrunsLive:        wire.OverrunsLive,
	}
}

type previewResponse struct {
	SchemaVersion         string                `json:"schema_version"`
	RouteID               string                `json:"route_id"`
	PolicyArtifactID      string                `json:"policy_artifact_id"`
	PolicyArtifactSHA256  string                `json:"policy_artifact_sha256"`
	PolicySHA256          string                `json:"policy_sha256"`
	RosterSHA256          string                `json:"roster_sha256"`
	HMMStateID            int                   `json:"hmm_state_id"`
	HMMStatePath          []int                 `json:"hmm_state_path"`
	HMMStateProbabilities []float64             `json:"hmm_state_probabilities"`
	ClassOrder            []string              `json:"class_order"`
	ClassProbabilities    map[string]float64    `json:"class_probabilities"`
	RankedFallback        []policy.PreviewGroup `json:"ranked_fallback"`
	SelectedGroup         string                `json:"selected_group"`
	EligibleRosterIDs     []string              `json:"eligible_roster_ids"`
	Error                 string                `json:"error"`
}

func (c *Client) Decide(ctx context.Context, query policy.Query) (policy.Result, error) {
	if c.taskDomain != nil && query.ExecutionMode == policy.ExecutionModeServing && query.TaskDomain != nil {
		return c.decideWithTaskDomain(ctx, query)
	}
	if c.expectedPolicySHA256 == "" && (query.ClassifierReleaseID != "" || query.ClassifierSelectionID != "") {
		return c.decideV5(ctx, query)
	}
	if query.SchemaVersion == policy.SchemaVersionV5 {
		return policy.Result{}, errors.New("v5 classifier request requires a pinned release and selection")
	}
	return c.decide(ctx, query)
}

func (c *Client) decideV5(ctx context.Context, query policy.Query) (policy.Result, error) {
	wire, err := c.classifyV5(ctx, "/v5/classify", query)
	if err != nil {
		return policy.Result{}, err
	}
	stateID := 0
	if wire.HMMStateID != nil {
		stateID = *wire.HMMStateID
	}
	return policy.Result{
		SchemaVersion: policy.SchemaVersionV5, RouteID: wire.RequestID,
		PolicyArtifactID: wire.ClassifierArtifactID, PolicyArtifactSHA256: wire.ClassifierArtifactSHA256,
		ClassifierReleaseID: wire.ClassifierReleaseID, ClassifierSelectionID: wire.ClassifierSelectionID,
		ClassifierVariantID: wire.ClassifierVariantID, ClassifierTaxonomySHA256: wire.ClassifierTaxonomySHA256,
		PredictedLabel: wire.PredictedLabel, ClassOrder: wire.ClassOrder, ClassProbabilities: wire.ClassProbabilities,
		Score: wire.ClassifierConfidence, Confidence: pointerTo(wire.ClassifierConfidence), Margin: pointerTo(wire.ClassifierMargin),
		HMMStateID: stateID, HMMStatePath: wire.HMMStatePath, HMMStateProbabilities: wire.HMMStateProbabilities,
	}, nil
}

func (c *Client) classifyV5(ctx context.Context, path string, query policy.Query) (classifierResponseV5, error) {
	requestBody, err := marshalClassifierRequestV5(query)
	if err != nil {
		return classifierResponseV5{}, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	status, payload, err := c.doPolicyRequest(requestCtx, path, requestBody)
	if err != nil {
		return classifierResponseV5{}, err
	}
	var wire classifierResponseV5
	if err := decodeStrictJSON(payload, &wire); err != nil {
		return classifierResponseV5{}, fmt.Errorf("decode v5 classifier %s response (status %d): %w", path, status, err)
	}
	if status != http.StatusOK {
		if pinErr := classifierPinUnavailable(query, status); pinErr != nil {
			return classifierResponseV5{}, pinErr
		}
		if wire.Error != "" {
			return classifierResponseV5{}, fmt.Errorf("v5 classifier %s status %d: %s", path, status, wire.Error)
		}
		if detail := classifierErrorDetail(wire.Detail); detail != "" {
			return classifierResponseV5{}, fmt.Errorf("v5 classifier %s status %d: %s", path, status, detail)
		}
		return classifierResponseV5{}, fmt.Errorf("v5 classifier %s status %d", path, status)
	}
	if err := validateClassifierResponseV5(query, wire); err != nil {
		return classifierResponseV5{}, fmt.Errorf("invalid v5 classifier %s response: %w", path, err)
	}
	return wire, nil
}

func validateClassifierResponseV5(query policy.Query, response classifierResponseV5) error {
	if response.ClassifierReleaseID != query.ClassifierReleaseID || response.ClassifierSelectionID != query.ClassifierSelectionID ||
		response.ClassifierVariantID != query.ClassifierVariantID ||
		(query.ClassifierArtifactSHA256 != "" && response.ClassifierArtifactSHA256 != query.ClassifierArtifactSHA256) {
		return router.ErrPolicyPinUnavailable
	}
	if response.RequestID != query.RouteID {
		return errors.New("v5 classifier response answers another request")
	}
	if response.SchemaVersion != policy.SchemaVersionV5 || response.ClassifierArtifactID == "" ||
		!validClassifierSHA256(response.ClassifierArtifactSHA256) || !validClassifierSHA256(response.ClassifierTaxonomySHA256) ||
		response.PredictedLabel == "" || len(response.ClassOrder) == 0 || len(response.ClassProbabilities) != len(response.ClassOrder) {
		return errors.New("v5 classifier response is missing identity or facts")
	}
	if taxonomyDigest(response.ClassOrder) != response.ClassifierTaxonomySHA256 {
		return errors.New("v5 classifier taxonomy digest does not match class order")
	}
	for _, label := range response.ClassOrder {
		if _, exists := response.ClassProbabilities[label]; !exists {
			return fmt.Errorf("v5 classifier probabilities omit class %q", label)
		}
	}
	if _, exists := response.ClassProbabilities[response.PredictedLabel]; !exists {
		return errors.New("v5 classifier prediction is outside class order")
	}
	if (response.HMMStateID == nil) != (response.HMMStatePath == nil) || (response.HMMStateID == nil) != (response.HMMStateProbabilities == nil) {
		return errors.New("v5 HMM state facts must be present together")
	}
	return nil
}

func validClassifierVariantID(variantID policy.ClassifierVariantID) bool {
	switch variantID {
	case policy.ClassifierVariantPrimary, policy.ClassifierVariantHMMBeta, policy.ClassifierVariantQwen:
		return true
	default:
		return false
	}
}

func classifierPinUnavailable(query policy.Query, status int) error {
	if query.ClassifierReleaseID == "" || query.ClassifierSelectionID == "" ||
		(status != http.StatusNotFound && status != http.StatusForbidden && status != http.StatusConflict) {
		return nil
	}
	return fmt.Errorf("classifier release %q is unavailable for selection %q: %w", query.ClassifierReleaseID, query.ClassifierSelectionID, router.ErrPolicyPinUnavailable)
}

func (c *Client) decide(ctx context.Context, query policy.Query) (policy.Result, error) {
	body, err := marshalRouteRequest(query)
	if err != nil {
		return policy.Result{}, err
	}

	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	status, payload, err := c.doPolicyRequest(requestCtx, "/route", body)
	if err != nil {
		return policy.Result{}, err
	}

	return decodeRouteResponse(query, status, payload, c.expectedPolicySHA256)
}

func decodeClassifierResponseV4(payload []byte) (classifierResponseV4, error) {
	var response classifierResponseV4
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return classifierResponseV4{}, err
	}
	if response.ClassifierArtifactID == "" || response.ClassifierSHA256 == "" {
		return classifierResponseV4{}, fmt.Errorf("classifier response is missing artifact identity")
	}
	if response.PredictedLabel == "" || len(response.ClassOrder) == 0 || len(response.ClassProbabilities) == 0 {
		return classifierResponseV4{}, fmt.Errorf("classifier response is missing class facts")
	}
	return response, nil
}

func valueOrZero(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

func (c *Client) Preview(ctx context.Context, query policy.Query) (policy.PreviewResult, error) {
	if query.ExecutionMode != policy.ExecutionModePreview {
		return policy.PreviewResult{}, fmt.Errorf("policy preview requires preview execution mode")
	}
	if c.expectedPolicySHA256 == "" && (query.ClassifierReleaseID != "" || query.ClassifierSelectionID != "") {
		return c.previewV5(ctx, query)
	}
	if query.SchemaVersion == policy.SchemaVersionV5 {
		return policy.PreviewResult{}, errors.New("v5 classifier preview requires a pinned release and selection")
	}
	body, err := marshalRouteRequest(query)
	if err != nil {
		return policy.PreviewResult{}, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	status, payload, err := c.doPolicyRequest(requestCtx, "/preview", body)
	if err != nil {
		return policy.PreviewResult{}, err
	}
	return decodePreviewResponse(query, status, payload, c.expectedPolicySHA256)
}

func decodePreviewResponse(query policy.Query, status int, payload []byte, expectedPolicySHA256 string) (policy.PreviewResult, error) {
	var parsed previewResponse
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return policy.PreviewResult{}, fmt.Errorf("decode policy preview response (status %d): %w", status, err)
	}
	if status != http.StatusOK {
		if err := unknownArtifactErr(query, status); err != nil {
			return policy.PreviewResult{}, err
		}
		if parsed.Error != "" {
			return policy.PreviewResult{}, fmt.Errorf("policy preview status %d: %s", status, parsed.Error)
		}
		return policy.PreviewResult{}, fmt.Errorf("policy preview status %d", status)
	}
	policySHA256 := firstNonEmpty(parsed.PolicyArtifactSHA256, parsed.PolicySHA256)
	if err := checkPolicyResponsePin(expectedPolicySHA256, policySHA256, parsed.SchemaVersion, parsed.RouteID, query.RouteID); err != nil {
		return policy.PreviewResult{}, err
	}
	if parsed.SchemaVersion == policy.SchemaVersionV4 {
		classifier, decodeErr := decodeClassifierResponseV4(payload)
		if decodeErr != nil {
			return policy.PreviewResult{}, fmt.Errorf("decode policy preview response (status %d): %w", status, decodeErr)
		}
		return policy.PreviewResult{
			SchemaVersion: classifier.SchemaVersion, RouteID: classifier.RouteID,
			PolicyArtifactID: classifier.ClassifierArtifactID, PolicyArtifactSHA256: classifier.ClassifierSHA256,
			HMMStateID: classifier.HMMStateID, HMMStatePath: classifier.HMMStatePath, HMMStateProbabilities: classifier.HMMStateProbabilities,
			ClassOrder: classifier.ClassOrder, ClassProbabilities: classifier.ClassProbabilities, PredictedLabel: classifier.PredictedLabel,
		}, nil
	}
	if !supportedSchema(parsed.SchemaVersion) {
		return policy.PreviewResult{}, fmt.Errorf("unsupported policy preview schema %q", parsed.SchemaVersion)
	}
	return policy.PreviewResult{
		SchemaVersion:         parsed.SchemaVersion,
		RouteID:               parsed.RouteID,
		PolicyArtifactID:      parsed.PolicyArtifactID,
		PolicyArtifactSHA256:  policySHA256,
		RosterSHA256:          parsed.RosterSHA256,
		HMMStateID:            parsed.HMMStateID,
		HMMStatePath:          parsed.HMMStatePath,
		HMMStateProbabilities: parsed.HMMStateProbabilities,
		ClassOrder:            parsed.ClassOrder,
		ClassProbabilities:    parsed.ClassProbabilities,
		RankedFallback:        parsed.RankedFallback,
		SelectedGroup:         parsed.SelectedGroup,
		EligibleRosterIDs:     parsed.EligibleRosterIDs,
	}, nil
}

func (c *Client) previewV5(ctx context.Context, query policy.Query) (policy.PreviewResult, error) {
	wire, err := c.classifyV5(ctx, "/v5/preview", query)
	if err != nil {
		return policy.PreviewResult{}, err
	}
	stateID := 0
	if wire.HMMStateID != nil {
		stateID = *wire.HMMStateID
	}
	return policy.PreviewResult{
		SchemaVersion: policy.SchemaVersionV5, RouteID: wire.RequestID,
		PolicyArtifactID: wire.ClassifierArtifactID, PolicyArtifactSHA256: wire.ClassifierArtifactSHA256,
		ClassifierReleaseID: wire.ClassifierReleaseID, ClassifierSelectionID: wire.ClassifierSelectionID,
		ClassifierVariantID: wire.ClassifierVariantID, ClassifierTaxonomySHA256: wire.ClassifierTaxonomySHA256,
		ClassOrder: wire.ClassOrder, ClassProbabilities: wire.ClassProbabilities, PredictedLabel: wire.PredictedLabel,
		HMMStateID: stateID, HMMStatePath: wire.HMMStatePath, HMMStateProbabilities: wire.HMMStateProbabilities,
	}, nil
}

func supportedSchema(version string) bool {
	switch version {
	case policy.SchemaVersionV1, policy.SchemaVersionV4:
		return true
	default:
		return false
	}
}

func marshalRouteRequest(query policy.Query) ([]byte, error) {
	schemaVersion := query.SchemaVersion
	if schemaVersion == "" {
		schemaVersion = policy.SchemaVersionV1
	}
	candidates := routeCandidates(query.Candidates, schemaVersion)
	models := make([]string, 0, len(query.Candidates))
	providerMap := make(map[string]string, len(query.Candidates))
	for _, candidate := range query.Candidates {
		models = append(models, candidate.RosterID)
		providerMap[candidate.RosterID] = candidate.Provider
	}
	routeLimits := classifierRouteMessageLimits(query)
	messages := convertRouteMessages(query.ConversationMessages, routeLimits)
	var omittedUserTurnCount *int
	if query.InteractionStateInputs {
		omittedUserTurnCount = pointerTo(userTextTurnCount(query.ConversationMessages) - routedUserTextTurnCount(messages))
	}
	wireTurnIndex := turnIndex(messages)
	var visibleTurnIndex *int
	var sessionTurnCount *int
	var sessionEverSwitched *bool
	var historyTruncated *bool
	var turnType string
	var previousServedModel string
	var previousProvider string
	var cacheState string
	var priorOutputTokens *int
	if query.TurnContext != nil {
		wireTurnIndex = query.TurnContext.VisibleTurnIndex
		visibleTurnIndex = pointerTo(query.TurnContext.VisibleTurnIndex)
		sessionTurnCount = pointerTo(query.TurnContext.SessionTurnCount)
		sessionEverSwitched = pointerTo(query.TurnContext.SessionEverSwitched)
		historyTruncated = pointerTo(
			query.TurnContext.HistoryTruncated ||
				routeMessagesExceedLimits(query.ConversationMessages, routeLimits),
		)
		turnType = query.TurnContext.TurnType
		previousServedModel = query.TurnContext.PreviousServedModel
		previousProvider = query.TurnContext.PreviousProvider
		cacheState = query.TurnContext.CacheState
		priorOutputTokens = query.TurnContext.PriorOutputTokens
	}
	var trainingDelta []routeMessage
	if router.IsHMMStrategy(query.Strategy) && query.TrainingAllowed {
		trainingDelta = trainingRouteMessageDelta(query.ConversationMessages)
	}
	harness := policy.HarnessForClientApp(query.ClientApp)
	invokedTools := clipRouteValues(invokedToolNames(query.ConversationMessages))
	availableTools := clipRouteValues(unionToolNames(query.AvailableTools, invokedTools))
	if schemaVersion == policy.SchemaVersionV4 {
		body, err := json.Marshal(classifierRequestV4{
			SchemaVersion: schemaVersion, Strategy: string(query.Strategy), ExecutionMode: query.ExecutionMode, RouteID: query.RouteID,
			OrganizationID: query.OrganizationID, InstallationID: query.InstallationID, ClientApp: query.ClientApp, Harness: harness, RolloutID: query.RolloutID,
			PromptText: query.PromptText, LatestUserText: latestUserText(messages), TurnIndex: wireTurnIndex, IsSubagent: turnType == "sub_agent_dispatch",
			VisibleTurnIndex: visibleTurnIndex, SessionTurnCount: sessionTurnCount, TurnType: turnType, CacheState: cacheState, PriorOutputTokens: priorOutputTokens,
			SessionEverSwitched: sessionEverSwitched, HistoryTruncated: historyTruncated, ConversationMessages: messages,
			OmittedUserTurnCount: omittedUserTurnCount, TrainingConversationDelta: trainingDelta,
			AvailableTools: availableTools, InvokedTools: invokedTools, Tools: routeTools(query.Tools),
			FeedbackKey: query.FeedbackKey, FeedbackRole: query.FeedbackRole, ClientSessionID: query.ClientSessionID, EstimatedInputTokens: query.EstimatedInputTokens,
			HasTools: query.HasTools, HasImages: query.HasImages, TrainingAllowed: query.TrainingAllowed, CaptureMode: query.CaptureMode, DebugEnabled: query.DebugEnabled,
		})
		if err != nil {
			return nil, fmt.Errorf("marshal classifier request: %w", err)
		}
		return body, nil
	}
	body, err := json.Marshal(routeRequest{
		SchemaVersion:             schemaVersion,
		ArtifactSHA256:            query.ArtifactSHA256,
		Strategy:                  string(query.Strategy),
		ExecutionMode:             query.ExecutionMode,
		RouteID:                   query.RouteID,
		OrganizationID:            query.OrganizationID,
		InstallationID:            query.InstallationID,
		ClientApp:                 query.ClientApp,
		Harness:                   harness,
		RolloutID:                 query.RolloutID,
		RequestedModel:            query.RequestedModel,
		PromptText:                query.PromptText,
		LatestUserText:            latestUserText(messages),
		TurnIndex:                 wireTurnIndex,
		IsSubagent:                turnType == "sub_agent_dispatch",
		VisibleTurnIndex:          visibleTurnIndex,
		SessionTurnCount:          sessionTurnCount,
		TurnType:                  turnType,
		PreviousServedModel:       previousServedModel,
		PreviousProvider:          previousProvider,
		CacheState:                cacheState,
		PriorOutputTokens:         priorOutputTokens,
		SessionEverSwitched:       sessionEverSwitched,
		HistoryTruncated:          historyTruncated,
		ConversationMessages:      messages,
		TrainingConversationDelta: trainingDelta,
		AvailableTools:            availableTools,
		InvokedTools:              invokedTools,
		Tools:                     routeTools(query.Tools),
		FeedbackKey:               query.FeedbackKey,
		FeedbackRole:              query.FeedbackRole,
		ClientSessionID:           query.ClientSessionID,
		EstimatedInputTokens:      query.EstimatedInputTokens,
		HasTools:                  query.HasTools,
		HasImages:                 query.HasImages,
		RoutingIntent:             query.RoutingIntent,
		PreferredModels:           query.PreferredModels,
		RoutingKnobs:              wireRoutingKnobs(query.RoutingKnobs),
		QualityBias:               qualityBias(query.RoutingKnobs),
		TrainingAllowed:           query.TrainingAllowed,
		CaptureMode:               query.CaptureMode,
		DebugEnabled:              query.DebugEnabled,
		Candidates:                candidates,
		CandidateModels:           models,
		CandidateProviders:        providerMap,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal policy route request: %w", err)
	}
	return body, nil
}

func routeCandidates(candidates []policy.Candidate, schemaVersion string) []routeCandidate {
	result := make([]routeCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		wireCandidate := routeCandidate{
			RosterID:                  candidate.RosterID,
			CatalogID:                 candidate.CatalogID,
			Provider:                  candidate.Provider,
			UpstreamID:                candidate.UpstreamID,
			PreferenceRank:            candidate.PreferenceRank,
			InputUSDPer1M:             candidate.InputUSDPer1M,
			OutputUSDPer1M:            candidate.OutputUSDPer1M,
			EstimatedCostUSD:          candidate.EstimatedCostUSD,
			CacheReadMultiplier:       candidate.CacheReadMultiplier,
			MarginalCostFactor:        candidate.MarginalCostFactor,
			EffectiveInputUSDPer1M:    candidate.EffectiveInputUSDPer1M,
			EffectiveOutputUSDPer1M:   candidate.EffectiveOutputUSDPer1M,
			EffectiveEstimatedCostUSD: candidate.EffectiveEstimatedCostUSD,
			Capabilities:              candidate.Capabilities,
		}
		result = append(result, wireCandidate)
	}
	return result
}

func pointerTo[T any](value T) *T {
	return &value
}

func (c *Client) doPolicyRequest(ctx context.Context, path string, body []byte) (int, []byte, error) {
	var lastErr, lastStatusErr error
	for attempt := 1; attempt <= defaultRouteAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return 0, nil, exhaustedPolicyErr(lastStatusErr, lastErr, err)
		}
		status, payload, done, attemptErr := c.doPolicyAttempt(ctx, attempt, path, body)
		if done {
			return status, payload, attemptErr
		}
		lastErr = attemptErr
		var statusErr *PolicyStatusError
		if errors.As(attemptErr, &statusErr) {
			lastStatusErr = attemptErr
		}
		if attempt == defaultRouteAttempts {
			break
		}
		timer := time.NewTimer(time.Duration(attempt) * routeRetryBackoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 0, nil, exhaustedPolicyErr(lastStatusErr, lastErr, ctx.Err())
		case <-timer.C:
		}
	}
	return 0, nil, exhaustedPolicyErr(lastStatusErr, lastErr, nil)
}

func preferStatusErr(statusErr, lastErr error) error {
	if statusErr != nil {
		return statusErr
	}
	return lastErr
}

func exhaustedPolicyErr(statusErr, lastErr, ctxErr error) error {
	primary := preferStatusErr(statusErr, lastErr)
	deadline := cancellationSignal(ctxErr, lastErr, statusErr)
	switch {
	case primary == nil && deadline == nil:
		return errors.New("policy sidecar retries exhausted")
	case primary == nil:
		return fmt.Errorf("policy sidecar retries exhausted: %w", deadline)
	case deadline == nil || errors.Is(primary, deadline):
		return fmt.Errorf("policy sidecar retries exhausted: %w", primary)
	default:
		return fmt.Errorf("policy sidecar retries exhausted: %w: %w", primary, deadline)
	}
}

func cancellationSignal(candidates ...error) error {
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		if errors.Is(candidate, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
		if errors.Is(candidate, context.Canceled) {
			return context.Canceled
		}
	}
	return nil
}

func (c *Client) doPolicyAttempt(
	ctx context.Context,
	attempt int,
	path string,
	body []byte,
) (int, []byte, bool, error) {
	attemptCtx, cancel := c.attemptContext(ctx)
	defer cancel()

	req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, true, fmt.Errorf("build policy route request: %w", err)
	}
	req.Header.Set("content-type", "application/json")
	if deadline, ok := attemptCtx.Deadline(); ok {
		req.Header.Set(policy.ClassifierTimeoutHeader, strconv.FormatInt(min(max(time.Until(deadline).Milliseconds(), 1), policy.MaxClassifierTimeoutMilliseconds), 10))
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return 0, nil, false, c.attemptError(ctx, attemptCtx, attempt, fmt.Errorf("call policy sidecar: %w", err))
	}
	if resp.StatusCode == http.StatusRequestEntityTooLarge {
		_ = resp.Body.Close()
		return 0, nil, true, fmt.Errorf(
			"policy sidecar rejected %d-byte %s request (status %d): %w",
			len(body), path, resp.StatusCode, policy.ErrContextWindowExceeded,
		)
	}
	payload, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	if readErr != nil {
		return 0, nil, false, c.attemptError(ctx, attemptCtx, attempt, fmt.Errorf("read policy route response: %w", readErr))
	}
	if resp.StatusCode == http.StatusServiceUnavailable {
		var failure struct {
			Error policy.ClassifierErrorCode `json:"error"`
		}
		if json.Unmarshal(payload, &failure) == nil && failure.Error == policy.ClassifierWarmingCode {
			return 0, nil, true, policy.ErrClassifierWarming
		}
	}
	if !isTransientPolicyStatus(resp.StatusCode) {
		return resp.StatusCode, payload, true, nil
	}
	return 0, nil, false, policyStatusError(resp.StatusCode, payload)
}

func (c *Client) attemptContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.attemptTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, c.attemptTimeout)
}

func (c *Client) attemptError(ctx, attemptCtx context.Context, attempt int, err error) error {
	if ctx.Err() == nil && errors.Is(attemptCtx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf(
			"policy sidecar attempt %d exceeded its %s attempt budget: %w",
			attempt,
			c.attemptTimeout,
			context.DeadlineExceeded,
		)
	}
	return err
}

func isTransientPolicyStatus(status int) bool {
	return status == http.StatusInternalServerError ||
		status == http.StatusBadGateway ||
		status == http.StatusServiceUnavailable ||
		status == http.StatusGatewayTimeout
}

func unknownArtifactErr(query policy.Query, status int) error {
	if query.ArtifactSHA256 == "" || status != http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("policy sidecar has no artifact %q: %w", query.ArtifactSHA256, router.ErrPolicyPinUnavailable)
}

type PolicyStatusError struct {
	Status  int
	Message string
}

func (e *PolicyStatusError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("policy sidecar status %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("policy sidecar status %d", e.Status)
}

func policyStatusError(status int, payload []byte) error {
	var parsed struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(payload, &parsed)
	return &PolicyStatusError{Status: status, Message: parsed.Error}
}

func wireRoutingKnobs(knobs *router.Overrides) *routingKnobs {
	if knobs == nil {
		return nil
	}
	return &routingKnobs{
		QualityBias:          knobs.QualityBias,
		SpeedWeight:          knobs.SpeedWeight,
		OutputCostRatio:      knobs.OutputCostRatio,
		ExpectedOutputTokens: knobs.ExpectedOutputTokens,
	}
}

func qualityBias(knobs *router.Overrides) *float64 {
	if knobs == nil {
		return nil
	}
	return knobs.QualityBias
}

type routeMessageLimits struct {
	maxMessages            int
	maxTextChars           int
	maxTotalTextChars      int
	maxToolCallInputKeys   int
	maxToolCallInputChars  int
	includeToolCallInput   bool
	includeToolResultText  bool
	dropTextAfterBudgetCut bool
}

var defaultRouteMessageLimits = routeMessageLimits{
	maxMessages:           maxRouteMessages,
	maxTextChars:          maxRouteMessageTextChars,
	maxTotalTextChars:     maxRouteMessageTotalChars,
	maxToolCallInputKeys:  maxRouteToolCallInputKeys,
	maxToolCallInputChars: maxRouteToolCallInputChars,
}

var interactionStateRouteMessageLimits = routeMessageLimits{
	maxMessages:            maxRouteMessages,
	maxTextChars:           interactionStateRouteMessageTextChars,
	maxTotalTextChars:      interactionStateRouteMessageTotalChars,
	maxToolCallInputKeys:   maxRouteToolCallInputKeys,
	maxToolCallInputChars:  maxRouteToolCallInputChars,
	dropTextAfterBudgetCut: true,
}

func classifierRouteMessageLimits(query policy.Query) routeMessageLimits {
	if query.InteractionStateInputs {
		return interactionStateRouteMessageLimits
	}
	return defaultRouteMessageLimits
}

func routeMessages(messages []router.ConversationMessage) []routeMessage {
	return convertRouteMessages(messages, defaultRouteMessageLimits)
}

func routeMessagesTruncated(messages []router.ConversationMessage) bool {
	return routeMessagesExceedLimits(messages, defaultRouteMessageLimits)
}

func routeMessagesExceedLimits(messages []router.ConversationMessage, limits routeMessageLimits) bool {
	if len(messages) > limits.maxMessages {
		return true
	}
	totalText := 0
	for _, message := range messages {
		text := strings.TrimSpace(message.Text)
		if len(text) > limits.maxTextChars {
			return true
		}
		totalText += len(text)
		if totalText > limits.maxTotalTextChars {
			return true
		}
		for _, call := range message.ToolCalls {
			if len(strings.TrimSpace(call.Name)) > limits.maxToolCallInputChars ||
				len(call.InputKeys) > limits.maxToolCallInputKeys {
				return true
			}
			for _, key := range call.InputKeys {
				if len(strings.TrimSpace(key)) > limits.maxToolCallInputChars {
					return true
				}
			}
		}
	}
	return false
}

func trainingRouteMessageDelta(messages []router.ConversationMessage) []routeMessage {
	if len(messages) == 0 {
		return nil
	}

	start := 0
	latestUser := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if routeRole(messages[i].Role) == "user" {
			latestUser = i
			break
		}
	}
	if latestUser >= 0 {
		for i := latestUser - 1; i >= 0; i-- {
			if routeRole(messages[i].Role) == "assistant" {
				start = i
				break
			}
		}
	}

	return convertRouteMessages(messages[start:], routeMessageLimits{
		includeToolCallInput:  true,
		includeToolResultText: true,
	})
}

func routeMessageWindow(messages []router.ConversationMessage, maxMessages int) []router.ConversationMessage {
	if maxMessages <= 0 || len(messages) <= maxMessages {
		return messages
	}
	window := messages[len(messages)-maxMessages:]
	if userTextIndex(window) >= 0 {
		return window
	}
	boundary := userTextIndex(messages[:len(messages)-maxMessages])
	if boundary < 0 {
		return window
	}
	kept := make([]router.ConversationMessage, 0, maxMessages)
	kept = append(kept, messages[boundary])
	return append(kept, window[1:]...)
}

func userTextIndex(messages []router.ConversationMessage) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if routeRole(messages[i].Role) == "user" && strings.TrimSpace(messages[i].Text) != "" {
			return i
		}
	}
	return -1
}

func convertRouteMessages(messages []router.ConversationMessage, limits routeMessageLimits) []routeMessage {
	if len(messages) == 0 {
		return nil
	}
	messages = routeMessageWindow(messages, limits.maxMessages)
	latestUserTextIndex := userTextIndex(messages)
	boundaryReserve := 0
	if latestUserTextIndex >= 0 && limits.maxTotalTextChars > 0 {
		boundaryReserve = len(clipRouteText(clipRouteText(messages[latestUserTextIndex].Text, limits.maxTextChars), limits.maxTotalTextChars))
	}
	reversed := make([]routeMessage, 0, len(messages))
	totalText := 0
	budgetCut := false
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		role := routeRole(message.Role)
		if role == "" {
			continue
		}
		text := clipRouteText(message.Text, limits.maxTextChars)
		if limits.maxTotalTextChars > 0 {
			budget := limits.maxTotalTextChars
			if i > latestUserTextIndex {
				budget -= boundaryReserve
			}
			if budgetCut && i != latestUserTextIndex {
				text = ""
			} else if remaining := budget - totalText; remaining < len(text) {
				budgetCut = limits.dropTextAfterBudgetCut
				text = ""
				if remaining > 0 {
					text = clipRouteText(message.Text, remaining)
				}
			}
		}
		totalText += len(text)
		calls := make([]routeToolCall, 0, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			name := clipRouteText(call.Name, limits.maxToolCallInputChars)
			if name == "" {
				continue
			}
			keys := call.InputKeys
			if limits.maxToolCallInputKeys > 0 && len(keys) > limits.maxToolCallInputKeys {
				keys = keys[:limits.maxToolCallInputKeys]
			}
			inputKeys := make([]string, 0, len(keys))
			for _, key := range keys {
				if clipped := clipRouteText(key, limits.maxToolCallInputChars); clipped != "" {
					inputKeys = append(inputKeys, clipped)
				}
			}
			routeCall := routeToolCall{Name: name, InputKeys: inputKeys}
			if limits.includeToolCallInput {
				routeCall.InputJSON = clipRouteText(call.InputJSON, limits.maxTextChars)
			}
			calls = append(calls, routeCall)
		}
		results := make([]routeToolResult, 0, len(message.ToolResults))
		for _, result := range message.ToolResults {
			charCount := result.CharCount
			byteCount := result.ByteCount
			if result.Text != "" {
				if charCount == 0 {
					charCount = utf8.RuneCountInString(result.Text)
				}
				if byteCount == 0 {
					byteCount = len(result.Text)
				}
			}
			routeResult := routeToolResult{
				ToolUseID:     clipRouteText(result.ToolUseID, limits.maxToolCallInputChars),
				IsError:       result.IsError,
				ResultPresent: result.ResultPresent || result.ToolUseID != "" || result.Text != "",
				CharCount:     charCount,
				ByteCount:     byteCount,
				ExitCategory:  result.ExitCategory,
			}
			if limits.includeToolResultText {
				routeResult.Text = clipRouteText(result.Text, limits.maxTextChars)
			}
			results = append(results, routeResult)
		}
		if text == "" && len(calls) == 0 && len(results) == 0 {
			continue
		}
		reversed = append(reversed, routeMessage{Role: role, Text: text, ToolCalls: calls, ToolResults: results})
	}
	out := make([]routeMessage, 0, len(reversed))
	for i := len(reversed) - 1; i >= 0; i-- {
		out = append(out, reversed[i])
	}
	return out
}

func routeRole(role string) string {
	switch strings.TrimSpace(strings.ToLower(role)) {
	case "system", "developer", "user", "assistant":
		return strings.TrimSpace(strings.ToLower(role))
	case "model":
		return "assistant"
	default:
		return ""
	}
}

func clipRouteText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return strings.TrimSpace(text[:limit])
}

func latestUserText(messages []routeMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" && strings.TrimSpace(messages[i].Text) != "" {
			return strings.TrimSpace(messages[i].Text)
		}
	}
	return ""
}

func invokedToolNames(messages []router.ConversationMessage) []string {
	var names []string
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			if name := strings.TrimSpace(call.Name); name != "" {
				names = append(names, name)
			}
		}
	}
	return dedupeStrings(names)
}

func unionToolNames(advertised, invoked []string) []string {
	merged := make([]string, 0, len(advertised)+len(invoked))
	for _, name := range advertised {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			merged = append(merged, trimmed)
		}
	}
	merged = append(merged, invoked...)
	return dedupeStrings(merged)
}

func dedupeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func turnIndex(messages []routeMessage) int {
	count := routedUserTextTurnCount(messages)
	if count <= 1 {
		return 0
	}
	return count - 1
}

func routedUserTextTurnCount(messages []routeMessage) int {
	count := 0
	for _, message := range messages {
		if message.Role == "user" && strings.TrimSpace(message.Text) != "" {
			count++
		}
	}
	return count
}

func userTextTurnCount(messages []router.ConversationMessage) int {
	count := 0
	for _, message := range messages {
		if routeRole(message.Role) == "user" && strings.TrimSpace(message.Text) != "" {
			count++
		}
	}
	return count
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstFloat(values ...*float64) *float64 {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func routeTools(tools []router.ToolDescriptor) []routeTool {
	if len(tools) == 0 {
		return nil
	}
	if len(tools) > maxRouteTools {
		tools = tools[:maxRouteTools]
	}
	out := make([]routeTool, 0, len(tools))
	seen := make(map[routeTool]struct{}, len(tools))
	for _, tool := range tools {
		wireTool := routeTool{
			Name:           clipRouteText(firstNonEmpty(tool.Name, tool.Type), maxRouteToolCallInputChars),
			Type:           clipRouteText(tool.Type, maxRouteToolCallInputChars),
			ServerExecuted: tool.ServerExecuted,
		}
		if wireTool.Name == "" {
			continue
		}
		if _, ok := seen[wireTool]; ok {
			continue
		}
		seen[wireTool] = struct{}{}
		out = append(out, wireTool)
	}
	return out
}

func clipRouteValues(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	if len(values) > maxRouteToolCallInputKeys {
		values = values[:maxRouteToolCallInputKeys]
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		clipped := clipRouteText(value, maxRouteToolCallInputChars)
		if clipped == "" {
			continue
		}
		if _, ok := seen[clipped]; ok {
			continue
		}
		seen[clipped] = struct{}{}
		out = append(out, clipped)
	}
	return out
}

var _ policy.Decider = (*Client)(nil)
var _ policy.PreviewDecider = (*Client)(nil)
var _ policy.OutcomeReporter = (*Client)(nil)
var _ policy.FeedbackReporter = (*Client)(nil)
