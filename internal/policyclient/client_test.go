package policyclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"workweave/router/internal/providers"
	"workweave/router/internal/router"
	"workweave/router/internal/router/policy"
)

func TestClientPostsVersionedRouteAndParsesPolicyMetadata(t *testing.T) {
	var got routeRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/route", request.URL.Path)
		require.NoError(t, json.NewDecoder(request.Body).Decode(&got))
		_ = json.NewEncoder(w).Encode(routeResponse{
			SchemaVersion:        policy.SchemaVersionV1,
			RouteID:              "route-1",
			SelectedArmID:        "arm-kimi-fireworks",
			SelectedRosterID:     "moonshotai/kimi-k2.7-code",
			SelectedProvider:     providers.ProviderFireworks,
			ChosenScore:          floatPtr(0.91),
			CandidateScores:      map[string]float32{"moonshotai/kimi-k2.7-code": 0.91},
			ScoreLabel:           "classifier_confidence",
			Cluster:              "balanced",
			ComplexityLabel:      "balanced",
			RoutingBucket:        "balanced|open",
			ClassifierConfidence: floatPtr(0.91),
			ClassifierMargin:     floatPtr(0.22),
			Propensity:           1,
			PolicyArtifactID:     "hmm-prod",
			PolicyArtifactSHA256: "sha256:abc",
			RosterVersion:        "roster-v2",
			DebugRef:             "debug-1",
		})
	}))
	defer server.Close()

	qualityBias := 0.7
	preferenceRank := 0
	client := New(server.URL, server.Client(), 0)
	result, err := client.Decide(context.Background(), policy.Query{
		Strategy:        router.StrategyHMMEmbedding,
		ExecutionMode:   policy.ExecutionModeShadow,
		RouteID:         "route-1",
		OrganizationID:  "org-1",
		InstallationID:  "installation-1",
		ClientApp:       "codex",
		RolloutID:       "rollout-1",
		RequestedModel:  "Weave",
		PromptText:      "hello",
		RoutingIntent:   "high",
		PreferredModels: []string{"moonshotai/kimi-k2.7"},
		RoutingKnobs:    &router.Overrides{QualityBias: &qualityBias},
		DebugEnabled:    true,
		ConversationMessages: []router.ConversationMessage{
			{Role: "user", Text: "please explore the repo"},
			{Role: "assistant", Text: "done"},
			{Role: "user", ToolResults: []router.ConversationToolResult{{
				ToolUseID:     "toolu_123",
				Text:          "full tool result",
				ResultPresent: true,
				CharCount:     16,
				ByteCount:     16,
				ExitCategory:  "success",
			}}},
			{Role: "user", Text: "latest hello", ToolCalls: []router.ConversationToolCall{{Name: "Read", InputKeys: []string{"file_path"}, InputJSON: `{"file_path":"README.md"}`}}},
		},
		TurnContext: &router.PolicyTurnContext{
			VisibleTurnIndex:    7,
			SessionTurnCount:    9,
			TurnType:            "sub_agent_dispatch",
			PreviousServedModel: "claude-opus-4-8",
			PreviousProvider:    providers.ProviderAnthropic,
			CacheState:          router.PolicyCacheStateWarm,
			PriorOutputTokens:   intPointer(321),
			SessionEverSwitched: true,
			HistoryTruncated:    true,
		},
		AvailableTools: []string{"Read", "Grep", "Read", ""},
		Tools: []router.ToolDescriptor{
			{Name: "WebSearch", Type: "web_search_20260318", ServerExecuted: true},
		},
		FeedbackKey:     "feedback-session",
		FeedbackRole:    "default",
		ClientSessionID: "client-session-abc",
		TrainingAllowed: true,
		Candidates: []policy.Candidate{{
			ArmID:          "arm-kimi-fireworks",
			RosterID:       "moonshotai/kimi-k2.7-code",
			CatalogID:      "moonshotai/kimi-k2.7",
			Provider:       providers.ProviderFireworks,
			UpstreamID:     "accounts/fireworks/models/kimi-k2p5",
			PreferenceRank: &preferenceRank,
		}},
	})

	require.NoError(t, err)
	assert.Equal(t, policy.SchemaVersionV1, got.SchemaVersion)
	assert.Equal(t, string(router.StrategyHMMEmbedding), got.Strategy)
	assert.Equal(t, policy.ExecutionModeShadow, got.ExecutionMode)
	assert.Equal(t, "org-1", got.OrganizationID)
	assert.Equal(t, "installation-1", got.InstallationID)
	assert.Equal(t, "codex", got.ClientApp)
	assert.Equal(t, "codex", got.Harness)
	assert.Equal(t, "rollout-1", got.RolloutID)
	assert.Equal(t, "high", got.RoutingIntent)
	assert.Equal(t, []string{"moonshotai/kimi-k2.7"}, got.PreferredModels)
	require.NotNil(t, got.QualityBias)
	assert.Equal(t, 0.7, *got.QualityBias)
	assert.True(t, got.TrainingAllowed)
	assert.True(t, got.DebugEnabled)
	assert.Equal(t, "latest hello", got.LatestUserText)
	assert.Equal(t, 7, got.TurnIndex)
	require.NotNil(t, got.VisibleTurnIndex)
	assert.Equal(t, 7, *got.VisibleTurnIndex)
	require.NotNil(t, got.SessionTurnCount)
	assert.Equal(t, 9, *got.SessionTurnCount)
	assert.Equal(t, "sub_agent_dispatch", got.TurnType)
	assert.Equal(t, "claude-opus-4-8", got.PreviousServedModel)
	assert.Equal(t, providers.ProviderAnthropic, got.PreviousProvider)
	assert.Equal(t, router.PolicyCacheStateWarm, got.CacheState)
	require.NotNil(t, got.PriorOutputTokens)
	assert.Equal(t, 321, *got.PriorOutputTokens)
	require.NotNil(t, got.SessionEverSwitched)
	assert.True(t, *got.SessionEverSwitched)
	require.NotNil(t, got.HistoryTruncated)
	assert.True(t, *got.HistoryTruncated)
	assert.Equal(t, []string{"Read", "Grep"}, got.AvailableTools)
	assert.Equal(t, []routeTool{{Name: "WebSearch", Type: "web_search_20260318", ServerExecuted: true}}, got.Tools)
	assert.True(t, got.IsSubagent)
	assert.Equal(t, "feedback-session", got.FeedbackKey)
	assert.Equal(t, "default", got.FeedbackRole)
	assert.Equal(t, "client-session-abc", got.ClientSessionID)
	require.Len(t, got.TrainingConversationDelta, 3)
	assert.Equal(t, "assistant", got.TrainingConversationDelta[0].Role)
	require.Len(t, got.TrainingConversationDelta[1].ToolResults, 1)
	assert.Equal(t, "full tool result", got.TrainingConversationDelta[1].ToolResults[0].Text)
	require.Len(t, got.TrainingConversationDelta[2].ToolCalls, 1)
	assert.Equal(t, `{"file_path":"README.md"}`, got.TrainingConversationDelta[2].ToolCalls[0].InputJSON)
	assert.Empty(t, got.ConversationMessages[2].ToolResults[0].Text)
	assert.True(t, got.ConversationMessages[2].ToolResults[0].ResultPresent)
	assert.Equal(t, 16, got.ConversationMessages[2].ToolResults[0].CharCount)
	assert.Equal(t, "success", got.ConversationMessages[2].ToolResults[0].ExitCategory)
	assert.Empty(t, got.ConversationMessages[3].ToolCalls[0].InputJSON)
	require.Len(t, got.Candidates, 1)
	assert.Empty(t, got.Candidates[0].ArmID)
	assert.Nil(t, got.Candidates[0].BindingIndex)
	assert.Equal(t, "moonshotai/kimi-k2.7", got.Candidates[0].CatalogID)
	assert.Equal(t, "accounts/fireworks/models/kimi-k2p5", got.Candidates[0].UpstreamID)
	assert.Equal(t, "balanced|open", result.PolicyRouteKey)
	assert.Equal(t, providers.ProviderFireworks, result.Provider)
	assert.Equal(t, "hmm-prod", result.PolicyArtifactID)
	assert.Equal(t, "sha256:abc", result.PolicyArtifactSHA256)
	assert.Equal(t, "roster-v2", result.RosterVersion)
	assert.Equal(t, "arm-kimi-fireworks", result.ArmID)
	assert.Equal(t, "debug-1", result.DebugRef)
	assert.Equal(t, 0.91, result.Score)
	assert.Equal(t, map[string]float32{"moonshotai/kimi-k2.7-code": 0.91}, result.CandidateScores)
}

