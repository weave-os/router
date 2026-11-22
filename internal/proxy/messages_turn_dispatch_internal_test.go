package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

func TestMessagesEscalationCompletionSeesThePostDispatchContext(t *testing.T) {
	store := &outcomeContextEscalationStore{escalationTestStore: newEscalationTestStore()}
	provider := escalationCompletionProvider{writeResponse: func(w http.ResponseWriter) error {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte(`{"id":"msg_test","type":"message","role":"assistant","model":"claude-haiku-4-5","content":[{"type":"text","text":"repository inspected"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":3}}`))
		return err
	}}
	svc := withHMMPorts(NewService(nil, dispatch.NewClients(map[string]providers.Client{providers.ProviderAnthropic: provider}), nil, newStubPinStore(), false, providers.ProviderAnthropic, "claude-opus-4-8", nil)).
		WithEscalation(store, &escalationTestObserver{}).
		WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMMEmbedding, Router: escalationDispatchRouter{}, Capabilities: policy.Capabilities{SchemaVersion: policy.SchemaVersionV1, AuthoritativePerTurnSelection: true}})
	body := []byte(`{"model":"claude-opus-4-8","max_tokens":4096,"tools":[{"name":"Read","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"inspect the repository"}]}`)

	err := svc.ProxyMessages(escalationCompletionContext(), body, httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/messages", nil))

	require.NoError(t, err)
	require.NotNil(t, store.outcomeCtx, "the served turn must record its escalation outcome")
	claudeTurn, _ := store.outcomeCtx.Value(claudeSubscriptionTurnKey{}).(bool)
	require.True(t, claudeTurn, "escalation completion must see the sibling funding the rescue chain armed on the turn context")
}
