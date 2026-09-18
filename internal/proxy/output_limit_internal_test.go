package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const (
	outputLimitFirstModel  = "claude-sonnet-5"
	outputLimitSecondModel = "claude-opus-5"
)

func TestMaxedOutServedModel_RequiresMatchingTerminalEvidence(t *testing.T) {
	ended := time.Unix(100, 0)
	for _, output := range []int{128, 8192, 32000, 64000} {
		for _, tc := range []struct {
			name   string
			marker time.Time
			want   string
		}{
			{"legacy unknown", time.Time{}, ""},
			{"older writer replaced usage", ended.Add(-time.Second), ""},
			{"confirmed cap", ended, outputLimitFirstModel},
		} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, output), func(t *testing.T) {
				pin := sessionpin.Pin{
					Model: outputLimitSecondModel, LastServedModel: outputLimitFirstModel + ":high",
					LastOutputTokens: output, LastTurnEndedAt: ended, LastOutputLimitAt: tc.marker,
				}
				assert.Equal(t, tc.want, maxedOutServedModel(pin))
				pin.LastServedModel = ""
				want := ""
				if tc.want != "" {
					want = outputLimitSecondModel
				}
				assert.Equal(t, want, maxedOutServedModel(pin), "legacy serving identity falls back to the anchor")
			})
		}
	}
}

func TestNormalizeHMMStayPin_HighOutputIsNotExhaustion(t *testing.T) {
	svc := NewService(nil, map[string]providers.Client{providers.ProviderAnthropic: nil}, nil, false, nil, nil, false,
		providers.ProviderAnthropic, outputLimitFirstModel, nil)
	pin := sessionpin.Pin{Provider: providers.ProviderAnthropic, LastServedModel: outputLimitFirstModel,
		LastOutputTokens: 32000, LastTurnEndedAt: time.Now().Add(-time.Second), PinnedUntil: time.Now().Add(time.Hour)}
	got, ok := svc.normalizeHMMStayPin(router.Request{}, pin)
	require.True(t, ok)
	assert.Equal(t, outputLimitFirstModel, got.Model)
	pin.LastOutputLimitAt = pin.LastTurnEndedAt
	_, ok = svc.normalizeHMMStayPin(router.Request{}, pin)
	assert.False(t, ok)
}

func TestPostCommandContinuation_UsesTerminalEvidence(t *testing.T) {
	for _, capped := range []bool{false, true} {
		t.Run(fmt.Sprintf("capped=%t", capped), func(t *testing.T) {
			store := newForceModelMapStore()
			key := [sessionpin.SessionKeyLen]byte{1}
			pin := sessionpin.Pin{SessionKey: key, Role: sessionpin.DefaultRole, Strategy: router.StrategyCluster,
				Provider: providers.ProviderAnthropic, Model: outputLimitFirstModel, LastServedModel: outputLimitFirstModel,
				LastOutputTokens: 32000, LastTurnEndedAt: time.Now(), PinnedUntil: time.Now().Add(time.Hour), Reason: "cluster"}
			if capped {
				pin.LastOutputLimitAt = pin.LastTurnEndedAt
			}
			store.pins[forceModelMapKey(key, pin.Role)] = pin
			svc := NewService(nil, nil, nil, false, nil, store, false, providers.ProviderAnthropic, outputLimitFirstModel, nil)
			ctx := router.WithStrategy(context.Background(), router.StrategyCluster)
			svc.grantPostCommandContinuation(ctx, uuid.New(), key, pin.Role)
			got, found := svc.consumePostCommandContinuation(ctx, key, pin.Role)
			assert.Equal(t, !capped, found)
			if found {
				assert.Equal(t, outputLimitFirstModel, got.Model)
			}
			_, found = svc.consumePostCommandContinuation(ctx, key, pin.Role)
			assert.False(t, found, "a continuation remains one-shot")
		})
	}
}

func TestRefreshPin_PreservesOutputLimitEvidence(t *testing.T) {
	store := newStubPinStore()
	svc := NewService(nil, nil, nil, false, nil, store, false, providers.ProviderAnthropic, outputLimitFirstModel, nil)
	ended := time.Now().Add(-time.Second)
	pin := sessionpin.Pin{Provider: providers.ProviderAnthropic, Model: outputLimitSecondModel, LastServedModel: outputLimitFirstModel,
		LastTurnEndedAt: ended, LastOutputLimitAt: ended, LastOutputTokens: 128}
	svc.refreshPin(context.Background(), uuid.New(), [sessionpin.SessionKeyLen]byte{1}, pin, sessionpin.DefaultRole,
		router.Decision{Provider: providers.ProviderAnthropic, Model: outputLimitSecondModel})
	require.Len(t, store.upserts, 1)
	assert.Equal(t, outputLimitFirstModel, maxedOutServedModel(store.upserts[0]), "refreshing the anchor cannot lose the prior served outcome")
}