func decideWithTimings(t *testing.T, timings *routeTimings) policy.Result {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(routeResponse{
			SelectedRosterID: "anthropic/claude-opus-4-8",
			Timings:          timings,
		})
	}))
	defer server.Close()

	result, err := New(server.URL, server.Client(), 0).Decide(context.Background(), policy.Query{
		Candidates: []policy.Candidate{{RosterID: "anthropic/claude-opus-4-8", CatalogID: "claude-opus-4-8", Provider: providers.ProviderAnthropic}},
	})
	require.NoError(t, err)
	return result
}

func TestClientDecideDecomposesFullTimingsIntoDisjointStagesSummingToRouteMs(t *testing.T) {
	const routeMs = 20.0
	result := decideWithTimings(t, &routeTimings{RouteMs: floatPtr(routeMs), SelectMs: floatPtr(3.5), EmbedMs: floatPtr(12.5)})

	require.NotNil(t, result.Timings)
	require.NotNil(t, result.Timings.EmbedMs)
	require.NotNil(t, result.Timings.SelectMs)
	require.NotNil(t, result.Timings.OtherMs)
	assert.Equal(t, 12.5, *result.Timings.EmbedMs)
	assert.Equal(t, 3.5, *result.Timings.SelectMs)
	assert.Equal(t, routeMs-12.5-3.5, *result.Timings.OtherMs)
	assert.Equal(t, routeMs, *result.Timings.EmbedMs+*result.Timings.SelectMs+*result.Timings.OtherMs)
}

