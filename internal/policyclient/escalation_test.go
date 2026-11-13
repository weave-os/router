package policyclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyclient"
	"weave-os/router/internal/router/escalation"
)

const validEscalationResponse = `{"schema_version":"escalation_runtime_v1","state":{"observed_turns":5},"prediction":{"score":0.75,"threshold":0.5,"escalate":true},"prediction_unavailable":false,"model_id":"classifier-fixture","package_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`

func TestObserveEscalationHTTPContract(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		due         bool
		wantError   string
		unavailable bool
	}{
		{name: "positive checkpoint", body: validEscalationResponse, due: true},
		{name: "threshold equality", body: strings.Replace(validEscalationResponse, `"score":0.75`, `"score":0.5`, 1), due: true},
		{name: "ordinary observation", body: strings.Replace(validEscalationResponse, `{"score":0.75,"threshold":0.5,"escalate":true}`, `null`, 1)},
		{name: "scoring unavailable preserves state", body: strings.Replace(strings.Replace(validEscalationResponse, `{"score":0.75,"threshold":0.5,"escalate":true}`, `null`, 1), `"prediction_unavailable":false`, `"prediction_unavailable":true`, 1), due: true, unavailable: true},
		{name: "negative score", body: strings.Replace(validEscalationResponse, `"score":0.75`, `"score":-0.1`, 1), due: true, wantError: "invalid escalation prediction"},
		{name: "score above one", body: strings.Replace(validEscalationResponse, `"score":0.75`, `"score":1.1`, 1), due: true, wantError: "invalid escalation prediction"},
		{name: "threshold above one", body: strings.Replace(validEscalationResponse, `"threshold":0.5`, `"threshold":1.1`, 1), due: true, wantError: "invalid escalation prediction"},
		{name: "threshold decision disagreement", body: strings.Replace(validEscalationResponse, `"escalate":true`, `"escalate":false`, 1), due: true, wantError: "invalid escalation prediction"},
		{name: "nonfinite JSON", body: strings.Replace(validEscalationResponse, `"score":0.75`, `"score":NaN`, 1), due: true, wantError: "decode escalation response"},
		{name: "overflow score", body: strings.Replace(validEscalationResponse, `"score":0.75`, `"score":1e999`, 1), due: true, wantError: "decode escalation response"},
		{name: "null score", body: strings.Replace(validEscalationResponse, `"score":0.75`, `"score":null`, 1), due: true, wantError: "incomplete escalation prediction"},
		{name: "missing threshold", body: strings.Replace(validEscalationResponse, `"threshold":0.5,`, ``, 1), due: true, wantError: "incomplete escalation prediction"},
		{name: "null decision", body: strings.Replace(validEscalationResponse, `"escalate":true`, `"escalate":null`, 1), due: true, wantError: "incomplete escalation prediction"},
		{name: "short digest", body: strings.Replace(validEscalationResponse, strings.Repeat("a", 64), "abc", 1), due: true, wantError: "invalid escalation response contract"},
		{name: "nonhex digest", body: strings.Replace(validEscalationResponse, strings.Repeat("a", 64), strings.Repeat("g", 64), 1), due: true, wantError: "invalid escalation package digest"},
		{name: "unsupported schema", body: strings.Replace(validEscalationResponse, "escalation_runtime_v1", "unsupported", 1), due: true, wantError: "invalid escalation response contract"},
		{name: "missing state", body: strings.Replace(validEscalationResponse, `{"observed_turns":5}`, `null`, 1), due: true, wantError: "invalid escalation response contract"},
		{name: "missing model identity", body: strings.Replace(validEscalationResponse, `"classifier-fixture"`, `""`, 1), due: true, wantError: "invalid escalation response contract"},
		{name: "prediction off cadence", body: validEscalationResponse, wantError: "cadence mismatch"},
		{name: "due without prediction", body: strings.Replace(validEscalationResponse, `{"score":0.75,"threshold":0.5,"escalate":true}`, `null`, 1), due: true, wantError: "cadence mismatch"},
		{name: "unavailable with prediction", body: strings.Replace(validEscalationResponse, `"prediction_unavailable":false`, `"prediction_unavailable":true`, 1), due: true, wantError: "invalid unavailable prediction"},
		{name: "unavailable off cadence", body: strings.Replace(strings.Replace(validEscalationResponse, `{"score":0.75,"threshold":0.5,"escalate":true}`, `null`, 1), `"prediction_unavailable":false`, `"prediction_unavailable":true`, 1), wantError: "invalid unavailable prediction"},
		{name: "trailing JSON", body: validEscalationResponse + ` {"prediction":null}`, due: true, wantError: "decode escalation response"},
		{name: "oversized body", body: validEscalationResponse + strings.Repeat(" ", 2<<20), due: true, wantError: "exceeds"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var attempts atomic.Int32
			var received escalation.ObserveRequest
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts.Add(1)
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "/escalation/observe", r.URL.Path)
				assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
				if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
					t.Error(err)
				}
				_, _ = fmt.Fprint(w, test.body)
			}))
			defer server.Close()
			request := escalation.ObserveRequest{Observation: json.RawMessage(`{"messages":[]}`), State: json.RawMessage(`{"observed_turns":4}`), PreviousOutcome: &escalation.PreviousOutcome{StatusCode: 503, IsError: true}, PredictDue: test.due}
			observed, err := policyclient.New(server.URL, server.Client(), time.Second).ObserveEscalation(context.Background(), request)
			assert.EqualValues(t, 1, attempts.Load())
			if test.wantError != "" {
				require.ErrorContains(t, err, test.wantError)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.due, received.PredictDue)
			require.NotNil(t, received.PreviousOutcome)
			assert.Equal(t, 503, received.PreviousOutcome.StatusCode)
			assert.JSONEq(t, `{"observed_turns":5}`, string(observed.State))
			assert.Equal(t, test.unavailable, observed.PredictionUnavailable)
			if test.due && !test.unavailable {
				require.NotNil(t, observed.Prediction)
				assert.True(t, observed.Prediction.Escalate)
			}
		})
	}
}

func TestObserveEscalationFailureHasOneAttempt(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts.Add(1)
				w.WriteHeader(status)
				_, _ = fmt.Fprint(w, "private service diagnostic")
			}))
			defer server.Close()
			_, err := policyclient.New(server.URL, server.Client(), time.Second).ObserveEscalation(context.Background(), escalation.ObserveRequest{})
			require.ErrorContains(t, err, fmt.Sprintf("service status %d", status))
			assert.NotContains(t, err.Error(), "private service diagnostic")
			assert.EqualValues(t, 1, attempts.Load())
		})
	}
}

func TestObserveEscalationHonorsConfiguredTimeoutWithoutRetry(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer server.Close()
	_, err := policyclient.New(server.URL, server.Client(), 50*time.Millisecond).ObserveEscalation(context.Background(), escalation.ObserveRequest{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded))
	assert.EqualValues(t, 1, attempts.Load())
}

func TestObserveEscalationHonorsEarlierCallerDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := policyclient.New(server.URL, server.Client(), time.Second).ObserveEscalation(ctx, escalation.ObserveRequest{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
}
