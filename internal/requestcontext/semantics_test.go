package requestcontext_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/requestcontext"
)

func TestContentLoggingDefaultsToAllowed(t *testing.T) {
	ctx := context.Background()
	assert.True(t, requestcontext.ContentLoggingAllowed(ctx), "non-proxy callers keep diagnostics")
	assert.False(t, requestcontext.ContentLoggingAllowed(requestcontext.WithContentLogging(ctx, false)))
}

func TestServingStateKeyNamespacesOnlyManagedRequests(t *testing.T) {
	key := []byte("conversation")
	assert.Equal(t, key, requestcontext.ServingStateKey(context.Background(), key), "legacy requests keep their key")
	first := requestcontext.ServingStateKey(requestcontext.WithServingIdentity(context.Background(), requestcontext.ServingIdentity{StateNamespace: "a"}), key)
	second := requestcontext.ServingStateKey(requestcontext.WithServingIdentity(context.Background(), requestcontext.ServingIdentity{StateNamespace: "b"}), key)
	assert.NotEqual(t, key, first)
	assert.NotEqual(t, first, second)
}

func TestInternalTestIdentityReplacesAssertedAttribution(t *testing.T) {
	ctx := requestcontext.WithInternalTestIdentity(context.Background(), requestcontext.InternalTestIdentity{SubjectID: "subject", SessionID: "launch"})
	ctx = requestcontext.WithClientIdentity(ctx, requestcontext.ClientIdentity{Email: "a@example.com", AccountID: "acct", SessionID: "client", Eval: true, RolloutID: "r"})
	got := requestcontext.ClientIdentityFrom(ctx)
	assert.Equal(t, requestcontext.ClientIdentity{AccountID: "subject", SessionID: "launch"}, got)
}

func TestAnthropicRejectsNonAnthropicXAPIKey(t *testing.T) {
	headers := http.Header{"X-Api-Key": {"sk-proj-openai"}}
	assert.Nil(t, requestcontext.ExtractClientCredentials("anthropic", headers))
}

func TestAnthropicBearerIsNotForwardedToOpenAICompat(t *testing.T) {
	headers := http.Header{"Authorization": {"Bearer sk-ant-api-key"}}
	assert.Nil(t, requestcontext.ExtractClientCredentials("openai", headers))
}

func TestCodexCredentialsUseAccountAsPrincipal(t *testing.T) {
	creds := requestcontext.CodexSubscriptionCreds("jwt", "acct")
	principal, secret := creds.UpstreamPrincipal()
	assert.Equal(t, "chatgpt-account:acct", principal)
	assert.False(t, secret)
	assert.Nil(t, requestcontext.CodexSubscriptionCreds("sk-live", "acct"))
}

func TestNilCredentialsMeansDeploymentKey(t *testing.T) {
	ctx := requestcontext.WithCredentials(context.Background(), nil)
	assert.Nil(t, requestcontext.CredentialsFromContext(ctx))
	assert.Equal(t, "https://deploy", requestcontext.EffectiveBaseURL(ctx, "https://deploy"))
}
