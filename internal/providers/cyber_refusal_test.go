package providers_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
)

func TestContainsCyberPolicyRefusal(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want bool
	}{
		{
			name: "streamed error event",
			in:   `data: {"type":"error","message":"This content was flagged for possible cybersecurity risk. If this seems wrong, try rephrasing your request."}`,
			want: true,
		},
		{
			name: "streamed turn.failed event",
			in:   `data: {"type":"turn.failed","error":{"message":"This content was flagged for possible cybersecurity risk."}}`,
			want: true,
		},
		{
			name: "streamed response.failed event carrying the code only",
			in:   `data: {"type":"response.failed","response":{"status":"failed","error":{"code":"cyber_policy","message":"declined"}}}`,
			want: true,
		},
		{
			name: "non-streaming error body",
			in:   `{"error":{"message":"This content was flagged for possible cybersecurity risk.","type":"invalid_request_error","code":"cyber_policy"}}`,
			want: true,
		},
		{
			name: "server error",
			in:   `{"error":{"message":"The server had an error processing your request","code":"server_error"}}`,
			want: false,
		},
		{
			name: "unrelated content filter",
			in:   `{"error":{"message":"Your prompt was flagged by our safety system","code":"invalid_prompt"}}`,
			want: false,
		},
		{
			name: "model output discussing cybersecurity",
			in:   `data: {"type":"response.output_text.delta","delta":"cybersecurity risk assessments are"}`,
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, providers.ContainsCyberPolicyRefusal([]byte(tc.in)))
		})
	}
}

func TestIsUpstreamCyberPolicyRefusal(t *testing.T) {
	refusal := &providers.UpstreamErrorResponse{
		Status: http.StatusBadRequest,
		Body:   []byte(`{"error":{"code":"cyber_policy","message":"This content was flagged for possible cybersecurity risk."}}`),
	}
	assert.True(t, providers.IsUpstreamCyberPolicyRefusal(refusal))

	assert.False(t, providers.IsUpstreamCyberPolicyRefusal(&providers.UpstreamErrorResponse{
		Status: http.StatusTooManyRequests,
		Body:   []byte(`{"error":{"code":"rate_limit_exceeded"}}`),
	}))
	assert.False(t, providers.IsUpstreamCyberPolicyRefusal(errors.New("dial tcp: connection refused")))
	assert.False(t, providers.IsUpstreamCyberPolicyRefusal(nil))
}

// The synthesized error stands in for a refusal seen on a 200 stream, so it has
// to classify as the refusal and stay out of the transport-retry path.
func TestCyberPolicyRefusalErrorRoundTrips(t *testing.T) {
	err := providers.CyberPolicyRefusalError()

	require.True(t, providers.IsUpstreamCyberPolicyRefusal(err))
	assert.Equal(t, http.StatusBadRequest, err.Status)
	assert.False(t, providers.IsRetryable(err))
	assert.Equal(t, providers.CyberPolicyRefusalMessage, providers.UpstreamErrorBodyMessage(err))
}
