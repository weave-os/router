package proxy

import (
	"context"
	"testing"

	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Encrypted reasoning only decrypts under the account and model that minted
// it, so every dimension that can change the upstream identity mid-session
// must change the scope — otherwise the replay 400s and ends the session.
func TestReasoningReplayScope_DistinguishesUpstreams(t *testing.T) {
	base := &requestcontext.Credentials{
		APIKey:  []byte("key-a"),
		Source:  requestcontext.SourceBYOK,
		BaseURL: "https://acct-a.snowflakecomputing.com/api/v2/cortex/v1",
	}
	ctx := requestcontext.WithCredentials(context.Background(), base)
	decision := router.Decision{Provider: "openai_compat", Model: "grok-4-fast"}
	want := reasoningReplayScope(ctx, decision)
	require.NotEmpty(t, want)
	assert.Equal(t, want, reasoningReplayScope(ctx, decision), "same upstream keeps the scope stable across turns")

	for name, tc := range map[string]struct {
		creds    requestcontext.Credentials
		decision router.Decision
	}{
		"another model":        {*base, router.Decision{Provider: decision.Provider, Model: "glm-5.1"}},
		"another provider":     {*base, router.Decision{Provider: "openai", Model: decision.Model}},
		"another account key":  {requestcontext.Credentials{APIKey: []byte("key-b"), Source: base.Source, BaseURL: base.BaseURL}, decision},
		"another account host": {requestcontext.Credentials{APIKey: base.APIKey, Source: base.Source, BaseURL: "https://acct-b.snowflakecomputing.com/api/v2/cortex/v1"}, decision},
		"another credential source": {
			requestcontext.Credentials{APIKey: base.APIKey, Source: requestcontext.SourceSubscription, BaseURL: base.BaseURL},
			decision,
		},
		"aliased to another wire model": {
			requestcontext.Credentials{
				APIKey:       base.APIKey,
				Source:       base.Source,
				BaseURL:      base.BaseURL,
				ModelAliases: map[string]string{decision.Model: "grok-4-fast-eu"},
			},
			decision,
		},
	} {
		t.Run(name, func(t *testing.T) {
			creds := tc.creds
			got := reasoningReplayScope(requestcontext.WithCredentials(context.Background(), &creds), tc.decision)
			assert.NotEqual(t, want, got)
		})
	}
}

// The scope travels to the client inside the minted signature, so it must not
// leak the key it is derived from.
func TestReasoningReplayScope_DoesNotLeakCredential(t *testing.T) {
	creds := &requestcontext.Credentials{APIKey: []byte("sk-secret-value"), AccountID: []byte("acct-secret")}
	scope := reasoningReplayScope(requestcontext.WithCredentials(context.Background(), creds), router.Decision{Provider: "openai", Model: "gpt-5.5"})

	assert.NotContains(t, scope, "sk-secret-value")
	assert.NotContains(t, scope, "acct-secret")
}
