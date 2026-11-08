package proxy

import (
	"context"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/auth"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBlindPassthrough_CodexPlanThrottleRollsOverOnSameModel(t *testing.T) {
	client := &codexQuotaClient{}
	svc := codexQuotaService(client)
	ctx := context.WithValue(context.Background(), auth.BlindExperimentContextKey{}, auth.BlindExperimentState{
		Active:              true,
		Arm:                 auth.BlindExperimentArmPassthrough,
		AssignmentSource:    auth.BlindExperimentAssignmentAutomatic,
		CanonicalSubjectKey: "account-1",
	})

	body := `{"model":"` + codexCoveredModel + `","stream":true,"messages":[{"role":"user","content":"read main.go"}]}`
	rec := httptest.NewRecorder()
	require.Error(t, svc.ProxyOpenAIChatCompletion(ctx, []byte(body), rec, codexSubHTTPRequest("/v1/chat/completions", body)), "the fake upstream throttles every attempt")

	require.GreaterOrEqual(t, len(client.oauthPerCall), 2, "a spent plan must roll over to credits in either arm")
	assert.True(t, client.oauthPerCall[0])
	assert.False(t, client.oauthPerCall[len(client.oauthPerCall)-1])
}

func TestBlindPassthrough_CodexResponsesPlanThrottleRollsOverOnSameModel(t *testing.T) {
	client := &codexQuotaClient{}
	svc := codexQuotaService(client)
	ctx := context.WithValue(context.Background(), auth.BlindExperimentContextKey{}, auth.BlindExperimentState{
		Active:              true,
		Arm:                 auth.BlindExperimentArmPassthrough,
		AssignmentSource:    auth.BlindExperimentAssignmentAutomatic,
		CanonicalSubjectKey: "account-1",
	})
	ctx = context.WithValue(ctx, ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})

	body := `{"model":"` + codexCoveredModel + `","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"read main.go"}]}]}`
	rec := httptest.NewRecorder()
	require.Error(t, svc.ProxyOpenAIResponses(ctx, []byte(body), rec, codexSubHTTPRequest("/v1/responses", body)), "the fake upstream throttles every attempt")

	require.GreaterOrEqual(t, len(client.oauthPerCall), 2, "a spent plan must roll over to credits in either arm")
	assert.True(t, client.oauthPerCall[0])
	assert.False(t, client.oauthPerCall[len(client.oauthPerCall)-1])
}
