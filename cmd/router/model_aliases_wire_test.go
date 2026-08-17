package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"workweave/router/internal/providers"
	openaiCompatProvider "workweave/router/internal/providers/openaicompat"
	"workweave/router/internal/router"
)

// stubUpstream stands in for an OpenAI-compatible endpoint that publishes the
// catalog's models under its own names, and records the model it was sent.
func stubUpstream(t *testing.T, gotModel *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var payload struct {
			Model string `json:"model"`
		}
		require.NoError(t, json.Unmarshal(body, &payload))
		*gotModel = payload.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1","object":"chat.completion","choices":[]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func dispatch(t *testing.T, client providers.Client, modelID string) {
	t.Helper()
	prep := providers.PreparedRequest{
		Body:    []byte(`{"model":"` + modelID + `","messages":[{"role":"user","content":"hi"}]}`),
		Headers: http.Header{},
	}
	decision := router.Decision{Provider: providers.ProviderOpenRouter, Model: modelID}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", http.NoBody)
	require.NoError(t, client.Proxy(context.Background(), decision, prep, httptest.NewRecorder(), req))
}

// Reproduces #541: the router put its catalog slash-form ID on the wire and a
// gateway publishing bare names rejected it.
func TestModelAliasRewritesModelOnTheWire(t *testing.T) {
	const (
		catalogID  = "deepseek/deepseek-v4-flash"
		gatewayID  = "deepseek-v4-flash"
		otherModel = "xiaomi/mimo-v2.5-pro"
	)

	t.Run("without an alias the catalog ID reaches the endpoint", func(t *testing.T) {
		var got string
		srv := stubUpstream(t, &got)

		aliases, err := resolveModelAliases(discardLogger())
		require.NoError(t, err)
		client := openaiCompatProvider.NewClientWithModelIDMap("k", srv.URL, aliases[providers.ProviderOpenRouter])

		dispatch(t, client, catalogID)
		assert.Equal(t, catalogID, got)
	})

	t.Run("the alias puts the endpoint's own name on the wire", func(t *testing.T) {
		var got string
		srv := stubUpstream(t, &got)
		t.Setenv(modelAliasEnvVar(providers.ProviderOpenRouter), `{"`+catalogID+`":"`+gatewayID+`"}`)

		aliases, err := resolveModelAliases(discardLogger())
		require.NoError(t, err)
		client := openaiCompatProvider.NewClientWithModelIDMap("k", srv.URL, aliases[providers.ProviderOpenRouter])

		dispatch(t, client, catalogID)
		assert.Equal(t, gatewayID, got)
	})

	t.Run("an unaliased model is still sent unchanged", func(t *testing.T) {
		var got string
		srv := stubUpstream(t, &got)
		t.Setenv(modelAliasEnvVar(providers.ProviderOpenRouter), `{"`+catalogID+`":"`+gatewayID+`"}`)

		aliases, err := resolveModelAliases(discardLogger())
		require.NoError(t, err)
		client := openaiCompatProvider.NewClientWithModelIDMap("k", srv.URL, aliases[providers.ProviderOpenRouter])

		dispatch(t, client, otherModel)
		assert.Equal(t, otherModel, got)
	})
}
