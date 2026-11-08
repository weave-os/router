package policyclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

const (
	classifierV5SelectionID = "a111111111111111111111111111111111111111111111111111111111111111"
	classifierV5ReleaseID   = "b222222222222222222222222222222222222222222222222222222222222222"
	classifierV5ArtifactSHA = "c333333333333333333333333333333333333333333333333333333333333333"
)

func TestClassifierV5ClientPinsReleaseAndKeepsCallbacksOnIntegrationClient(t *testing.T) {
	var routeRequests []map[string]any
	classifierServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			require.Equal(t, "/v5/capabilities", request.URL.Path)
			assert.Equal(t, classifierV5SelectionID, request.URL.Query().Get("classifier_selection_id"))
			assert.Equal(t, classifierV5ReleaseID, request.URL.Query().Get("classifier_release_id"))
			_ = json.NewEncoder(w).Encode(ClassifierCapabilitiesV5{
				SchemaVersion: policy.SchemaVersionV5, APIMajor: policy.ClassifierAPIMajorV5,
				ClassifierSelectionID: classifierV5SelectionID, ClassifierReleaseID: classifierV5ReleaseID,
				WireSchema: policy.SchemaVersionV5, ClassOrder: []string{"fast", "maximum"},
				TaxonomySHA256:     taxonomyDigest([]string{"fast", "maximum"}),
				ClassifierVariants: []policy.ClassifierVariantID{policy.ClassifierVariantPrimary, policy.ClassifierVariantQwen},
				SupportsPreview:    true,
			})
			return
		}
		var requestPayload map[string]any
		require.NoError(t, json.NewDecoder(request.Body).Decode(&requestPayload))
		routeRequests = append(routeRequests, requestPayload)
		assert.Contains(t, []string{"/v5/classify", "/v5/preview"}, request.URL.Path)
		_ = json.NewEncoder(w).Encode(classifierResponseV5{
			SchemaVersion: policy.SchemaVersionV5, RequestID: requestPayload["request_id"].(string),
			ClassifierSelectionID: classifierV5SelectionID, ClassifierReleaseID: classifierV5ReleaseID,
			ClassifierVariantID: policy.ClassifierVariantQwen, ClassifierArtifactID: "qwen-v5",
			ClassifierArtifactSHA256: classifierV5ArtifactSHA, ClassifierTaxonomySHA256: taxonomyDigest([]string{"fast", "maximum"}),
			PredictedLabel: "fast", ClassOrder: []string{"fast", "maximum"},
			ClassProbabilities:   map[string]float64{"fast": 0.7, "maximum": 0.3},
			ClassifierConfidence: 0.7, ClassifierMargin: 0.4,
		})
	}))
	defer classifierServer.Close()

	var integrationPaths []string
	integrationServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		integrationPaths = append(integrationPaths, request.URL.Path)
		if request.URL.Path == "/capabilities" {
			_ = json.NewEncoder(w).Encode(policy.Capabilities{
				SchemaVersion: policy.SchemaVersionV4, ReportsOutcomes: true, ReportsFeedback: true,
			})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer integrationServer.Close()

	integrationClient := New(integrationServer.URL, nil, time.Second)
	classifierClient := New(classifierServer.URL, nil, time.Second)
	combined, err := NewWithClassifierService(integrationClient, classifierClient)
	require.NoError(t, err)

	capabilities, err := combined.CapabilitiesForClassifierRelease(context.Background(), classifierV5SelectionID, classifierV5ReleaseID)
	require.NoError(t, err)
	assert.Equal(t, policy.SchemaVersionV5, capabilities.SchemaVersion)
	assert.Equal(t, []string{"fast", "maximum"}, capabilities.ClassOrder)
	assert.True(t, capabilities.ReportsOutcomes)
	assert.True(t, capabilities.ReportsFeedback)

	query := policy.Query{
		SchemaVersion: policy.SchemaVersionV5, ExecutionMode: policy.ExecutionModeServing,
		ClassifierReleaseID: classifierV5ReleaseID, ClassifierSelectionID: classifierV5SelectionID,
		ClassifierVariantID: policy.ClassifierVariantQwen, RouteID: "route-v5",
		ClientSessionID: "opaque-client-session", PromptText: "fix this",
		Candidates:      []policy.Candidate{{RosterID: "openai/gpt-6-sol", Provider: "openai"}},
		PreferredModels: []string{"openai/gpt-6-sol"}, TrainingAllowed: true,
	}
	decision, err := combined.Decide(context.Background(), query)
	require.NoError(t, err)
	assert.Equal(t, "qwen-v5", decision.PolicyArtifactID)
	assert.Equal(t, classifierV5ReleaseID, decision.ClassifierReleaseID)
	assert.Equal(t, classifierV5SelectionID, decision.ClassifierSelectionID)
	assert.Equal(t, policy.ClassifierVariantQwen, decision.ClassifierVariantID)
	assert.Nil(t, decision.HMMStatePath, "non-HMM variants do not receive fabricated state facts")

	query.ExecutionMode = policy.ExecutionModePreview
	preview, err := combined.Preview(context.Background(), query)
	require.NoError(t, err)
	assert.Equal(t, classifierV5ReleaseID, preview.ClassifierReleaseID)
	assert.Equal(t, classifierV5SelectionID, preview.ClassifierSelectionID)

	require.Len(t, routeRequests, 2)
	for _, requestPayload := range routeRequests {
		assert.Equal(t, classifierV5ReleaseID, requestPayload["classifier_release_id"])
		assert.Equal(t, classifierV5SelectionID, requestPayload["classifier_selection_id"])
		assert.Equal(t, "qwen", requestPayload["classifier_variant_id"])
		assert.Equal(t, "opaque-client-session", requestPayload["session_id"])
		assert.NotContains(t, requestPayload, "candidates")
		assert.NotContains(t, requestPayload, "candidate_models")
		assert.NotContains(t, requestPayload, "preferred_models")
		assert.NotContains(t, requestPayload, "training_allowed")
	}

	require.NoError(t, combined.ReportOutcome(context.Background(), map[string]interface{}{"status": "ok"}))
	assert.Equal(t, []string{"/capabilities", "/outcome"}, integrationPaths)
}

