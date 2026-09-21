package policyclient_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyclient"
	"weave-os/router/internal/router"
)

func TestAtomicClassifierAuthenticatedFactsAndRelease(t *testing.T) {
	const release = "llm-classifier-v1.0.0"
	digest := strings.Repeat("a", 64)
	bearer := strings.Repeat("s", 32)
	var received router.AtomicClassificationRequest
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/classify", r.URL.Path)
		require.Equal(t, "Bearer "+bearer, r.Header.Get("Authorization"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&received))
		fmt.Fprintf(w, `{"release":%q,"release_sha256":%q,"prediction":2,"probabilities":[0.1,0.2,0.6,0.1],"input_tokens":8192,"history_source":%q}`, release, digest, router.ClassifierHistoricalPrediction)
	}))
	defer server.Close()
	client, err := policyclient.NewAtomicClassifier(server.URL, bearer, release, digest, server.Client())
	require.NoError(t, err)
	input := router.AtomicClassificationRequest{HistorySource: router.ClassifierHistoricalPrediction, User: router.AtomicClassifierUser{CurrentUserMessage: strings.Repeat("private prompt ", 1000), Features: router.ClassifierFeatures{UserMessageCount: 1}, PrecedingResponses: []router.PredictedClassifierResponse{}}}
	prediction, err := client.Classify(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, router.ClassifierHigh, prediction.Complexity)
	require.Equal(t, input, received, "full text must not be clipped or augmented with candidate models")
}

func TestAtomicClassifierFailsClosed(t *testing.T) {
	digest := strings.Repeat("a", 64)
	valid := fmt.Sprintf(`{"release":"llm-classifier-v1.0.0","release_sha256":%q,"prediction":2,"probabilities":[0.1,0.2,0.6,0.1],"input_tokens":42,"history_source":%q}`, digest, router.ClassifierHistoricalPrediction)
	for _, test := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"unauthorized", 401, "private input echoed", router.ErrClassifierUnavailable},
		{"overflow", 413, "private input echoed", router.ErrClassifierInputTooLong},
		{"service", 503, "private input echoed", router.ErrClassifierUnavailable},
		{"wrong_release", 200, strings.Replace(valid, "v1.0.0", "v2.0.0", 1), router.ErrClassifierUnavailable},
		{"wrong_hash", 200, strings.Replace(valid, digest, strings.Repeat("b", 64), 1), router.ErrClassifierUnavailable},
		{"missing_digit", 200, strings.Replace(valid, `"prediction":2,`, "", 1), router.ErrClassifierUnavailable},
		{"out_of_range", 200, strings.Replace(valid, `"prediction":2`, `"prediction":4`, 1), router.ErrClassifierUnavailable},
		{"inconsistent_argmax", 200, strings.Replace(valid, `"prediction":2`, `"prediction":1`, 1), router.ErrClassifierUnavailable},
		{"bad_probabilities", 200, strings.Replace(valid, "0.6", "0.9", 1), router.ErrClassifierUnavailable},
		{"wrong_provenance", 200, strings.Replace(valid, string(router.ClassifierHistoricalPrediction), "dataset_label", 1), router.ErrClassifierUnavailable},
		{"overlength_success", 200, strings.Replace(valid, "42", "8193", 1), router.ErrClassifierUnavailable},
		{"old_32k_success", 200, strings.Replace(valid, "42", "32768", 1), router.ErrClassifierUnavailable},
		{"oversized_response", 200, strings.Repeat(" ", 17000) + valid, router.ErrClassifierUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(test.status); fmt.Fprint(w, test.body) }))
			defer server.Close()
			client, err := policyclient.NewAtomicClassifier(server.URL, strings.Repeat("k", 32), "llm-classifier-v1.0.0", digest, server.Client())
			require.NoError(t, err)
			_, err = client.Classify(context.Background(), router.AtomicClassificationRequest{})
			require.ErrorIs(t, err, test.want)
			require.NotContains(t, err.Error(), "private input")
		})
	}
}

func TestAtomicClassifierRejectsRedirectAndCancellation(t *testing.T) {
	var calls int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Location", "https://example.invalid/steal")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client, err := policyclient.NewAtomicClassifier(server.URL, strings.Repeat("k", 32), "llm-classifier-v1.0.0", strings.Repeat("a", 64), server.Client())
	require.NoError(t, err)
	_, err = client.Classify(context.Background(), router.AtomicClassificationRequest{})
	require.ErrorIs(t, err, router.ErrClassifierUnavailable)
	require.Equal(t, 1, calls)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.Classify(ctx, router.AtomicClassificationRequest{})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, calls)
}
