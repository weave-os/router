package policyclient_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyclient"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

type preparationTransportFunc func(*http.Request) (*http.Response, error)

func (f preparationTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestClassifierPreparationStartsAndPollsFixedSidecarPaths(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer synthetic-service-token", r.Header.Get("Authorization"))
		calls = append(calls, r.Method+" "+r.URL.Path)
		state := policy.PreparationReady
		if r.Method == http.MethodPost {
			state = policy.PreparationWarming
			w.WriteHeader(http.StatusAccepted)
		}
		_, _ = fmt.Fprintf(w, `{"model_id":"qwen","bundle_sha256":%q,"serving_revision":%q,"state":%q,"operation_id":"fc-synthetic1","warmup_lengths":[128]}`, strings.Repeat("a", 64), strings.Repeat("b", 64), state)
	}))
	defer server.Close()
	authenticatedClient := server.Client()
	transport := authenticatedClient.Transport
	authenticatedClient.Transport = preparationTransportFunc(func(r *http.Request) (*http.Response, error) {
		r.Header.Set("Authorization", "Bearer synthetic-service-token")
		return transport.RoundTrip(r)
	})
	client := policyclient.New(server.URL, authenticatedClient, time.Second)
	admittedRouter := policy.NewSidecarRouter(policy.SidecarRouterConfig{Strategy: router.StrategyHMMQwen}, client, nil)
	warming, err := admittedRouter.PrepareClassifier(context.Background(), "")
	require.NoError(t, err)
	require.Equal(t, policy.PreparationWarming, warming.State)
	ready, err := admittedRouter.PrepareClassifier(context.Background(), warming.OperationID)
	require.NoError(t, err)
	require.Equal(t, policy.PreparationReady, ready.State)
	require.Equal(t, []int{128}, ready.WarmupLengths)
	_, err = admittedRouter.PrepareClassifier(context.Background(), "../readyz")
	require.Error(t, err)
	require.Equal(t, []string{"POST /internal/classifiers/qwen/prepare", "GET /internal/classifiers/qwen/prepare/fc-synthetic1"}, calls)
}

func TestClassifierPreparationRejectsStatusStateAndOperationMismatches(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		state     policy.PreparationState
		operation string
	}{
		{"warming as ready", http.StatusOK, policy.PreparationWarming, "fc-synthetic1"},
		{"ready as warming", http.StatusAccepted, policy.PreparationReady, "fc-synthetic1"},
		{"wrong operation", http.StatusOK, policy.PreparationReady, "fc-another"},
		{"unavailable", http.StatusServiceUnavailable, policy.PreparationReady, "fc-synthetic1"},
		{"redirect", http.StatusTemporaryRedirect, policy.PreparationReady, "fc-synthetic1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprintf(w, `{"state":%q,"operation_id":%q}`, tc.state, tc.operation)
			}))
			defer server.Close()
			client := policyclient.New(server.URL, nil, time.Second)
			_, err := client.PrepareClassifier(context.Background(), "fc-synthetic1")
			require.Error(t, err)
		})
	}
}
