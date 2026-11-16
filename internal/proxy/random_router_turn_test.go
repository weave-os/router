package proxy

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/sjson"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	randomrouter "weave-os/router/internal/router/random_router"
	"weave-os/router/internal/subscriptions"
)

func TestRandomResponsesDrawForMainToolAndSubagent(t *testing.T) {
	config := randomTestConfig()
	success := fakeOutcome{writeBytes: []byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"done\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")}
	client := &fakeClient{name: providers.ProviderOpenAI, outcomes: []fakeOutcome{success, success, success}}
	draws := 0
	selection := randomrouter.New(func(int) (int, error) { index := draws; draws++; return index, nil })
	svc := NewService(nil, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, nil, false, providers.ProviderOpenAI, catalog.ModelGPT61Sol.String(), nil).
		WithManagedSubscriptions(&scriptedSubscriptionLeaser{}).
		WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyRandomBenchmark, Router: selection, Unavailable: randomrouter.ErrUnavailable})
	encoded, err := json.Marshal(config)
	require.NoError(t, err)
	for index, shape := range []struct {
		agent      string
		toolResult bool
	}{{"/root", false}, {"/root", true}, {"/root/worker", true}} {
		nativeCtx, _ := codexRoutingEnvelope(t, shape.agent, shape.toolResult)
		body, err := sjson.SetBytes(nativeCtx.Value(nativeResponsesBodyContextKey{}).([]byte), "stream", true)
		require.NoError(t, err)
		ctx := flags.WithOverrides(randomTestContext(config), flags.Overrides{Strings: map[flags.Key]string{flags.KeyRandomBenchmarkConfig: string(encoded)}})
		ctx, _, err = BeginRandomBenchmark(svc, ctx, config.PolicySHA256)
		require.NoError(t, err)
		ctx = context.WithValue(ctx, ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex, SessionID: "same-session"})
		ctx = WithManagedSubscriptionUsage(ctx)
		state := randomrouter.RequestFrom(ctx)
		state.Prepared = true
		state.Lease = subscriptions.Lease{AccountID: config.SubscriptionAccountID, State: auth.SubscriptionAccountStateActive}
		state.Credentials = &Credentials{OAuth: true, Source: credSourceCodexSubscription, APIKey: []byte("synthetic"), AccountID: []byte("synthetic-account")}
		request := httptest.NewRequestWithContext(ctx, http.MethodPost, randomrouter.ResponsesPath, strings.NewReader(string(body)))
		request.Header.Set("Session-Id", "same-session")
		response := httptest.NewRecorder()
		require.NoError(t, svc.ProxyOpenAIResponses(ctx, body, response, request))
		require.Equal(t, index+1, draws)
		require.Equal(t, index+1, client.calls)
		require.Equal(t, randomrouter.Models()[index], response.Header().Get(HeaderRouterModel))
	}
}