type outputLimitSequenceRouter struct {
	choices  []string
	requests []router.Request
}

func (r *outputLimitSequenceRouter) Route(_ context.Context, req router.Request) (router.Decision, error) {
	r.requests = append(r.requests, req)
	for _, model := range r.choices {
		if _, excluded := req.ExcludedModels[model]; excluded {
			continue
		}
		return router.Decision{Provider: providers.ProviderAnthropic, Model: model, Reason: "hmm_policy(label=maximum)",
			Metadata: &router.RoutingMetadata{Strategy: string(router.StrategyHMM)}}, nil
	}
	return router.Decision{}, errors.New("no eligible test model")
}

type outputLimitSequenceProvider struct {
	stopReason string
	output     int
}

func (p *outputLimitSequenceProvider) Proxy(_ context.Context, _ router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	if gjson.GetBytes(prep.Body, "stream").Bool() {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, err := fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"content\":[],\"usage\":{\"input_tokens\":160000}}}\n\n"+
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_1\",\"name\":\"Read\",\"input\":{}}}\n\n"+
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{}\"}}\n\n"+
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":%q},\"usage\":{\"output_tokens\":%d}}\n\n"+
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", p.stopReason, p.output)
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, err := fmt.Fprintf(w, `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"Read","input":{}}],"stop_reason":%q,"usage":{"input_tokens":160000,"output_tokens":%d}}`, p.stopReason, p.output)
	return err
}

func (*outputLimitSequenceProvider) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return errors.New("unexpected passthrough")
}

func TestProxyMessages_OutputLimitEightActionSequence(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, trueCap := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/trueCap=%t", stream, trueCap), func(t *testing.T) {
				store := newForceModelMapStore()
				routes := &outputLimitSequenceRouter{choices: []string{outputLimitFirstModel, outputLimitSecondModel}}
				upstream := &outputLimitSequenceProvider{stopReason: "tool_use", output: 32000}
				ctx := context.WithValue(context.Background(), APIKeyIDContextKey{}, "output-limit-key")
				ctx = context.WithValue(ctx, InstallationIDContextKey{}, uuid.NewString())
				ctx = router.WithStrategy(ctx, router.StrategyHMM)
				messages := []any{map[string]any{"role": "user", "content": "Inspect the repository files"}}
				for action := 0; action < 8; action++ {
					upstream.stopReason, upstream.output = "tool_use", 32000
					if trueCap && action == 3 {
						upstream.stopReason, upstream.output = "max_tokens", 128
					}
					svc := NewService(nil, map[string]providers.Client{providers.ProviderAnthropic: upstream}, nil, false, nil, store, false,
						providers.ProviderAnthropic, outputLimitFirstModel, nil).
						WithNativeAnthropicResponseSignals(false).
						WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMM, Router: routes,
							Capabilities: policy.Capabilities{SchemaVersion: policy.SchemaVersionV1, AuthoritativePerTurnSelection: true}})
					body, err := json.Marshal(map[string]any{"model": outputLimitSecondModel, "max_tokens": 64000, "stream": stream,
						"messages": messages, "tools": []any{map[string]any{"name": "Read", "description": "Read a file", "input_schema": map[string]any{"type": "object"}}}})
					require.NoError(t, err)
					rec := httptest.NewRecorder()
					req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body)))
					require.NoError(t, svc.ProxyMessages(ctx, body, rec, req))
					want := outputLimitFirstModel
					if trueCap && action == 4 {
						want = outputLimitSecondModel
					}
					assert.Equal(t, want, rec.Header().Get(HeaderRouterModel), "action %d", action)
					require.Len(t, routes.requests, action+1)
					if trueCap && action == 4 {
						assert.Contains(t, routes.requests[action].SafetyExcludedModels, outputLimitFirstModel)
					} else {
						assert.NotContains(t, routes.requests[action].SafetyExcludedModels, outputLimitFirstModel)
						assert.NotContains(t, routes.requests[action].ExcludedModels, outputLimitFirstModel)
					}
					messages = append(messages,
						map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": fmt.Sprintf("call_%d", action), "name": "Read", "input": map[string]any{}}}},
						map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": fmt.Sprintf("call_%d", action), "content": fmt.Sprintf("file %d inspected", action)}}})
				}
			})
		}
	}
}
