package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
)

// The Codex force-model skill ends its turn once emit.sh prints the
// directive. Dispatching that turn hands the model a tool output the router
// has already emptied, and a model that obeys the skill returns no text, which
// the empty-completion guard turns into a retried 502.
func TestCodexForceModelSkillToolResultIsAnsweredByRouter(t *testing.T) {
	store := &recordingPinStore{}
	upstream := &codexTitleProvider{}
	svc := NewService(&codexTitleRouter{}, map[string]providers.Client{providers.ProviderOpenAI: upstream},
		nil, store, false, providers.ProviderOpenAI, "gpt-6.1-sol", nil)
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})
	ctx = context.WithValue(ctx, InstallationIDContextKey{}, uuid.New().String())
	ctx = context.WithValue(ctx, APIKeyIDContextKey{}, "synthetic-api-key")
	body := []byte(`{"model":"gpt-6.1-sol","stream":true,"input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"$force-model gpt-6-astra xhigh"}]},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"<skill>\n<name>force-model</name>\nrun emit.sh\n</skill>"}]},` +
		`{"type":"custom_tool_call","call_id":"call_skill","name":"exec","input":"/home/dev/.codex/skills/force-model/scripts/emit.sh 'gpt-6-astra xhigh'"},` +
		`{"type":"custom_tool_call_output","call_id":"call_skill","output":[{"type":"input_text","text":"Script completed\nOutput:\n"},{"type":"input_text","text":" /force-model gpt-6-astra xhigh\n"}]}]}`)
	rec := httptest.NewRecorder()

	require.NoError(t, svc.ProxyOpenAIResponses(ctx, body, rec, httptest.NewRequest(http.MethodPost, "/v1/responses", nil)))

	assert.Empty(t, upstream.models, "the skill's directive turn must not dispatch a model call")
	require.NotEmpty(t, store.upserts, "pin written")
	assert.Equal(t, forceModelSessionRole, store.upserts[0].Role)
	assert.Equal(t, "gpt-6-astra", store.upserts[0].Model)
	assert.Equal(t, "xhigh", store.upserts[0].Effort)
	assert.Contains(t, rec.Body.String(), "force-model applied: gpt-6-astra")
}