func TestClassifierV5WrongReleaseIdentityIsAPinError(t *testing.T) {
	classifierServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(w).Encode(classifierResponseV5{
			SchemaVersion: policy.SchemaVersionV5, RequestID: "route-v5",
			ClassifierSelectionID: classifierV5SelectionID, ClassifierReleaseID: "d" + strings.Repeat("1", 63),
			ClassifierVariantID: policy.ClassifierVariantPrimary, ClassifierArtifactID: "hmm-v5",
			ClassifierArtifactSHA256: classifierV5ArtifactSHA, ClassifierTaxonomySHA256: taxonomyDigest([]string{"fast", "maximum"}),
			PredictedLabel: "fast", ClassOrder: []string{"fast", "maximum"},
			ClassProbabilities:   map[string]float64{"fast": 0.7, "maximum": 0.3},
			ClassifierConfidence: 0.7, ClassifierMargin: 0.4,
		})
	}))
	defer classifierServer.Close()
	client, err := NewWithClassifierService(New(classifierServer.URL, nil, time.Second), New(classifierServer.URL, nil, time.Second))
	require.NoError(t, err)
	_, err = client.Decide(context.Background(), policy.Query{
		SchemaVersion: policy.SchemaVersionV5, ClassifierReleaseID: classifierV5ReleaseID,
		ClassifierSelectionID: classifierV5SelectionID, ClassifierVariantID: policy.ClassifierVariantPrimary,
		RouteID: "route-v5", ExecutionMode: policy.ExecutionModeServing,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, router.ErrPolicyPinUnavailable))
}

