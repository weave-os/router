package providers_test

import (
	"testing"

	"weave-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeAzureOpenAIBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    string
	}{
		{
			name:    "openai-style /v1 on an azure resource",
			baseURL: "https://zllama-dev.openai.azure.com/v1",
			want:    "https://zllama-dev.openai.azure.com/openai/v1",
		},
		{
			name:    "bare azure resource",
			baseURL: "https://zllama-dev.openai.azure.com",
			want:    "https://zllama-dev.openai.azure.com/openai/v1",
		},
		{
			name:    "foundry resource",
			baseURL: "https://contoso.services.ai.azure.com/",
			want:    "https://contoso.services.ai.azure.com/openai/v1",
		},
		{
			name:    "already correct",
			baseURL: "https://zllama-dev.openai.azure.com/openai/v1",
			want:    "https://zllama-dev.openai.azure.com/openai/v1",
		},
		{
			name:    "deployment-scoped path is left alone",
			baseURL: "https://zllama-dev.openai.azure.com/openai/deployments/gpt-5",
			want:    "https://zllama-dev.openai.azure.com/openai/deployments/gpt-5",
		},
		{
			name:    "non-azure gateway",
			baseURL: "https://gw.example.com/v1",
			want:    "https://gw.example.com/v1",
		},
		{
			name:    "lookalike host",
			baseURL: "https://notopenai.azure.com.evil.example/v1",
			want:    "https://notopenai.azure.com.evil.example/v1",
		},
		{
			name:    "not a URL",
			baseURL: "zllama-dev.openai.azure.com",
			want:    "zllama-dev.openai.azure.com",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, providers.NormalizeAzureOpenAIBaseURL(tt.baseURL))
		})
	}
}