func TestClientDecideLeavesSelectAndOtherNilForEmbedOnlyTimings(t *testing.T) {
	result := decideWithTimings(t, &routeTimings{EmbedMs: floatPtr(12.5)})

	require.NotNil(t, result.Timings)
	require.NotNil(t, result.Timings.EmbedMs)
	assert.Equal(t, 12.5, *result.Timings.EmbedMs)
	assert.Nil(t, result.Timings.SelectMs)
	assert.Nil(t, result.Timings.OtherMs)
}

func TestClientDecidePreservesPresentZeroEmbedMsAndComputesOtherFromRoute(t *testing.T) {
	result := decideWithTimings(t, &routeTimings{RouteMs: floatPtr(5), SelectMs: floatPtr(2), EmbedMs: floatPtr(0)})

	require.NotNil(t, result.Timings)
	require.NotNil(t, result.Timings.EmbedMs)
	require.NotNil(t, result.Timings.OtherMs)
	assert.Equal(t, 0.0, *result.Timings.EmbedMs)
	assert.Equal(t, 3.0, *result.Timings.OtherMs)
}

func TestClientDecideLeavesTimingsNilWhenOmittedEntirely(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"model": "anthropic/claude-opus-4-8"})
	}))
	defer server.Close()

	result, err := New(server.URL, server.Client(), 0).Decide(context.Background(), policy.Query{
		Candidates: []policy.Candidate{{RosterID: "anthropic/claude-opus-4-8", CatalogID: "claude-opus-4-8", Provider: providers.ProviderAnthropic}},
	})

	require.NoError(t, err)
	assert.Nil(t, result.Timings)
}

func TestClientDecideDropsTimingsWhenStagesExceedRouteMs(t *testing.T) {
	result := decideWithTimings(t, &routeTimings{RouteMs: floatPtr(5), SelectMs: floatPtr(4), EmbedMs: floatPtr(3)})

	assert.Nil(t, result.Timings, "an inconsistent breakdown must be dropped, not published with overlapping stages")
}

func TestClientDecidePopulatesServingStatsFromTimings(t *testing.T) {
	result := decideWithTimings(t, &routeTimings{
		RouteMs:             floatPtr(20),
		SelectMs:            floatPtr(3.5),
		EmbedMs:             floatPtr(12.5),
		EmbedCacheHits:      int64Ptr(42),
		EmbedCacheMisses:    int64Ptr(0),
		EmbedCacheEvictions: int64Ptr(3),
		RoutesInflight:      int64Ptr(7),
		OverrunsLive:        int64Ptr(1),
	})

	require.NotNil(t, result.ServingStats)
	require.NotNil(t, result.ServingStats.EmbedCacheHits)
	require.NotNil(t, result.ServingStats.EmbedCacheMisses, "a measured zero must be preserved as present-0, not collapsed to nil")
	require.NotNil(t, result.ServingStats.EmbedCacheEvictions)
	require.NotNil(t, result.ServingStats.RoutesInflight)
	require.NotNil(t, result.ServingStats.OverrunsLive)
	assert.Equal(t, int64(42), *result.ServingStats.EmbedCacheHits)
	assert.Equal(t, int64(0), *result.ServingStats.EmbedCacheMisses)
	assert.Equal(t, int64(3), *result.ServingStats.EmbedCacheEvictions)
	assert.Equal(t, int64(7), *result.ServingStats.RoutesInflight)
	assert.Equal(t, int64(1), *result.ServingStats.OverrunsLive)
}

func TestClientDecideLeavesServingStatsNilWhenStatsAbsent(t *testing.T) {
	result := decideWithTimings(t, &routeTimings{RouteMs: floatPtr(20), SelectMs: floatPtr(3.5), EmbedMs: floatPtr(12.5)})

	assert.Nil(t, result.ServingStats, "a sidecar that only reports latency must not synthesize serving stats")
}

