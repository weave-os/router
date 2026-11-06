package proxy

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"
)

// End to end from the typed command line to the written pin: the parser must
// hand the resolver the whole name, and the ack must name the pinned model.
func TestForceModelCommand_TypedForms(t *testing.T) {
	tests := []struct {
		typed      string
		wantModel  string
		wantEffort string
	}{
		{"/fm opus", "claude-opus-5-5", ""},
		{"/fm claude-opus-5.5", "claude-opus-5-5", ""},
		{"/fm claude-opus-5-5", "claude-opus-5-5", ""},
		{"/fm opus 5.5", "claude-opus-5-5", ""},
		{"/fm   opus   5.5  ", "claude-opus-5-5", ""},
		{"/force-model claude-opus-5.5", "claude-opus-5-5", ""},
		{"/model opus-4.8", "claude-opus-4-8", ""},
		{"$fm sol", "gpt-6.1-sol", ""},
		{"/fm sol:high", "gpt-6.1-sol", "high"},
		{"/fm astra high", "gpt-6-astra", "high"},
		{"/fm claude-opus-5-5[1m]", "claude-opus-5-5", ""},
		{"/fm qwen 3.8", "qwen/qwen3.8-max", ""},
		{"<command-name>/fm</command-name>\n/fm claude-opus-5.5", "claude-opus-5-5", ""},
	}
	for _, tt := range tests {
		t.Run(tt.typed, func(t *testing.T) {
			store := &recordingPinStore{}
			svc := NewService(nil, nil, nil, false, nil, store, false,
				providers.ProviderAnthropic, "claude-haiku-4-5", nil)
			env := typedCommandEnv(t, tt.typed+"\nfix the failing test")
			cmd, found := env.ExtractForceModelCommand()
			require.True(t, found, "command parsed")

			rec := httptest.NewRecorder()
			require.NoError(t, svc.handleForceModelCommand(context.Background(), rec, env, cmd,
				uuid.New(), DeriveSessionKey(env, "key-1"), DeriveSessionKey(env, "key-1"), 10))

			require.Len(t, store.upserts, 1, "pin written")
			assert.Equal(t, tt.wantModel, store.upserts[0].Model)
			assert.Equal(t, tt.wantEffort, store.upserts[0].Effort)
			assert.Contains(t, rec.Body.String(), "force-model applied: "+tt.wantModel)
		})
	}
}

// A name that resolves to no model writes no pin and keeps the ack wording the
// statusline scripts match on.
func TestForceModelCommand_TypedUnknownKeepsRouting(t *testing.T) {
	for _, typed := range []string{"/fm opsu", "/fm opus-5-", "/fm gpt-5.6-astra", "/fm opus fix the tests"} {
		t.Run(typed, func(t *testing.T) {
			store := &recordingPinStore{}
			svc := NewService(nil, nil, nil, false, nil, store, false,
				providers.ProviderAnthropic, "claude-haiku-4-5", nil)
			env := typedCommandEnv(t, typed)
			cmd, found := env.ExtractForceModelCommand()
			require.True(t, found, "command parsed")

			rec := httptest.NewRecorder()
			require.NoError(t, svc.handleForceModelCommand(context.Background(), rec, env, cmd,
				uuid.New(), DeriveSessionKey(env, "key-1"), DeriveSessionKey(env, "key-1"), 10))

			assert.Empty(t, store.upserts, "no pin")
			assert.Contains(t, rec.Body.String(), "isn't a recognized model")
		})
	}
}

func TestForceModelCommand_TypoSuggestsWithoutPinning(t *testing.T) {
	store := &recordingPinStore{}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil)
	env := typedCommandEnv(t, "/fm opsu")
	cmd, found := env.ExtractForceModelCommand()
	require.True(t, found, "command parsed")

	rec := httptest.NewRecorder()
	require.NoError(t, svc.handleForceModelCommand(context.Background(), rec, env, cmd,
		uuid.New(), DeriveSessionKey(env, "key-1"), DeriveSessionKey(env, "key-1"), 10))

	assert.Empty(t, store.upserts, "a suggestion never pins")
	assert.Contains(t, rec.Body.String(), "isn't a recognized model")
	assert.Contains(t, rec.Body.String(), `Did you mean \"opus\"?`)
}

func typedCommandEnv(t *testing.T, text string) *translate.RequestEnvelope {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"model":    "claude-opus-4-8",
		"messages": []map[string]any{{"role": "user", "content": text}},
	})
	require.NoError(t, err)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	return env
}
