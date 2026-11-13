package escalationmodal_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/escalationmodal"
	"weave-os/router/internal/router/llmescalation"
)

func TestJudgeRecordsServedReleaseAndRejectsMalformedIdentity(t *testing.T) {
	apiKey := "a-dedicated-classifier-key-with-enough-entropy"
	releaseName := "qwen-escalation-v1.1.0"
	modelHash := "620f908e24268bf9f116d533cc1d42f825f2a38599374c3db63c5db210b783d6"
	promptHash := "6fc829a3bc36d3f5c2cd2705ee28ba3d4f61dc5380313adf2012bfab22a8f9a0"
	served := map[string]any{"release_name": releaseName, "model_sha256": modelHash, "prompt_sha256": promptHash}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/classify", request.URL.Path)
		require.Equal(t, "Bearer "+apiKey, request.Header.Get("Authorization"))
		require.Equal(t, llmescalation.QwenRendererVersion, request.Header.Get(llmescalation.QwenRendererHeader))
		var input struct {
			User string `json:"user"`
		}
		require.NoError(t, json.NewDecoder(request.Body).Decode(&input))
		require.Equal(t, "five completed turns", input.User)
		response := map[string]any{"prediction": 1, "raw_is_digit": true, "probabilities": []float64{0.1, 0.9}, "input_tokens": 250}
		for field, value := range served {
			response[field] = value
		}
		require.NoError(t, json.NewEncoder(writer).Encode(response))
	}))
	defer server.Close()
	judge, err := escalationmodal.NewJudge(server.URL, apiKey, server.Client())
	require.NoError(t, err)
	verdict, err := judge.Judge(context.Background(), llmescalation.JudgeRequest{Transcript: "five completed turns"})
	require.NoError(t, err)
	require.True(t, verdict.Escalate)
	require.Equal(t, 250, verdict.Usage.InputTokens)
	require.Equal(t, &llmescalation.ServedRelease{Name: releaseName, ModelSHA256: modelHash, PromptSHA256: promptHash}, verdict.Release)

	served = map[string]any{"release_name": releaseName, "model_sha256": modelHash}
	verdict, err = judge.Judge(context.Background(), llmescalation.JudgeRequest{Transcript: "five completed turns"})
	require.NoError(t, err, "a release that omits prompt_sha256 predates prompt attribution")
	require.Equal(t, &llmescalation.ServedRelease{Name: releaseName, ModelSHA256: modelHash}, verdict.Release)

	for _, malformed := range []struct{ release, model, prompt string }{
		{releaseName, "not-a-digest", promptHash},
		{"", modelHash, promptHash},
		{"Release With Spaces", modelHash, promptHash},
		{releaseName, modelHash, "short"},
		{releaseName, modelHash, ""},
	} {
		served = map[string]any{"release_name": malformed.release, "model_sha256": malformed.model, "prompt_sha256": malformed.prompt}
		_, err = judge.Judge(context.Background(), llmescalation.JudgeRequest{Transcript: "five completed turns"})
		require.ErrorIs(t, err, llmescalation.ErrInvalidJudgment, malformed)
	}
}