func TestClientDecideServingStatsSurviveUntrustedLatencyBreakdown(t *testing.T) {
	result := decideWithTimings(t, &routeTimings{
		RouteMs:             floatPtr(5),
		SelectMs:            floatPtr(4),
		EmbedMs:             floatPtr(3),
		EmbedCacheHits:      int64Ptr(10),
		EmbedCacheMisses:    int64Ptr(2),
		EmbedCacheEvictions: int64Ptr(0),
		RoutesInflight:      int64Ptr(5),
		OverrunsLive:        int64Ptr(0),
	})

	assert.Nil(t, result.Timings, "an inconsistent breakdown must still be dropped")
	require.NotNil(t, result.ServingStats, "stats live in a sibling struct precisely so the latency-consistency check can't drop them along with a distrusted breakdown")
	require.NotNil(t, result.ServingStats.EmbedCacheHits)
	require.NotNil(t, result.ServingStats.EmbedCacheMisses)
	require.NotNil(t, result.ServingStats.EmbedCacheEvictions, "a measured zero must be preserved as present-0, not collapsed to nil")
	require.NotNil(t, result.ServingStats.RoutesInflight)
	require.NotNil(t, result.ServingStats.OverrunsLive, "a measured zero must be preserved as present-0, not collapsed to nil")
	assert.Equal(t, int64(10), *result.ServingStats.EmbedCacheHits)
	assert.Equal(t, int64(2), *result.ServingStats.EmbedCacheMisses)
	assert.Equal(t, int64(0), *result.ServingStats.EmbedCacheEvictions)
	assert.Equal(t, int64(5), *result.ServingStats.RoutesInflight)
	assert.Equal(t, int64(0), *result.ServingStats.OverrunsLive)
}

func TestClientDecidePopulatesServingStatsWhenOnlyStatsReported(t *testing.T) {
	result := decideWithTimings(t, &routeTimings{
		EmbedCacheHits:      int64Ptr(1),
		EmbedCacheMisses:    int64Ptr(1),
		EmbedCacheEvictions: int64Ptr(1),
		RoutesInflight:      int64Ptr(1),
		OverrunsLive:        int64Ptr(1),
	})

	assert.Nil(t, result.Timings, "no latency fields were reported, so there is nothing for decomposeTimings to build")
	assert.NotNil(t, result.ServingStats, "stats do not depend on any latency field being present")
}

func TestClientOmitsV2CandidateFieldsFromV1(t *testing.T) {
	body, err := marshalRouteRequest(policy.Query{
		SchemaVersion: policy.SchemaVersionV1,
		Candidates: []policy.Candidate{{
			ArmID:                        "arm-fireworks",
			RosterID:                     "deepseek/deepseek-v4-pro",
			CatalogID:                    "deepseek/deepseek-v4-pro",
			Provider:                     providers.ProviderFireworks,
			BindingIndex:                 0,
			Endpoint:                     string(router.EndpointAnthropicMessages),
			ModelRevision:                "2026-07-20",
			ReasoningConfigurationSHA256: "reasoning-hash",
			ToolConfigurationSHA256:      "tool-hash",
		}},
	})

	require.NoError(t, err)
	var payload struct {
		Candidates []map[string]json.RawMessage `json:"candidates"`
	}
	require.NoError(t, json.Unmarshal(body, &payload))
	require.Len(t, payload.Candidates, 1)
	for _, field := range []string{
		"arm_id",
		"binding_index",
		"endpoint",
		"model_revision",
		"reasoning_configuration_sha256",
		"tool_configuration_sha256",
	} {
		assert.NotContains(t, payload.Candidates[0], field)
	}
}

func TestClientPostsArmProviderMapForV2(t *testing.T) {
	var got routeRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		require.NoError(t, json.NewDecoder(request.Body).Decode(&got))
		_ = json.NewEncoder(w).Encode(routeResponse{
			SchemaVersion:    policy.SchemaVersionV2,
			SelectedArmID:    "arm-fireworks",
			SelectedRosterID: "deepseek/deepseek-v4-pro",
		})
	}))
	defer server.Close()

	_, err := New(server.URL, server.Client(), 0).Decide(context.Background(), policy.Query{
		SchemaVersion: policy.SchemaVersionV2,
		Candidates: []policy.Candidate{
			{
				ArmID:        "arm-fireworks",
				RosterID:     "deepseek/deepseek-v4-pro",
				CatalogID:    "deepseek/deepseek-v4-pro",
				Provider:     providers.ProviderFireworks,
				BindingIndex: 0,
			},
			{
				ArmID:        "arm-makora",
				RosterID:     "deepseek/deepseek-v4-pro",
				CatalogID:    "deepseek/deepseek-v4-pro",
				Provider:     providers.ProviderMakora,
				BindingIndex: 1,
			},
		},
	})

	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"arm-fireworks": providers.ProviderFireworks,
		"arm-makora":    providers.ProviderMakora,
	}, got.CandidateProviders)
	assert.Equal(t, []string{"arm-fireworks", "arm-makora"}, got.CandidateModels)
	require.NotNil(t, got.Candidates[0].BindingIndex)
	assert.Equal(t, 0, *got.Candidates[0].BindingIndex)
	assert.Equal(t, "arm-fireworks", got.Candidates[0].ArmID)
}

func TestClientPreviewAcceptsV2Schema(t *testing.T) {
	var got routeRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		require.NoError(t, json.NewDecoder(request.Body).Decode(&got))
		_ = json.NewEncoder(w).Encode(previewResponse{
			SchemaVersion: policy.SchemaVersionV2,
		})
	}))
	defer server.Close()

	result, err := New(server.URL, server.Client(), 0).Preview(context.Background(), policy.Query{
		SchemaVersion: policy.SchemaVersionV2,
		ExecutionMode: policy.ExecutionModePreview,
		Candidates:    []policy.Candidate{{ArmID: "arm-a", RosterID: "model-a"}},
	})

	require.NoError(t, err)
	assert.Equal(t, policy.SchemaVersionV2, got.SchemaVersion)
	assert.Equal(t, policy.SchemaVersionV2, result.SchemaVersion)
}

func TestClientAcceptsLegacyRouteResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"model": "anthropic/claude-opus-4-8"})
	}))
	defer server.Close()

	result, err := New(server.URL, server.Client(), 0).Decide(context.Background(), policy.Query{
		Candidates: []policy.Candidate{{RosterID: "anthropic/claude-opus-4-8", CatalogID: "claude-opus-4-8", Provider: providers.ProviderAnthropic}},
	})

	require.NoError(t, err)
	assert.Equal(t, "anthropic/claude-opus-4-8", result.Model)
	assert.Empty(t, result.SchemaVersion)
}

func TestClientPreviewPostsNonLearningRequestAndReturnsEveryArm(t *testing.T) {
	var got routeRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/preview", request.URL.Path)
		require.NoError(t, json.NewDecoder(request.Body).Decode(&got))
		_ = json.NewEncoder(w).Encode(previewResponse{
			SchemaVersion:         policy.SchemaVersionV1,
			RouteID:               "preview-1",
			PolicyArtifactID:      "hmm-prod",
			PolicyArtifactSHA256:  "sha256:artifact",
			RosterSHA256:          "sha256:roster",
			HMMStateID:            7,
			HMMStatePath:          []int{2, 7},
			HMMStateProbabilities: []float64{0.01, 0.01, 0.05, 0.01, 0.01, 0.01, 0.1, 0.8},
			ClassOrder:            []string{"hard", "balanced"},
			ClassProbabilities:    map[string]float64{"hard": 0.75, "balanced": 0.25},
			RankedFallback: []policy.PreviewGroup{{
				Group:        "hard",
				Probability:  0.75,
				RosterArms:   []string{"arm-a", "arm-b"},
				EligibleArms: []string{"arm-a", "arm-b"},
			}},
			SelectedGroup:     "hard",
			EligibleRosterIDs: []string{"arm-a", "arm-b"},
		})
	}))
	defer server.Close()

	result, err := New(server.URL, server.Client(), 0).Preview(context.Background(), policy.Query{
		ExecutionMode:   policy.ExecutionModePreview,
		RouteID:         "preview-1",
		TrainingAllowed: false,
		Candidates: []policy.Candidate{
			{RosterID: "arm-a", CatalogID: "model-a", Provider: providers.ProviderAnthropic},
			{RosterID: "arm-b", CatalogID: "model-b", Provider: providers.ProviderOpenAI},
		},
	})

	require.NoError(t, err)
	assert.Equal(t, policy.ExecutionModePreview, got.ExecutionMode)
	assert.False(t, got.TrainingAllowed)
	assert.Equal(t, []float64{0.01, 0.01, 0.05, 0.01, 0.01, 0.01, 0.1, 0.8}, result.HMMStateProbabilities)
	assert.Equal(t, []string{"arm-a", "arm-b"}, result.EligibleRosterIDs)
	assert.Equal(t, "sha256:artifact", result.PolicyArtifactSHA256)
	assert.Equal(t, "sha256:roster", result.RosterSHA256)
}

func TestClientPreviewRequiresPreviewExecutionMode(t *testing.T) {
	_, err := New("http://unused.invalid", nil, 0).Preview(context.Background(), policy.Query{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires preview execution mode")
}

func TestClientOmitsHMMTrainingTranscriptWithoutPermission(t *testing.T) {
	var got routeRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		require.NoError(t, json.NewDecoder(request.Body).Decode(&got))
		_ = json.NewEncoder(w).Encode(routeResponse{SelectedRosterID: "model"})
	}))
	defer server.Close()

	_, err := New(server.URL, server.Client(), 0).Decide(context.Background(), policy.Query{
		Strategy: router.StrategyHMM,
		ConversationMessages: []router.ConversationMessage{
			{Role: "user", Text: "request"},
			{Role: "assistant", Text: "response"},
			{Role: "user", Text: "next request"},
		},
		Candidates: []policy.Candidate{{RosterID: "model"}},
	})

	require.NoError(t, err)
	assert.False(t, got.TrainingAllowed)
	assert.Empty(t, got.TrainingConversationDelta)
}

