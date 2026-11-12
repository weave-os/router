package policyclient

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error {
	b.closed = true
	return nil
}

// scriptedTransport answers each request with the next scripted status and
// records every body it hands out.
type scriptedTransport struct {
	mu       sync.Mutex
	statuses []int
	bodies   []*trackedBody
}

func (t *scriptedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	status := t.statuses[len(t.bodies)%len(t.statuses)]
	body := &trackedBody{Reader: strings.NewReader(`{"error":"scripted"}`)}
	t.bodies = append(t.bodies, body)
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: body, Request: req}, nil
}

func bodyCloseQuery(mode string) policy.Query {
	return policy.Query{
		SchemaVersion:  policy.SchemaVersionV4,
		Strategy:       router.StrategyHMM,
		ExecutionMode:  mode,
		RouteID:        "route-body-close",
		RequestedModel: "openai/gpt-5.6-sol",
		PromptText:     "close every body",
		Candidates: []policy.Candidate{{
			RosterID:  "openai/gpt-5.6-sol",
			CatalogID: "openai/gpt-5.6-sol",
			Provider:  providers.ProviderOpenAI,
		}},
	}
}

func TestPolicyRequestsCloseEveryResponseBody(t *testing.T) {
	cases := []struct {
		name     string
		statuses []int
		wantN    int
	}{
		{name: "final status", statuses: []int{http.StatusBadRequest}, wantN: 1},
		{name: "too large", statuses: []int{http.StatusRequestEntityTooLarge}, wantN: 1},
		{name: "transient then final", statuses: []int{http.StatusServiceUnavailable, http.StatusServiceUnavailable, http.StatusBadRequest}, wantN: 3},
		{name: "retries exhausted", statuses: []int{http.StatusBadGateway}, wantN: defaultRouteAttempts},
	}
	calls := map[string]func(*Client) error{
		"decide": func(c *Client) error {
			_, err := c.Decide(context.Background(), bodyCloseQuery(policy.ExecutionModeServing))
			return err
		},
		"preview": func(c *Client) error {
			_, err := c.Preview(context.Background(), bodyCloseQuery(policy.ExecutionModePreview))
			return err
		},
	}
	for _, tc := range cases {
		for callName, call := range calls {
			t.Run(tc.name+"/"+callName, func(t *testing.T) {
				transport := &scriptedTransport{statuses: tc.statuses}
				client := New("http://policy.test", &http.Client{Transport: transport}, 0)

				require.Error(t, call(client))

				require.Len(t, transport.bodies, tc.wantN)
				for i, body := range transport.bodies {
					assert.True(t, body.closed, "response body %d left open", i)
				}
			})
		}
	}
}
