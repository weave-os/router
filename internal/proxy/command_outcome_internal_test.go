package proxy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

func readCommandOutcome(t *testing.T, recorder *httptest.ResponseRecorder) translate.CommandOutcome {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(recorder.Header().Get(translate.RouterCommandHeader))
	require.NoError(t, err)
	var outcome translate.CommandOutcome
	require.NoError(t, json.Unmarshal(raw, &outcome))
	require.Equal(t, 1, outcome.Version)
	require.NotEmpty(t, outcome.ID)
	return outcome
}

func TestCommandOutcomesAcrossSurfaces(t *testing.T) {
	for _, surface := range []translate.Format{translate.FormatAnthropic, translate.FormatOpenAI, translate.FormatOpenAIResponses} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%v/stream=%t", surface, streaming), func(t *testing.T) {
				store := newForceModelMapStore()
				svc := NewService(nil, nil, nil, false, nil, store, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil).
					WithDeploymentKeyedProviders(keyed(providers.ProviderAnthropic))
				app := ClientAppClaudeCode
				if surface != translate.FormatAnthropic {
					app = ClientAppCodex
				}
				ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: app, SessionID: "synthetic-session"})
				ctx = context.WithValue(ctx, InstallationIDContextKey{}, uuid.NewString())
				ctx = context.WithValue(ctx, APIKeyIDContextKey{}, "synthetic-key")
				seen := map[string]bool{}
				for _, command := range []struct {
					text        string
					kind        translate.CommandKind
					transition  translate.PinTransition
					disposition translate.CommandDisposition
					excluded    bool
				}{
					{"/fm opus:high", translate.CommandForceModel, translate.PinApplied, translate.CommandAccepted, false},
					{"/force-parent opus:high", translate.CommandForceModel, translate.PinApplied, translate.CommandAccepted, false},
					{"/router-session", translate.CommandRouterSession, translate.PinUnchanged, translate.CommandAccepted, false},
					{"/router-models", translate.CommandRouterModels, translate.PinUnchanged, translate.CommandAccepted, false},
					{"/rf+", translate.CommandRouterFeedback, translate.PinUnchanged, translate.CommandAccepted, false},
					{"/fm synthetic-unknown", translate.CommandForceModel, translate.PinUnchanged, translate.CommandRejected, false},
					{"/fm opus", translate.CommandForceModel, translate.PinUnchanged, translate.CommandRejected, true},
					{"/unforce-model", translate.CommandForceModel, translate.PinCleared, translate.CommandAccepted, false},
				} {
					body := map[string]any{"model": "claude-opus-4-8", "stream": streaming, "max_tokens": 100}
					input := []map[string]string{{"role": "user", "content": command.text}}
					if surface == translate.FormatOpenAIResponses {
						body["input"] = input
					} else {
						body["messages"] = input
					}
					encoded, err := json.Marshal(body)
					require.NoError(t, err)
					rec := httptest.NewRecorder()
					req := httptest.NewRequest(http.MethodPost, "/", nil)
					requestContext := ctx
					if command.excluded {
						requestContext = context.WithValue(ctx, InstallationExcludedModelsContextKey{}, []string{"claude-opus-5-5"})
					}
					switch surface {
					case translate.FormatAnthropic:
						err = svc.ProxyMessages(requestContext, encoded, rec, req)
					case translate.FormatOpenAI:
						err = svc.ProxyOpenAIChatCompletion(requestContext, encoded, rec, req)
					case translate.FormatOpenAIResponses:
						err = svc.ProxyOpenAIResponses(requestContext, encoded, rec, req)
					}
					require.NoError(t, err)
					outcome := readCommandOutcome(t, rec)
					assert.Equal(t, command.kind, outcome.Command)
					assert.Equal(t, command.transition, outcome.Pin.Transition)
					assert.Equal(t, command.disposition, outcome.Disposition)
					assert.False(t, seen[outcome.ID])
					seen[outcome.ID] = true
					if outcome.Pin.Transition == translate.PinApplied {
						assert.Equal(t, "claude-opus-5-5", outcome.Pin.Model)
						assert.Equal(t, providers.ProviderAnthropic, outcome.Pin.Provider)
						assert.Equal(t, "high", outcome.Pin.Effort)
					}
					if surface == translate.FormatAnthropic {
						if streaming {
							assert.Contains(t, rec.Body.String(), `"router_command":`)
						} else {
							assert.JSONEq(t, string(mustJSON(t, outcome)), gjson.Get(rec.Body.String(), "router_command").Raw)
						}
						assert.NotContains(t, rec.Body.String(), "[weave-router-command-v1]")
					} else {
						// The carrier is part of output_text after the actual Chat→Responses conversion.
						assert.Contains(t, rec.Body.String(), "[weave-router-command-v1]")
						assert.Contains(t, rec.Body.String(), "weave-router:"+rec.Header().Get(translate.RouterCommandHeader))
					}
					for _, pin := range store.pins {
						if pin.Role != forceModelSessionRole {
							continue
						}
						if command.transition == translate.PinCleared {
							assert.Equal(t, userUnforcedReason, pin.Reason)
						} else {
							assert.Equal(t, "claude-opus-5-5", pin.Model)
						}
					}
				}
			})
		}
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return encoded
}

