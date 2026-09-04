package providers_test

import (
	"testing"

	"weave-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeSnowflakeCortexOpenAIBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    string
	}{
		{
			name:    "cortex rest root",
			baseURL: "https://ACME-PROD1.snowflakecomputing.com/api/v2/cortex",
			want:    "https://ACME-PROD1.snowflakecomputing.com/api/v2/cortex/v1",
		},
		{
			name:    "trailing slash",
			baseURL: "https://acme.snowflakecomputing.com/api/v2/cortex/",
			want:    "https://acme.snowflakecomputing.com/api/v2/cortex/v1",
		},
		{
			name:    "already versioned",
			baseURL: "https://acme.snowflakecomputing.com/api/v2/cortex/v1",
			want:    "https://acme.snowflakecomputing.com/api/v2/cortex/v1",
		},
		{
			name:    "other snowflake path is left alone",
			baseURL: "https://acme.snowflakecomputing.com/api/v2/databases",
			want:    "https://acme.snowflakecomputing.com/api/v2/databases",
		},
		{
			name:    "non-snowflake gateway",
			baseURL: "https://gw.example.com/api/v2/cortex",
			want:    "https://gw.example.com/api/v2/cortex",
		},
		{
			name:    "lookalike host",
			baseURL: "https://acme.snowflakecomputing.com.evil.example/api/v2/cortex",
			want:    "https://acme.snowflakecomputing.com.evil.example/api/v2/cortex",
		},
		{
			name:    "not a URL",
			baseURL: "acme.snowflakecomputing.com/api/v2/cortex",
			want:    "acme.snowflakecomputing.com/api/v2/cortex",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, providers.NormalizeSnowflakeCortexOpenAIBaseURL(tt.baseURL))
		})
	}
}