func TestClientRejectsUnknownRouteSchema(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"schema_version": "policy_router_v99", "selected_roster_id": "model"})
	}))
	defer server.Close()

	_, err := New(server.URL, server.Client(), 0).Decide(context.Background(), policy.Query{
		Candidates: []policy.Candidate{{RosterID: "model"}},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported policy route schema")
}

func TestClientRetriesTransientRouteFailureWithoutFallback(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": "replica unavailable"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"schema_version":     policy.SchemaVersionV1,
			"selected_roster_id": "model-a",
		})
	}))
	defer server.Close()

	result, err := New(server.URL, server.Client(), time.Second).Decide(
		context.Background(),
		policy.Query{Candidates: []policy.Candidate{{RosterID: "model-a"}}},
	)

	require.NoError(t, err)
	assert.Equal(t, "model-a", result.Model)
	assert.Equal(t, 3, attempts)
}

func TestClientReturnsErrorAfterTransientRetriesExhausted(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": "no ready replica"})
	}))
	defer server.Close()

	_, err := New(server.URL, server.Client(), time.Second).Decide(
		context.Background(),
		policy.Query{Candidates: []policy.Candidate{{RosterID: "model-a"}}},
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "retries exhausted")
	assert.Contains(t, err.Error(), "no ready replica")
	assert.Equal(t, 3, attempts)
}

func TestClientRetriesPastStalledSidecarInstanceWithinBudget(t *testing.T) {
	attempts := 0
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts == 1 {
			<-release
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"schema_version":     policy.SchemaVersionV1,
			"selected_roster_id": "model-a",
		})
	}))
	defer server.Close()
	defer close(release)

	result, err := New(server.URL, server.Client(), 5*time.Second, WithAttemptTimeout(150*time.Millisecond)).Decide(
		context.Background(),
		policy.Query{Candidates: []policy.Candidate{{RosterID: "model-a"}}},
	)

	require.NoError(t, err)
	assert.Equal(t, "model-a", result.Model)
	assert.Equal(t, 2, attempts)
}

func TestClientReportsAttemptBudgetWhenEveryAttemptStalls(t *testing.T) {
	attempts := 0
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		attempts++
		<-release
	}))
	defer server.Close()
	defer close(release)

	_, err := New(server.URL, server.Client(), 5*time.Second, WithAttemptTimeout(100*time.Millisecond)).Decide(
		context.Background(),
		policy.Query{Candidates: []policy.Candidate{{RosterID: "model-a"}}},
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "attempt budget")
	// The proxy degrades on deadline errors, so the attempt bound must not
	// disguise itself as an ordinary transport failure.
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, defaultRouteAttempts, attempts)
}

func TestClientAttemptBudgetNeverOutlivesDecisionBudget(t *testing.T) {
	attempts := 0
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		attempts++
		<-release
	}))
	defer server.Close()
	defer close(release)

	started := time.Now()
	_, err := New(server.URL, server.Client(), 200*time.Millisecond, WithAttemptTimeout(time.Minute)).Decide(
		context.Background(),
		policy.Query{Candidates: []policy.Candidate{{RosterID: "model-a"}}},
	)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(started), 3*time.Second)
	assert.Equal(t, 1, attempts)
}

func TestDeriveAttemptTimeoutLeavesRoomForRetries(t *testing.T) {
	assert.Equal(t, 1800*time.Millisecond, DeriveAttemptTimeout(DefaultTimeout))
	assert.Equal(t, 1800*time.Millisecond, DeriveAttemptTimeout(0))
	// Budgets too small to split still get one usable attempt.
	assert.Equal(t, 300*time.Millisecond, DeriveAttemptTimeout(300*time.Millisecond))
	assert.Equal(t, minAttemptTimeout, DeriveAttemptTimeout(700*time.Millisecond))
}

// TestDefaultBudgetFitsASecondFullAttempt guards that two full attempts plus
// backoff fit in DefaultTimeout, and that the bound outlives the sidecar's own deadline.
func TestDefaultBudgetFitsASecondFullAttempt(t *testing.T) {
	attemptTimeout := DeriveAttemptTimeout(DefaultTimeout)
	firstBackoff := time.Duration(1) * routeRetryBackoff

	require.LessOrEqual(t, 2*attemptTimeout+firstBackoff, DefaultTimeout,
		"two full attempts plus the first backoff must fit in the decision budget")
	// An attempt bound under the sidecar's own deadline (HMM: 1.5s) cancels a
	// request it was about to answer, turning a degrade into a router-side timeout.
	const knownSidecarInferenceDeadline = 1500 * time.Millisecond
	assert.Greater(t, attemptTimeout, knownSidecarInferenceDeadline,
		"an attempt must outlive the sidecar's own deadline so its answer is seen")

	// The floor holds this for smaller configured budgets: 0.4 x 3s = 1.2s < 1.5s,
	// so a deployment using ROUTER_*_SIDECAR_TIMEOUT_MS=3000 would cancel degrades.
	assert.Greater(t, DeriveAttemptTimeout(3*time.Second), knownSidecarInferenceDeadline,
		"a 3s configured budget must still outlive the sidecar deadline")
	assert.Equal(t, sidecarInferenceFloor, DeriveAttemptTimeout(3*time.Second))

	// Budgets too small to seat the floor and still leave a retry keep the old
	// behaviour rather than spending everything on attempt 1.
	assert.Equal(t, minAttemptTimeout, DeriveAttemptTimeout(700*time.Millisecond))
	assert.Equal(t, 300*time.Millisecond, DeriveAttemptTimeout(300*time.Millisecond))
}