type failedCommandStore struct{ *forceModelMapStore }

func (*failedCommandStore) Upsert(context.Context, sessionpin.Pin) error {
	return errors.New("synthetic persistence failure")
}

func TestCommandOutcomeRequiresPersistence(t *testing.T) {
	store := &failedCommandStore{newForceModelMapStore()}
	svc := NewService(nil, nil, nil, false, nil, store, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAnthropic))
	env := forceCommandEnv(t)
	for _, command := range []translate.ForceModelResult{{Model: "opus"}, {Clear: true}} {
		rec := httptest.NewRecorder()
		err := svc.handleForceModelCommand(context.Background(), rec, env, command, uuid.New(), [sessionpin.SessionKeyLen]byte{1}, [sessionpin.SessionKeyLen]byte{2}, 1)
		require.Error(t, err)
		assert.Empty(t, rec.Header().Get(translate.RouterCommandHeader))
		assert.Empty(t, rec.Body.String())
	}
}

func TestLoopStopsPreserveHistoricalForcePin(t *testing.T) {
	svc := NewService(nil, nil, nil, false, nil, newForceModelMapStore(), false, providers.ProviderAnthropic, "claude-haiku-4-5", nil)
	env := forceCommandEnv(t)
	for _, repetition := range []bool{false, true} {
		rec := httptest.NewRecorder()
		var err error
		if repetition {
			err = svc.handleTextRepetitionBreak(context.Background(), rec, env, 3, "synthetic", uuid.New(), [sessionpin.SessionKeyLen]byte{1}, "maximum", "synthetic-model", providers.ProviderAnthropic, 1)
		} else {
			err = svc.handleNoProgressBreak(context.Background(), rec, env, 3, uuid.New(), [sessionpin.SessionKeyLen]byte{1}, "maximum", "synthetic-model", providers.ProviderAnthropic, 1, translate.ReasonUserForceModel)
		}
		require.NoError(t, err)
		outcome := readCommandOutcome(t, rec)
		assert.Equal(t, translate.CommandLoopStop, outcome.Command)
		assert.Equal(t, translate.PinUnchanged, outcome.Pin.Transition)
	}
}

type commandWriteFailure struct{ *httptest.ResponseRecorder }

func (*commandWriteFailure) Write([]byte) (int, error) { return 0, errors.New("synthetic disconnect") }

func TestAcknowledgmentWriteFailureDoesNotUndoPin(t *testing.T) {
	store := newForceModelMapStore()
	svc := NewService(nil, nil, nil, false, nil, store, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAnthropic))
	key := [sessionpin.SessionKeyLen]byte{2}
	err := svc.handleForceModelCommand(context.Background(), &commandWriteFailure{httptest.NewRecorder()}, forceCommandEnv(t), translate.ForceModelResult{Model: "opus"}, uuid.New(), [sessionpin.SessionKeyLen]byte{1}, key, 1)
	require.Error(t, err)
	pin, found, err := store.Get(context.Background(), key, forceModelSessionRole)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "claude-opus-5-5", pin.Model)
}

func TestHeaderPersistenceFailureRetainsSameActionForce(t *testing.T) {
	store := &failedCommandStore{newForceModelMapStore()}
	svc := NewService(nil, nil, nil, false, nil, store, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAnthropic))
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	request.Header.Set(ForceModelHeader, "opus:high")
	_, model, err := svc.applyForceModelHeader(context.Background(), request, uuid.New(), [sessionpin.SessionKeyLen]byte{1})
	require.NoError(t, err)
	assert.Equal(t, "claude-opus-5-5:high", model)
	assert.Empty(t, store.pins)
}
