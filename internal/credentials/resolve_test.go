package credentials_test

import (
	"net/http"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

const (
	claudeToken    = "sk-ant-oat01-caller"
	codexToken     = "codex-jwt"
	codexAccountID = "acct-1"
)

func bearer(token string) http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	return h
}

func byokKey(provider string, priority auth.FundingPriority) *auth.ExternalAPIKey {
	return &auth.ExternalAPIKey{ID: "key-" + provider, Provider: provider, Plaintext: []byte("byok-" + provider), FundingPriority: priority}
}

func routerKeyed(f requestcontext.RequestFacts) requestcontext.RequestFacts {
	f.InstallationID = uuid.New()
	return f
}

func spent(t *testing.T, res credentials.Resolution) string {
	t.Helper()
	if res.Credentials == nil {
		return ""
	}
	return string(res.Credentials.APIKey)
}

func TestResolve_KeyFirstBYOKBeatsStoredSubscription(t *testing.T) {
	facts := requestcontext.RequestFacts{
		ExternalKeys:          []*auth.ExternalAPIKey{byokKey(providers.ProviderAnthropic, auth.FundingPriorityKeyFirst)},
		AnthropicSubscription: claudeToken,
	}
	res := credentials.Resolve(facts, providers.ProviderAnthropic, credentials.Suppression{})
	assert.Equal(t, "byok-anthropic", spent(t, res))
	assert.Equal(t, requestcontext.SourceBYOK, res.Credentials.Source)
}

func TestResolve_PlanFirstBYOKLeavesSubscriptionFirst(t *testing.T) {
	facts := requestcontext.RequestFacts{
		ExternalKeys:          []*auth.ExternalAPIKey{byokKey(providers.ProviderAnthropic, auth.FundingPriorityPlanFirst)},
		AnthropicSubscription: " " + claudeToken + " ",
	}
	res := credentials.Resolve(facts, providers.ProviderAnthropic, credentials.Suppression{})
	assert.Equal(t, claudeToken, spent(t, res))
	assert.True(t, res.Credentials.OAuth)
	assert.False(t, res.InboundSubscription)
}

func TestResolve_SuppressedClaudeFallsToBYOK(t *testing.T) {
	facts := requestcontext.RequestFacts{
		ExternalKeys:          []*auth.ExternalAPIKey{byokKey(providers.ProviderAnthropic, auth.FundingPriorityPlanFirst)},
		AnthropicSubscription: claudeToken,
	}
	res := credentials.Resolve(facts, providers.ProviderAnthropic, credentials.Suppression{Claude: true})
	assert.Equal(t, "byok-anthropic", spent(t, res))
}

func TestResolve_InboundSubscriptionBearerHonoredWhenRouterKeyed(t *testing.T) {
	facts := routerKeyed(requestcontext.RequestFacts{Headers: bearer(claudeToken)})
	res := credentials.Resolve(facts, providers.ProviderAnthropic, credentials.Suppression{})
	assert.Equal(t, claudeToken, spent(t, res))
	assert.True(t, res.InboundSubscription)
}

func TestResolve_ClientAPIKeyOnlyOffRouterKeyPath(t *testing.T) {
	headers := apiKeyHeaders("sk-ant-api03-client")

	unkeyed := credentials.Resolve(requestcontext.RequestFacts{Headers: headers}, providers.ProviderAnthropic, credentials.Suppression{})
	assert.Equal(t, "sk-ant-api03-client", spent(t, unkeyed))
	assert.Equal(t, requestcontext.SourceClient, unkeyed.Credentials.Source)

	keyed := credentials.Resolve(routerKeyed(requestcontext.RequestFacts{Headers: headers}), providers.ProviderAnthropic, credentials.Suppression{})
	assert.Equal(t, credentials.Resolution{}, keyed)
}

func TestResolve_SuppressedSubscriptionIsClearedNotForwarded(t *testing.T) {
	facts := requestcontext.RequestFacts{Headers: bearer(claudeToken), AnthropicSubscription: claudeToken}
	res := credentials.Resolve(facts, providers.ProviderAnthropic, credentials.Suppression{Claude: true})
	assert.Equal(t, credentials.Resolution{Clear: true}, res)
}

func TestResolve_SuppressionIsPerProvider(t *testing.T) {
	facts := requestcontext.RequestFacts{AnthropicSubscription: claudeToken, OpenAISubscription: codexToken, OpenAIAccountID: codexAccountID}
	res := credentials.Resolve(facts, providers.ProviderOpenAI, credentials.Suppression{Claude: true})
	assert.Equal(t, codexToken, spent(t, res))
	assert.Equal(t, requestcontext.SourceCodexSubscription, res.Credentials.Source)
}

func TestResolve_CodexSubscriptionNeedsAccountID(t *testing.T) {
	facts := routerKeyed(requestcontext.RequestFacts{OpenAISubscription: codexToken})
	res := credentials.Resolve(facts, providers.ProviderOpenAI, credentials.Suppression{Codex: true})
	assert.Equal(t, credentials.Resolution{Clear: true}, res)

	res = credentials.Resolve(facts, providers.ProviderOpenAI, credentials.Suppression{})
	assert.Equal(t, credentials.Resolution{}, res)
}

func TestResolve_SubscriptionNeverCrossesProviders(t *testing.T) {
	facts := requestcontext.RequestFacts{Headers: bearer(claudeToken), AnthropicSubscription: claudeToken}
	res := credentials.Resolve(facts, providers.ProviderOpenAI, credentials.Suppression{})
	assert.NotEqual(t, claudeToken, spent(t, res))
}

func TestKeyFirst_LastUsableKeyWins(t *testing.T) {
	keys := []*auth.ExternalAPIKey{
		byokKey(providers.ProviderAnthropic, auth.FundingPriorityKeyFirst),
		byokKey(providers.ProviderAnthropic, auth.FundingPriorityPlanFirst),
		{Provider: providers.ProviderAnthropic, FundingPriority: auth.FundingPriorityKeyFirst},
	}
	assert.False(t, credentials.KeyFirst(keys, providers.ProviderAnthropic))
	assert.True(t, credentials.KeyFirst(keys[:1], providers.ProviderAnthropic))
	assert.False(t, credentials.KeyFirst(keys[:1], providers.ProviderOpenAI))
}