func TestClassifierV5UnknownReleaseIsAPinError(t *testing.T) {
	classifierServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"unknown classifier release"}`))
	}))
	defer classifierServer.Close()
	client := New(classifierServer.URL, nil, time.Second)
	_, err := client.Decide(context.Background(), policy.Query{
		SchemaVersion: policy.SchemaVersionV5, ClassifierReleaseID: classifierV5ReleaseID,
		ClassifierSelectionID: classifierV5SelectionID, ClassifierVariantID: policy.ClassifierVariantPrimary,
		RouteID: "route-v5", ExecutionMode: policy.ExecutionModeServing,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, router.ErrPolicyPinUnavailable))
}

func TestClassifierV5WrongArtifactIdentityIsAPinError(t *testing.T) {
	servedArtifactSHA := "e" + strings.Repeat("4", 63)
	classifierServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(w).Encode(classifierResponseV5{
			SchemaVersion: policy.SchemaVersionV5, RequestID: "route-v5",
			ClassifierSelectionID: classifierV5SelectionID, ClassifierReleaseID: classifierV5ReleaseID,
			ClassifierVariantID: policy.ClassifierVariantPrimary, ClassifierArtifactID: "hmm-v5",
			ClassifierArtifactSHA256: servedArtifactSHA, ClassifierTaxonomySHA256: taxonomyDigest([]string{"fast", "maximum"}),
			PredictedLabel: "fast", ClassOrder: []string{"fast", "maximum"},
			ClassProbabilities:   map[string]float64{"fast": 0.7, "maximum": 0.3},
			ClassifierConfidence: 0.7, ClassifierMargin: 0.4,
		})
	}))
	defer classifierServer.Close()
	client := New(classifierServer.URL, nil, time.Second)
	query := policy.Query{
		SchemaVersion: policy.SchemaVersionV5, ClassifierReleaseID: classifierV5ReleaseID,
		ClassifierSelectionID: classifierV5SelectionID, ClassifierVariantID: policy.ClassifierVariantPrimary,
		ClassifierArtifactSHA256: classifierV5ArtifactSHA,
		RouteID:                  "route-v5", ExecutionMode: policy.ExecutionModeServing,
	}
	_, err := client.Decide(context.Background(), query)
	require.Error(t, err)
	assert.ErrorIs(t, err, router.ErrPolicyPinUnavailable)

	query.ExecutionMode = policy.ExecutionModePreview
	_, err = client.Preview(context.Background(), query)
	require.Error(t, err)
	assert.ErrorIs(t, err, router.ErrPolicyPinUnavailable)
}

func TestClassifierV5PropagatesRemainingDeadlineToWorker(t *testing.T) {
	budgets := make(chan string, 2)
	classifierServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		budgets <- request.Header.Get(policy.ClassifierTimeoutHeader)
		_ = json.NewEncoder(w).Encode(classifierResponseV5{
			SchemaVersion: policy.SchemaVersionV5, RequestID: "route-v5",
			ClassifierSelectionID: classifierV5SelectionID, ClassifierReleaseID: classifierV5ReleaseID,
			ClassifierVariantID: policy.ClassifierVariantPrimary, ClassifierArtifactID: "hmm-v5",
			ClassifierArtifactSHA256: classifierV5ArtifactSHA, ClassifierTaxonomySHA256: taxonomyDigest([]string{"fast", "maximum"}),
			PredictedLabel: "fast", ClassOrder: []string{"fast", "maximum"},
			ClassProbabilities:   map[string]float64{"fast": 0.7, "maximum": 0.3},
			ClassifierConfidence: 0.7, ClassifierMargin: 0.4,
		})
	}))
	defer classifierServer.Close()
	client := New(classifierServer.URL, nil, 2*time.Second)
	query := policy.Query{
		SchemaVersion: policy.SchemaVersionV5, ClassifierReleaseID: classifierV5ReleaseID,
		ClassifierSelectionID: classifierV5SelectionID, ClassifierVariantID: policy.ClassifierVariantPrimary,
		ClassifierArtifactSHA256: classifierV5ArtifactSHA,
		RouteID:                  "route-v5", ExecutionMode: policy.ExecutionModeServing,
	}
	callerCtx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := client.Decide(callerCtx, query)
	require.NoError(t, err)
	budget, err := strconv.Atoi(<-budgets)
	require.NoError(t, err, "the worker receives the Router's remaining classify budget")
	assert.Greater(t, budget, 0)
	assert.LessOrEqual(t, budget, 300, "the caller's tighter deadline bounds the worker budget")

	query.ExecutionMode = policy.ExecutionModePreview
	_, err = client.Preview(context.Background(), query)
	require.NoError(t, err)
	budget, err = strconv.Atoi(<-budgets)
	require.NoError(t, err)
	assert.Greater(t, budget, 300)
	assert.LessOrEqual(t, budget, 2000, "the client timeout bounds the worker budget")
}

func TestClassifierV5MalformedResponseIsAProtocolErrorNotAPinError(t *testing.T) {
	for name, mutate := range map[string]func(*classifierResponseV5){
		"other request":        func(r *classifierResponseV5) { r.RequestID = "route-other" },
		"class missing":        func(r *classifierResponseV5) { r.ClassProbabilities = map[string]float64{"fast": 0.7, "slow": 0.3} },
		"taxonomy digest":      func(r *classifierResponseV5) { r.ClassifierTaxonomySHA256 = strings.Repeat("f", 64) },
		"prediction off-class": func(r *classifierResponseV5) { r.PredictedLabel = "slow" },
		"partial HMM state":    func(r *classifierResponseV5) { r.HMMStateID = pointerTo(0) },
	} {
		t.Run(name, func(t *testing.T) {
			classifierServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				response := classifierResponseV5{
					SchemaVersion: policy.SchemaVersionV5, RequestID: "route-v5",
					ClassifierSelectionID: classifierV5SelectionID, ClassifierReleaseID: classifierV5ReleaseID,
					ClassifierVariantID: policy.ClassifierVariantPrimary, ClassifierArtifactID: "hmm-v5",
					ClassifierArtifactSHA256: classifierV5ArtifactSHA, ClassifierTaxonomySHA256: taxonomyDigest([]string{"fast", "maximum"}),
					PredictedLabel: "fast", ClassOrder: []string{"fast", "maximum"},
					ClassProbabilities:   map[string]float64{"fast": 0.7, "maximum": 0.3},
					ClassifierConfidence: 0.7, ClassifierMargin: 0.4,
				}
				mutate(&response)
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer classifierServer.Close()
			query := policy.Query{
				SchemaVersion: policy.SchemaVersionV5, ClassifierReleaseID: classifierV5ReleaseID,
				ClassifierSelectionID: classifierV5SelectionID, ClassifierVariantID: policy.ClassifierVariantPrimary,
				ClassifierArtifactSHA256: classifierV5ArtifactSHA,
				RouteID:                  "route-v5", ExecutionMode: policy.ExecutionModeServing,
			}
			_, err := New(classifierServer.URL, nil, time.Second).Decide(context.Background(), query)
			require.Error(t, err)
			assert.NotErrorIs(t, err, router.ErrPolicyPinUnavailable)
			query.ExecutionMode = policy.ExecutionModePreview
			_, err = New(classifierServer.URL, nil, time.Second).Preview(context.Background(), query)
			require.Error(t, err)
			assert.NotErrorIs(t, err, router.ErrPolicyPinUnavailable)
		})
	}
}
