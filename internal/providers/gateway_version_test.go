package providers_test

import (
	"testing"

	"weave-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
)

func TestGatewayVersionMemoURLs(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		suffix  string
		want    []string
	}{
		{
			name:    "versioned base needs no alternative",
			baseURL: "https://openrouter.ai/api/v1",
			suffix:  "/chat/completions",
			want:    []string{"https://openrouter.ai/api/v1/chat/completions"},
		},
		{
			name:    "unversioned base is probed one segment down",
			baseURL: "https://acct.snowflakecomputing.com/api/v2/cortex",
			suffix:  "/chat/completions",
			want: []string{
				"https://acct.snowflakecomputing.com/api/v2/cortex/chat/completions",
				"https://acct.snowflakecomputing.com/api/v2/cortex/v1/chat/completions",
			},
		},
		{
			name:    "versioned suffix needs no alternative on an unversioned base",
			baseURL: "https://acct.snowflakecomputing.com/api/v2/cortex",
			suffix:  "/v1/messages",
			want:    []string{"https://acct.snowflakecomputing.com/api/v2/cortex/v1/messages"},
		},
		{
			name:    "versioned suffix on a versioned base is probed without the duplicate",
			baseURL: "https://acct.snowflakecomputing.com/api/v2/cortex/v1",
			suffix:  "/v1/messages",
			want: []string{
				"https://acct.snowflakecomputing.com/api/v2/cortex/v1/v1/messages",
				"https://acct.snowflakecomputing.com/api/v2/cortex/v1/messages",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var memo providers.GatewayVersionMemo
			assert.Equal(t, tc.want, memo.URLs(tc.baseURL, tc.suffix))
		})
	}
}

// A learned base URL must put the alternate first so later requests stop paying
// for the 404 probe, and must not leak that ordering to other base URLs.
func TestGatewayVersionMemoLearn(t *testing.T) {
	var memo providers.GatewayVersionMemo
	memo.Learn("https://acct.snowflakecomputing.com/api/v2/cortex")

	assert.Equal(t, []string{
		"https://acct.snowflakecomputing.com/api/v2/cortex/v1/chat/completions",
		"https://acct.snowflakecomputing.com/api/v2/cortex/chat/completions",
	}, memo.URLs("https://acct.snowflakecomputing.com/api/v2/cortex", "/chat/completions"))

	assert.Equal(t, []string{
		"https://other.example.com/gateway/chat/completions",
		"https://other.example.com/gateway/v1/chat/completions",
	}, memo.URLs("https://other.example.com/gateway", "/chat/completions"))
}
