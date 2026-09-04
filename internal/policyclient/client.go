// Package policyclient implements the versioned HTTP contract shared by
// out-of-process policy routers.
package policyclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

// DefaultTimeout bounds a single delegated policy decision. It has to hold more
// than one full attempt: 2x1.8s + a 50ms backoff = 3.65s, so the old 3s starved
// attempt 2 and left attempt 3 unreachable.
const DefaultTimeout = 4500 * time.Millisecond

const (
	defaultRouteAttempts = 3
	routeRetryBackoff    = 50 * time.Millisecond
)

// Per-attempt bound so a stalled instance cannot consume the whole decision
// budget and prevent retries. Sized so a second full attempt still fits
// (2 x fraction x timeout + backoff <= timeout); three full attempts
// deliberately do not fit — the third covers fast failures only. The bound must
// exceed the sidecar's own inference deadline (HMM: 1.5s), or the router
// cancels a request it was about to answer. 0.4 x 4.5s = 1.8s satisfies both.
const (
	defaultAttemptFraction = 0.4
	minAttemptTimeout      = 500 * time.Millisecond
	// sidecarInferenceFloor is the smallest per-attempt bound that still
	// outlives a policy sidecar's own inference deadline (constant, not
	// fraction-scaling). Applied only when the budget can afford a retry too.
	sidecarInferenceFloor = 1800 * time.Millisecond
)

// Option customizes a policy sidecar client.
type Option func(*Client)

// WithAttemptTimeout bounds a single HTTP attempt. A non-positive duration
// lets one attempt consume the client's whole decision budget.
func WithAttemptTimeout(timeout time.Duration) Option {
	return func(c *Client) { c.attemptTimeout = timeout }
}

// DeriveAttemptTimeout is the per-attempt bound applied when none is
// configured.
func DeriveAttemptTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	attemptTimeout := time.Duration(float64(timeout) * defaultAttemptFraction)
	// Budgets too small to seat the floor and still leave a retry keep the old behaviour.
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
	maxRouteMessages           = 96
	maxRouteMessageTextChars   = 3000
	maxRouteMessageTotalChars  = 48000
	maxRouteTools              = 96
	maxRouteToolCallInputKeys  = 24
	maxRouteToolCallInputChars = 80
)

// Client calls a versioned policy sidecar.
type Client struct {
	baseURL        string
	client         *http.Client
	timeout        time.Duration
	attemptTimeout time.Duration
}