// TestRetriesExhaustedKeepsBothTheSidecarErrorAndTheDeadline: a sidecar 503 must
// survive alongside the deadline so the policy-deadline fallback still degrades.
func TestRetriesExhaustedKeepsBothTheSidecarErrorAndTheDeadline(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		// Slower than the attempt bound derived from the tiny budget below, so
		// the ladder ends on the parent deadline rather than on a clean status.
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": "hmm inference exceeded its deadline"})
	}))
	defer server.Close()

	_, err := New(server.URL, server.Client(), 250*time.Millisecond).Decide(
		context.Background(),
		policy.Query{Candidates: []policy.Candidate{{RosterID: "model-a"}}},
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "retries exhausted")
	assert.Contains(t, err.Error(), "hmm inference exceeded its deadline",
		"the sidecar's own diagnosis must survive the deadline")
	assert.GreaterOrEqual(t, attempts, 1)
	assert.ErrorIs(t, err, context.DeadlineExceeded,
		"the deadline must stay wrapped so the policy-deadline fallback still degrades")
}

// TestExhaustedPolicyErrShapes pins every way the ladder can end.
func TestExhaustedPolicyErrShapes(t *testing.T) {
	sidecarErr := &PolicyStatusError{Status: http.StatusServiceUnavailable, Message: "fail closed"}

	both := exhaustedPolicyErr(sidecarErr, nil, context.DeadlineExceeded)
	assert.ErrorIs(t, both, context.DeadlineExceeded)
	assert.Contains(t, both.Error(), "fail closed")

	onlySidecar := exhaustedPolicyErr(sidecarErr, nil, nil)
	assert.NotErrorIs(t, onlySidecar, context.DeadlineExceeded,
		"with no deadline anywhere, none should be invented")
	assert.Contains(t, onlySidecar.Error(), "fail closed")

	onlyDeadline := exhaustedPolicyErr(nil, nil, context.DeadlineExceeded)
	assert.ErrorIs(t, onlyDeadline, context.DeadlineExceeded)
	assert.Contains(t, onlyDeadline.Error(), "retries exhausted")

	// A transport deadline is not printed twice: the sentinel is already
	// reachable through the wrapped attempt error.
	transportErr := fmt.Errorf("call policy sidecar: %w", context.DeadlineExceeded)
	deduped := exhaustedPolicyErr(nil, transportErr, context.DeadlineExceeded)
	assert.ErrorIs(t, deduped, context.DeadlineExceeded)
	assert.Equal(t, 1, strings.Count(deduped.Error(), context.DeadlineExceeded.Error()))

	cancelled := exhaustedPolicyErr(nil, fmt.Errorf("call policy sidecar: %w", context.Canceled), nil)
	assert.ErrorIs(t, cancelled, context.Canceled)
}

// TestExhaustedLadderKeepsTheDeadlineWhenPreferringAStatus is the regression
// guard: preferring the status over a truncated final attempt must not drop the
// deadline, or isPolicyDeadlineErr misses and the fallback fails to degrade.
func TestExhaustedLadderKeepsTheDeadlineWhenPreferringAStatus(t *testing.T) {
	statusErr := &PolicyStatusError{Status: http.StatusServiceUnavailable, Message: "hmm inference exceeded its deadline"}
	truncated := fmt.Errorf("call policy sidecar: %w", context.DeadlineExceeded)

	// ctxErr is nil: the loop exhausted its attempts rather than tripping
	// ctx.Done(), which is exactly where the deadline used to be dropped.
	err := exhaustedPolicyErr(statusErr, truncated, nil)

	assert.Contains(t, err.Error(), "hmm inference exceeded its deadline",
		"the sidecar's diagnosis must still surface")
	assert.ErrorIs(t, err, context.DeadlineExceeded,
		"the deadline must survive so the policy-deadline fallback degrades")
}

// TestExhaustedLadderPrefersTheSidecarStatusOverATruncatedAttempt: the earlier
// sidecar status must surface over a budget-truncated final attempt.
func TestExhaustedLadderPrefersTheSidecarStatusOverATruncatedAttempt(t *testing.T) {
	statusErr := &PolicyStatusError{Status: http.StatusServiceUnavailable, Message: "hmm inference exceeded its deadline"}
	truncated := fmt.Errorf("call policy sidecar: %w", context.DeadlineExceeded)

	assert.Same(t, statusErr, preferStatusErr(statusErr, truncated),
		"a sidecar status outranks a later truncated attempt")
	assert.Same(t, truncated, preferStatusErr(nil, truncated),
		"with no status seen, the transport error is all there is")

	surfaced := exhaustedPolicyErr(statusErr, truncated, context.DeadlineExceeded)
	assert.Contains(t, surfaced.Error(), "hmm inference exceeded its deadline")
	assert.ErrorIs(t, surfaced, context.DeadlineExceeded,
		"the policy-deadline fallback must still recognise this as degradable")
}

