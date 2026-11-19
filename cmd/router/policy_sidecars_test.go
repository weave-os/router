package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildConfiguredPolicySidecarsOnboardsFutureStrategy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/capabilities":
			_ = json.NewEncoder(w).Encode(policy.Capabilities{
				SchemaVersion:  policy.SchemaVersionV1,
				SupportsShadow: true,
			})
		case "/route":
			var payload struct {
				ExecutionMode string `json:"execution_mode"`
				Candidates    []struct {
					RosterID string `json:"roster_id"`
					Provider string `json:"provider"`
				} `json:"candidates"`
			}
			if !assert.NoError(t, json.NewDecoder(request.Body).Decode(&payload)) {
				return
			}
			if !assert.NotEmpty(t, payload.Candidates) {
				return
			}
			assert.Equal(t, policy.ExecutionModeServing, payload.ExecutionMode)
			assert.NotEqual(t, providers.ProviderOpenRouter, payload.Candidates[0].Provider)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"schema_version":     policy.SchemaVersionV1,
				"selected_roster_id": payload.Candidates[0].RosterID,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	registrations, err := buildConfiguredPolicySidecars(
		context.Background(),
		policySidecarSettings{endpoints: `{"future-policy":"` + server.URL + `"}`, timeout: time.Second},
		map[string]struct{}{"gpt-5.5": {}},
		map[string]struct{}{providers.ProviderOpenAI: {}},
		server.Client(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	require.NoError(t, err)
	require.Len(t, registrations, 1)
	assert.Equal(t, router.Strategy("future-policy"), registrations[0].Strategy)
	assert.True(t, registrations[0].Capabilities.SupportsShadow)
	decision, err := registrations[0].Router.Route(context.Background(), router.Request{})
	require.NoError(t, err)
	assert.Equal(t, "gpt-5.5", decision.Model)
	assert.Equal(t, providers.ProviderOpenAI, decision.Provider)
}

func TestBuildConfiguredPolicySidecarsRejectsReservedAndInvalidConfiguration(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, raw := range []string{
		`{"hmm":"https://sidecar.internal"}`,
		`{"hmm_embedding":"https://sidecar.internal"}`,
		`{"hmm_beta":"https://sidecar.internal"}`,
		`{"future":"not-a-url"}`,
		`{"future":{"origin":"https://sidecar.internal"}}`,
		`{"future":{"origin":"https://sidecar.internal","policy_sha256":"bad-digest"}}`,
		`{"future policy":"https://sidecar.internal"}`,
		`{"Future":"https://one.internal","future":"https://two.internal"}`,
	} {
		_, err := buildConfiguredPolicySidecars(
			context.Background(), policySidecarSettings{endpoints: raw, timeout: time.Second}, nil, nil, nil, logger,
		)
		require.Error(t, err, raw)
	}
}

func TestParseConfiguredPolicySidecarAuthNormalizesStrategies(t *testing.T) {
	authModes, err := parseConfiguredPolicySidecarSettings(
		`{" Future-Policy ":"google-id-token"}`,
		"ROUTER_POLICY_SIDECAR_AUTH",
		map[router.Strategy]struct{}{"future-policy": {}},
	)

	require.NoError(t, err)
	assert.Equal(t, policySidecarAuthGoogleIDToken, authModes["future-policy"])
}

func TestParseConfiguredPolicySidecarAuthRejectsUnknownStrategy(t *testing.T) {
	_, err := parseConfiguredPolicySidecarSettings(
		`{"unknown":"google-id-token"}`,
		"ROUTER_POLICY_SIDECAR_AUTH",
		map[router.Strategy]struct{}{"future-policy": {}},
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not configured")
}

func TestBuildConfiguredPolicySidecarsRejectsAuthWithoutSidecars(t *testing.T) {
	_, err := buildConfiguredPolicySidecars(
		context.Background(),
		policySidecarSettings{authModes: `{"future-policy":"google-id-token"}`, timeout: time.Second},
		nil,
		nil,
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires ROUTER_POLICY_SIDECARS")
}

func TestConfiguredPolicyRejectsHTTPBearerWithoutOptIn(t *testing.T) {
	_, err := buildConfiguredPolicySidecars(
		context.Background(), policySidecarSettings{
			endpoints:    `{"future-policy":"http://sidecar.internal"}`,
			authModes:    `{"future-policy":"bearer"}`,
			bearerTokens: `{"future-policy":"fixture-sidecar-token"}`,
			timeout:      time.Second,
		}, nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	require.ErrorContains(t, err, "requires HTTPS for bearer auth")
}

func TestConfiguredPolicyValidatesBearerTokenBindings(t *testing.T) {
	for _, tc := range []struct {
		name, mode, tokens string
	}{
		{"missing", "bearer", ""},
		{"blank", "bearer", `{"future-policy":"  "}`},
		{"unexpected", "none", `{"future-policy":"fixture-sidecar-token"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildConfiguredPolicySidecars(
				context.Background(), policySidecarSettings{
					endpoints:    `{"future-policy":"https://sidecar.internal"}`,
					authModes:    `{"future-policy":"` + tc.mode + `"}`,
					bearerTokens: tc.tokens,
					timeout:      time.Second,
				}, nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)),
			)
			require.ErrorContains(t, err, "requires exactly one nonempty token")
		})
	}
}

func TestConfiguredBenchmarkPolicy(t *testing.T) {
	const token = "fixture-sidecar-token"
	digest := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name          string
		authoritative bool
		responseSHA   string
		changeRouteID bool
	}{
		{name: "selects eligible model", authoritative: true, responseSHA: digest},
		{name: "requires authority at startup"},
		{name: "rejects different digest", authoritative: true, responseSHA: strings.Repeat("b", 64)},
		{name: "rejects different route ID", authoritative: true, responseSHA: digest, changeRouteID: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if !assert.Equal(t, "Bearer "+token, request.Header.Get("Authorization")) {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				switch request.URL.Path {
				case "/capabilities":
					_ = json.NewEncoder(w).Encode(policy.Capabilities{
						SchemaVersion: policy.SchemaVersionV1, AuthoritativePerTurnSelection: tc.authoritative,
					})
				case "/route":
					replyToBenchmarkRoute(t, w, request, tc.responseSHA, tc.changeRouteID)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			registrations, err := buildConfiguredPolicySidecars(context.Background(),
				policySidecarSettings{
					endpoints:    `{"benchmark-step-down":{"origin":"` + server.URL + `","policy_sha256":"` + digest + `","allow_http_bearer":true}}`,
					authModes:    `{"benchmark-step-down":"bearer"}`,
					bearerTokens: `{"benchmark-step-down":"` + token + `"}`,
					timeout:      time.Second,
				},
				map[string]struct{}{"gpt-5.5": {}}, map[string]struct{}{providers.ProviderOpenAI: {}},
				server.Client(), slog.New(slog.NewTextHandler(io.Discard, nil)))
			if !tc.authoritative {
				require.ErrorContains(t, err, "must advertise authoritative policy_router_v1 selection")
				return
			}
			require.NoError(t, err)
			require.Len(t, registrations, 1)
			assert.True(t, registrations[0].Capabilities.AuthoritativePerTurnSelection)
			decision, err := registrations[0].Router.Route(context.Background(), router.Request{
				Session: router.SessionView{PolicyTurnContext: &router.PolicyTurnContext{VisibleTurnIndex: 3}},
			})
			if tc.responseSHA != digest || tc.changeRouteID {
				require.ErrorIs(t, err, router.ErrPolicyPinUnavailable)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "gpt-5.5", decision.Model)
			assert.Equal(t, providers.ProviderOpenAI, decision.Provider)
			assert.Equal(t, digest, decision.Metadata.PolicyArtifactSHA256)
		})
	}
}

func replyToBenchmarkRoute(t *testing.T, w http.ResponseWriter, request *http.Request, digest string, changeRouteID bool) {
	t.Helper()
	var payload struct {
		SchemaVersion string             `json:"schema_version"`
		RouteID       string             `json:"route_id"`
		TurnIndex     int                `json:"turn_index"`
		Candidates    []policy.Candidate `json:"candidates"`
	}
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		t.Errorf("decode benchmark route: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if !assert.Len(t, payload.Candidates, 1) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	assert.Equal(t, policy.SchemaVersionV1, payload.SchemaVersion)
	assert.Equal(t, 3, payload.TurnIndex)
	assert.Equal(t, "gpt-5.5", payload.Candidates[0].CatalogID)
	if changeRouteID {
		payload.RouteID += "-different"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"schema_version": policy.SchemaVersionV1, "route_id": payload.RouteID,
		"selected_roster_id": payload.Candidates[0].RosterID,
		"selected_provider":  payload.Candidates[0].Provider,
		"policy_artifact_id": "step-down", "policy_sha256": digest,
	})
}