// New builds a policy sidecar client. A nil HTTP client uses a bounded default.
func New(baseURL string, client *http.Client, timeout time.Duration, opts ...Option) *Client {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if client == nil {
		// Same no-redirect policy as newGoogleIDTokenHTTPClient (auth.go):
		// a 3xx fails the != 200 status checks instead of being followed.
		client = &http.Client{
			Timeout: timeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
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

// CheckHealth verifies that the policy sidecar is ready to serve traffic.
func (c *Client) CheckHealth(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/readyz", nil)
	if err != nil {
		return fmt.Errorf("build policy readiness request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("call policy readiness endpoint: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("policy readiness status %d", resp.StatusCode)
	}
	return nil
}

// ReportOutcome posts final dispatch usage/status to the policy sidecar.
func (c *Client) ReportOutcome(ctx context.Context, payload map[string]interface{}) error {
	return c.post(ctx, "/outcome", payload, "outcome")
}

// ReportFeedback posts explicit request/session feedback to the policy sidecar.
func (c *Client) ReportFeedback(ctx context.Context, payload map[string]interface{}) error {
	return c.post(ctx, "/feedback", payload, "feedback")
}

// Capabilities fetches the sidecar's optional behavior declaration.
func (c *Client) Capabilities(ctx context.Context) (policy.Capabilities, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/capabilities", nil)
	if err != nil {
		return policy.Capabilities{}, fmt.Errorf("build policy capabilities request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return policy.Capabilities{}, fmt.Errorf("call policy capabilities endpoint: %w", err)
	}
	defer resp.Body.Close()
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

// rosterResponse is the shape of the sidecar's GET /roster body.
type rosterResponse struct {
	SchemaVersion string              `json:"schema_version"`
	RosterVersion string              `json:"roster_version"`
	RosterIDs     []string            `json:"roster_ids"`
	Clusters      map[string][]string `json:"clusters"`
}

// Roster fetches roster arm IDs from the sidecar; unlike the cluster
// artifact registry, this is the set the HMM strategy actually routes across.
func (c *Client) Roster(ctx context.Context) ([]string, error) {
	roster, err := c.fetchRoster(ctx)
	if err != nil {
		return nil, err
	}
	return roster.RosterIDs, nil
}

// ClusterRoster fetches the sidecar's frozen per-cluster arm roster.
func (c *Client) ClusterRoster(ctx context.Context) (policy.RosterSnapshot, error) {
	roster, err := c.fetchRoster(ctx)
	if err != nil {
		return policy.RosterSnapshot{}, err
	}
	return policy.RosterSnapshot{Clusters: roster.Clusters, RosterSHA256: roster.RosterVersion}, nil
}

func (c *Client) fetchRoster(ctx context.Context) (rosterResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/roster", nil)
	if err != nil {
		return rosterResponse{}, fmt.Errorf("build policy roster request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return rosterResponse{}, fmt.Errorf("call policy roster endpoint: %w", err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return rosterResponse{}, fmt.Errorf("read policy roster response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return rosterResponse{}, fmt.Errorf("policy roster status %d", resp.StatusCode)
	}
	var roster rosterResponse
	if err := json.Unmarshal(payload, &roster); err != nil {
		return rosterResponse{}, fmt.Errorf("decode policy roster response: %w", err)
	}
	if !supportedSchema(roster.SchemaVersion) {
		return rosterResponse{}, fmt.Errorf("unsupported policy roster schema %q", roster.SchemaVersion)
	}
	return roster, nil
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
	defer resp.Body.Close()
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
	Strategy                  string            `json:"strategy"`
	ExecutionMode             string            `json:"execution_mode"`
	RouteID                   string            `json:"route_id"`
	OrganizationID            string            `json:"organization_id,omitempty"`
	InstallationID            string            `json:"installation_id,omitempty"`
	ClientApp                 string            `json:"client_app,omitempty"`
	Harness                   string            `json:"harness,omitempty"`
	RolloutID                 string            `json:"rollout_id,omitempty"`
	RequestedModel            string            `json:"requested_model,omitempty"`
	PromptText                string            `json:"prompt_text"`
	LatestUserText            string            `json:"latest_user_text,omitempty"`
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

type routeResponse struct {
	SchemaVersion        string                 `json:"schema_version"`
	RouteID              string                 `json:"route_id"`
	SelectedArmID        string                 `json:"selected_arm_id"`
	SelectedRosterID     string                 `json:"selected_roster_id"`
	SelectedProvider     string                 `json:"selected_provider"`
	Model                string                 `json:"model"`
	Score                float64                `json:"score"`
	ChosenScore          *float64               `json:"chosen_score"`
	CandidateScores      map[string]float32     `json:"candidate_scores"`
	ScoreKind            string                 `json:"score_kind"`
	ScoreLabel           string                 `json:"score_label"`
	Reason               string                 `json:"reason"`
	PolicyState          string                 `json:"policy_state"`
	StateLabel           string                 `json:"state_label"`
	PolicyGroup          string                 `json:"policy_group"`
	Cluster              string                 `json:"cluster"`
	PolicyLabel          string                 `json:"policy_label"`
	ComplexityLabel      string                 `json:"complexity_label"`
	PolicyRouteKey       string                 `json:"policy_route_key"`
	RoutingBucket        string                 `json:"routing_bucket"`
	Confidence           *float64               `json:"confidence"`
	ClassifierConfidence *float64               `json:"classifier_confidence"`
	Margin               *float64               `json:"margin"`
	ClassifierMargin     *float64               `json:"classifier_margin"`
	Propensity           float64                `json:"propensity"`
	DisplayMarker        string                 `json:"display_marker"`
	PolicyArtifactID     string                 `json:"policy_artifact_id"`
	PolicyModelID        string                 `json:"policy_model_id"`
	PolicyArtifactSHA256 string                 `json:"policy_artifact_sha256"`
	PolicySHA256         string                 `json:"policy_sha256"`
	RosterVersion        string                 `json:"roster_version"`
	DebugRef             string                 `json:"debug_ref"`
	Debug                map[string]interface{} `json:"debug"`
	RankedFallback       []policy.PreviewGroup  `json:"ranked_fallback"`
	ArmScores            map[string]float32     `json:"arm_scores"`
	PredictedLabel       string                 `json:"predicted_label"`
	ClassProbabilities   map[string]float64     `json:"class_probabilities"`
	Timings              *routeTimings          `json:"timings"`
	Error                string                 `json:"error"`
}

// routeTimings is the sidecar's optional per-request latency breakdown plus
// serving stats. Fields are nil (not zero) when not measured/reported;
// route_ms spans the whole decision and is a superset of the other stages.
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

// decomposeTimings converts sidecar wire timings into non-overlapping stages; nil when nothing was measured.
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
			// Stages exceeding the total they were measured inside of mean the
			// breakdown can't be trusted for disjoint attribution; drop it.
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

// extractServingStats reads the sidecar's serving stats from the same wire
// payload as decomposeTimings, but separately: stats are exempt from the
// route_ms consistency check that can drop the latency breakdown wholesale.
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

// Decide posts the supplied candidate set and returns the sidecar selection.
func (c *Client) Decide(ctx context.Context, query policy.Query) (policy.Result, error) {
	body, err := marshalRouteRequest(query)
	if err != nil {
		return policy.Result{}, err
	}

	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	resp, payload, err := c.doPolicyRequest(requestCtx, "/route", body)
	if err != nil {
		return policy.Result{}, err
	}

	var parsed routeResponse
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return policy.Result{}, fmt.Errorf("decode policy route response (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK {
		if parsed.Error != "" {
			return policy.Result{}, fmt.Errorf("policy sidecar status %d: %s", resp.StatusCode, parsed.Error)
		}
		return policy.Result{}, fmt.Errorf("policy sidecar status %d", resp.StatusCode)
	}
	selectedModel := firstNonEmpty(parsed.SelectedRosterID, parsed.Model)
	switch parsed.SchemaVersion {
	case policy.SchemaVersionV3:
		// Classifier-only contract: the caller selects the arm, so a response
		// naming one is a contract violation rather than a harmless extra.
		if parsed.SelectedArmID != "" || selectedModel != "" {
			return policy.Result{}, fmt.Errorf("policy sidecar returned a selected arm on schema %s", policy.SchemaVersionV3)
		}
		if len(parsed.RankedFallback) == 0 {
			return policy.Result{}, fmt.Errorf("policy sidecar returned no ranked fallback on schema %s", policy.SchemaVersionV3)
		}
	case "", policy.SchemaVersionV1, policy.SchemaVersionV2:
		if parsed.SelectedArmID == "" && selectedModel == "" {
			return policy.Result{}, fmt.Errorf("policy sidecar returned empty arm and model")
		}
	default:
		return policy.Result{}, fmt.Errorf("unsupported policy route schema %q", parsed.SchemaVersion)
	}
	score := parsed.Score
	if parsed.ChosenScore != nil {
		score = *parsed.ChosenScore
	}
	return policy.Result{
		SchemaVersion:        parsed.SchemaVersion,
		RouteID:              parsed.RouteID,
		ArmID:                parsed.SelectedArmID,
		Model:                selectedModel,
		Provider:             parsed.SelectedProvider,
		Score:                score,
		CandidateScores:      parsed.CandidateScores,
		ScoreKind:            firstNonEmpty(parsed.ScoreKind, parsed.ScoreLabel),
		Reason:               parsed.Reason,
		PolicyState:          firstNonEmpty(parsed.PolicyState, parsed.StateLabel),
		PolicyGroup:          firstNonEmpty(parsed.PolicyGroup, parsed.Cluster),
		PolicyLabel:          firstNonEmpty(parsed.PolicyLabel, parsed.ComplexityLabel),
		PolicyRouteKey:       firstNonEmpty(parsed.PolicyRouteKey, parsed.RoutingBucket),
		Confidence:           firstFloat(parsed.Confidence, parsed.ClassifierConfidence),
		Margin:               firstFloat(parsed.Margin, parsed.ClassifierMargin),
		Propensity:           parsed.Propensity,
		DisplayMarker:        parsed.DisplayMarker,
		PolicyArtifactID:     firstNonEmpty(parsed.PolicyArtifactID, parsed.PolicyModelID),
		PolicyArtifactSHA256: firstNonEmpty(parsed.PolicyArtifactSHA256, parsed.PolicySHA256),
		RosterVersion:        parsed.RosterVersion,
		DebugRef:             parsed.DebugRef,
		Debug:                parsed.Debug,
		RankedFallback:       parsed.RankedFallback,
		ArmScores:            parsed.ArmScores,
		PredictedLabel:       parsed.PredictedLabel,
		ClassProbabilities:   parsed.ClassProbabilities,
		Timings:              decomposeTimings(parsed.Timings),
		ServingStats:         extractServingStats(parsed.Timings),
	}, nil
}

// Preview evaluates the supplied candidate set without serving or callbacks.
func (c *Client) Preview(ctx context.Context, query policy.Query) (policy.PreviewResult, error) {
	if query.ExecutionMode != policy.ExecutionModePreview {
		return policy.PreviewResult{}, fmt.Errorf("policy preview requires preview execution mode")
	}
	body, err := marshalRouteRequest(query)
	if err != nil {
		return policy.PreviewResult{}, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	resp, payload, err := c.doPolicyRequest(requestCtx, "/preview", body)
	if err != nil {
		return policy.PreviewResult{}, err
	}
	var parsed previewResponse
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return policy.PreviewResult{}, fmt.Errorf("decode policy preview response (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK {
		if parsed.Error != "" {
			return policy.PreviewResult{}, fmt.Errorf("policy preview status %d: %s", resp.StatusCode, parsed.Error)
		}
		return policy.PreviewResult{}, fmt.Errorf("policy preview status %d", resp.StatusCode)
	}
	if !supportedSchema(parsed.SchemaVersion) {
		return policy.PreviewResult{}, fmt.Errorf("unsupported policy preview schema %q", parsed.SchemaVersion)
	}
	return policy.PreviewResult{
		SchemaVersion:         parsed.SchemaVersion,
		RouteID:               parsed.RouteID,
		PolicyArtifactID:      parsed.PolicyArtifactID,
		PolicyArtifactSHA256:  parsed.PolicyArtifactSHA256,
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

// supportedSchema reports whether a sidecar declares a wire contract this
// client speaks.
func supportedSchema(version string) bool {
	switch version {
	case policy.SchemaVersionV1, policy.SchemaVersionV2, policy.SchemaVersionV3:
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
		providerKey := candidate.RosterID
		if schemaVersion == policy.SchemaVersionV2 {
			providerKey = candidate.ArmID
		}
		models = append(models, providerKey)
		providerMap[providerKey] = candidate.Provider
	}
	messages := routeMessages(query.ConversationMessages)
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
				routeMessagesTruncated(query.ConversationMessages),
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
	body, err := json.Marshal(routeRequest{
		SchemaVersion:             schemaVersion,
		Strategy:                  string(query.Strategy),
		ExecutionMode:             query.ExecutionMode,
		RouteID:                   query.RouteID,
		OrganizationID:            query.OrganizationID,
		InstallationID:            query.InstallationID,
		ClientApp:                 query.ClientApp,
		Harness:                   query.ClientApp,
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
		AvailableTools:            clipRouteValues(query.AvailableTools, maxRouteToolCallInputKeys, maxRouteToolCallInputChars),
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
		if schemaVersion == policy.SchemaVersionV2 {
			bindingIndex := candidate.BindingIndex
			wireCandidate.ArmID = candidate.ArmID
			wireCandidate.BindingIndex = &bindingIndex
			wireCandidate.Endpoint = candidate.Endpoint
			wireCandidate.ModelRevision = candidate.ModelRevision
			wireCandidate.ReasoningConfigurationSHA256 = candidate.ReasoningConfigurationSHA256
			wireCandidate.ToolConfigurationSHA256 = candidate.ToolConfigurationSHA256
		}
		result = append(result, wireCandidate)
	}
	return result
}

func pointerTo[T any](value T) *T {
	return &value
}

func (c *Client) doPolicyRequest(ctx context.Context, path string, body []byte) (*http.Response, []byte, error) {
	// lastStatusErr is the sidecar's own diagnosis; it diverges from lastErr when the budget cuts a final attempt short.
	var lastErr, lastStatusErr error
	for attempt := 1; attempt <= defaultRouteAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, exhaustedPolicyErr(lastStatusErr, lastErr, err)
		}
		resp, payload, done, attemptErr := c.doPolicyAttempt(ctx, attempt, path, body)
		if done {
			return resp, payload, attemptErr
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
			return nil, nil, exhaustedPolicyErr(lastStatusErr, lastErr, ctx.Err())
		case <-timer.C:
		}
	}
	return nil, nil, exhaustedPolicyErr(lastStatusErr, lastErr, nil)
}

// preferStatusErr picks the sidecar's own status over a transport failure, so an
// exhausted ladder surfaces the sidecar's diagnosis and not the budget expiry.
func preferStatusErr(statusErr, lastErr error) error {
	if statusErr != nil {
		return statusErr
	}
	return lastErr
}

// exhaustedPolicyErr reports why the ladder ended. Both the sidecar's status and
// the deadline are preserved: the status is the sidecar's own diagnosis; the
// deadline keeps isPolicyDeadlineErr matching so the policy-deadline fallback
// still degrades rather than surfacing the 503 that fallback exists to prevent.
func exhaustedPolicyErr(statusErr, lastErr, ctxErr error) error {
	primary := preferStatusErr(statusErr, lastErr)
	deadline := cancellationSignal(ctxErr, lastErr, statusErr)
	switch {
	case primary == nil && deadline == nil:
		return errors.New("policy sidecar retries exhausted")
	case primary == nil:
		return fmt.Errorf("policy sidecar retries exhausted: %w", deadline)
	// errors.Is keeps a transport deadline from being printed twice; the
	// sentinel is already reachable through primary.
	case deadline == nil || errors.Is(primary, deadline):
		return fmt.Errorf("policy sidecar retries exhausted: %w", primary)
	default:
		return fmt.Errorf("policy sidecar retries exhausted: %w: %w", primary, deadline)
	}
}

// cancellationSignal returns the first context sentinel reachable from any
// candidate, so preferring a status error for its diagnosis never costs the
// caller the deadline it needs to degrade on.
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

// doPolicyAttempt runs one bounded attempt; returns done=true on a final outcome (success or fatal error).
func (c *Client) doPolicyAttempt(
	ctx context.Context,
	attempt int,
	path string,
	body []byte,
) (*http.Response, []byte, bool, error) {
	attemptCtx, cancel := c.attemptContext(ctx)
	defer cancel()

	req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, nil, true, fmt.Errorf("build policy route request: %w", err)
	}
	req.Header.Set("content-type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, nil, false, c.attemptError(ctx, attemptCtx, attempt, fmt.Errorf("call policy sidecar: %w", err))
	}
	payload, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if readErr != nil {
		return nil, nil, false, c.attemptError(ctx, attemptCtx, attempt, fmt.Errorf("read policy route response: %w", readErr))
	}
	if !isTransientPolicyStatus(resp.StatusCode) {
		return resp, payload, true, nil
	}
	return nil, nil, false, policyStatusError(resp.StatusCode, payload)
}

// attemptContext bounds one attempt so a single stalled instance cannot spend
// the whole decision budget. The parent deadline still caps the attempt.
func (c *Client) attemptContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.attemptTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, c.attemptTimeout)
}

// attemptError distinguishes per-attempt-bound timeouts from sidecar failures; always wraps context.DeadlineExceeded so callers can degrade on it.
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

// PolicyStatusError is a non-2xx response from the sidecar, typed so the retry
// ladder can prefer it over a transport error: a status is the sidecar's own
// diagnosis; "context deadline exceeded" only says the budget ran out.
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
	maxMessages           int
	maxTextChars          int
	maxTotalTextChars     int
	maxToolCallInputKeys  int
	maxToolCallInputChars int
	includeToolCallInput  bool
	includeToolResultText bool
}

func routeMessages(messages []router.ConversationMessage) []routeMessage {
	return convertRouteMessages(messages, routeMessageLimits{
		maxMessages:           maxRouteMessages,
		maxTextChars:          maxRouteMessageTextChars,
		maxTotalTextChars:     maxRouteMessageTotalChars,
		maxToolCallInputKeys:  maxRouteToolCallInputKeys,
		maxToolCallInputChars: maxRouteToolCallInputChars,
	})
}

func routeMessagesTruncated(messages []router.ConversationMessage) bool {
	if len(messages) > maxRouteMessages {
		return true
	}
	totalText := 0
	for _, message := range messages {
		text := strings.TrimSpace(message.Text)
		if len(text) > maxRouteMessageTextChars {
			return true
		}
		totalText += len(text)
		if totalText > maxRouteMessageTotalChars {
			return true
		}
		for _, call := range message.ToolCalls {
			if len(strings.TrimSpace(call.Name)) > maxRouteToolCallInputChars ||
				len(call.InputKeys) > maxRouteToolCallInputKeys {
				return true
			}
			for _, key := range call.InputKeys {
				if len(strings.TrimSpace(key)) > maxRouteToolCallInputChars {
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

	// Each route happens before its next assistant response. Preserve the new
	// exchange since the last response so training can reconstruct the turn.
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

// routeMessageWindow keeps the newest maxMessages messages, except that the
// newest user message carrying text is always kept: a long agent tool loop can
// push every text-bearing user turn out of the window, and a routing history
// without one describes no request at all.
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

// userTextIndex is the index of the newest user message carrying text, or -1.
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
	// The oldest message is converted last, so the text budget reserves room for
	// it: otherwise the pulled-back user boundary arrives with empty text and is
	// dropped, which is the same wire shape the window guard exists to prevent.
	boundaryReserve := 0
	if boundary := userTextIndex(messages); boundary == 0 && limits.maxTotalTextChars > 0 {
		boundaryReserve = len(clipRouteText(messages[0].Text, limits.maxTextChars))
	}
	reversed := make([]routeMessage, 0, len(messages))
	totalText := 0
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		role := routeRole(message.Role)
		if role == "" {
			continue
		}
		text := clipRouteText(message.Text, limits.maxTextChars)
		if limits.maxTotalTextChars > 0 {
			budget := limits.maxTotalTextChars
			if i > 0 {
				budget -= boundaryReserve
			}
			if remaining := budget - totalText; remaining < len(text) {
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

func turnIndex(messages []routeMessage) int {
	count := 0
	for _, message := range messages {
		if message.Role == "user" && strings.TrimSpace(message.Text) != "" {
			count++
		}
	}
	if count <= 1 {
		return 0
	}
	return count - 1
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
			Name:           clipRouteText(tool.Name, maxRouteToolCallInputChars),
			Type:           clipRouteText(tool.Type, maxRouteToolCallInputChars),
			ServerExecuted: tool.ServerExecuted,
		}
		if wireTool.Name == "" && wireTool.Type == "" {
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

func clipRouteValues(values []string, maxValues, maxChars int) []string {
	if len(values) == 0 {
		return nil
	}
	if maxValues > 0 && len(values) > maxValues {
		values = values[:maxValues]
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		clipped := clipRouteText(value, maxChars)
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
var _ policy.RosterSource = (*Client)(nil)