func TestClientCapabilities(t *testing.T) {
	for _, schemaVersion := range []string{policy.SchemaVersionV1, policy.SchemaVersionV2} {
		t.Run(schemaVersion, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				require.Equal(t, "/capabilities", request.URL.Path)
				_ = json.NewEncoder(w).Encode(policy.Capabilities{
					SchemaVersion:   schemaVersion,
					ReportsFeedback: true,
				})
			}))
			defer server.Close()

			capabilities, err := New(server.URL, server.Client(), 0).Capabilities(context.Background())

			require.NoError(t, err)
			assert.Equal(t, schemaVersion, capabilities.SchemaVersion)
			assert.True(t, capabilities.ReportsFeedback)
		})
	}
}

func TestClientRoster(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/roster", request.URL.Path)
		_ = json.NewEncoder(w).Encode(rosterResponse{
			SchemaVersion: policy.SchemaVersionV2,
			RosterVersion: "abc123",
			RosterIDs:     []string{"openai/gpt-5.6-sol", "anthropic/claude-opus-4.8"},
		})
	}))
	defer server.Close()

	rosterIDs, err := New(server.URL, server.Client(), 0).Roster(context.Background())

	require.NoError(t, err)
	assert.Equal(t, []string{"openai/gpt-5.6-sol", "anthropic/claude-opus-4.8"}, rosterIDs)
}

func TestClientClusterRoster(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/roster", request.URL.Path)
		_ = json.NewEncoder(w).Encode(rosterResponse{
			SchemaVersion: policy.SchemaVersionV2,
			RosterVersion: "abc123",
			RosterIDs:     []string{"openai/gpt-5.6-sol", "anthropic/claude-opus-4.8"},
			Clusters: map[string][]string{
				"maximum": {"anthropic/claude-opus-4.8", "openai/gpt-5.6-sol"},
			},
		})
	}))
	defer server.Close()

	snapshot, err := New(server.URL, server.Client(), 0).ClusterRoster(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "abc123", snapshot.RosterSHA256)
	assert.Equal(t, []string{"anthropic/claude-opus-4.8", "openai/gpt-5.6-sol"}, snapshot.Clusters["maximum"])
}

func TestClientRosterRejectsUnknownSchema(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(rosterResponse{SchemaVersion: "v99", RosterIDs: []string{"x"}})
	}))
	defer server.Close()

	_, err := New(server.URL, server.Client(), 0).Roster(context.Background())

	require.Error(t, err)
}

func TestClientRosterPropagatesStatusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	_, err := New(server.URL, server.Client(), 0).Roster(context.Background())

	require.Error(t, err)
}

func TestClientCheckHealth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		assert.Equal(t, http.MethodGet, request.Method)
		assert.Equal(t, "/readyz", request.URL.Path)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	err := New(server.URL, server.Client(), 0).CheckHealth(context.Background())

	require.NoError(t, err)
}

func TestClientCheckHealthRejectsUnreadySidecar(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"unhealthy"}`))
	}))
	defer server.Close()

	err := New(server.URL, server.Client(), 0).CheckHealth(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "policy readiness status 503")
}

func TestClientReportsOutcomeAndFeedback(t *testing.T) {
	paths := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
	}))
	defer server.Close()
	client := New(server.URL, server.Client(), 0)

	require.NoError(t, client.ReportOutcome(context.Background(), map[string]interface{}{"route_id": "route-1"}))
	require.NoError(t, client.ReportFeedback(context.Background(), map[string]interface{}{"route_id": "route-1"}))
	assert.Equal(t, []string{"/outcome", "/feedback"}, paths)
}

func TestClientUsesBoundedDefaultHTTPClient(t *testing.T) {
	configuredTimeout := 250 * time.Millisecond

	assert.Equal(t, configuredTimeout, New("http://policy", nil, configuredTimeout).client.Timeout)
	assert.Equal(t, DefaultTimeout, New("http://policy", nil, 0).client.Timeout)
}

func TestRouteMessagesPreservesLatestUserWhenPayloadIsCapped(t *testing.T) {
	source := []router.ConversationMessage{
		{Role: "user", Text: strings.Repeat("a", maxRouteMessageTotalChars+100)},
		{Role: "assistant", Text: "older response"},
		{Role: "tool", Text: "raw tool output should be skipped"},
		{Role: "user", Text: "latest request"},
	}
	messages := routeMessages(source)

	assert.Equal(t, "latest request", latestUserText(messages))
	assert.Equal(t, 1, turnIndex(messages))
	assert.True(t, routeMessagesTruncated(source))
	for _, message := range messages {
		assert.NotEqual(t, "tool", message.Role)
	}
}

func floatPtr(value float64) *float64 { return &value }

func intPointer(value int) *int { return &value }

func int64Ptr(value int64) *int64 { return &value }
