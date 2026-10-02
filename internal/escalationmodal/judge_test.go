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

func TestJudgeAcceptsOnlyPinnedDigitVerdict(t *testing.T) {
	apiKey := "a-dedicated-classifier-key-with-enough-entropy"
	modelHash := "620f908e24268bf9f116d533cc1d42f825f2a38599374c3db63c5db210b783d6"
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/classify", request.URL.Path)
		require.Equal(t, "Bearer "+apiKey, request.Header.Get("Authorization"))
		var input struct {
			User string `json:"user"`
		}
		require.NoError(t, json.NewDecoder(request.Body).Decode(&input))
		require.Equal(t, "five completed turns", input.User)
		require.NoError(t, json.NewEncoder(writer).Encode(map[string]any{
			"release_name":  "qwen-finetuned-escalation-classifier",
			"model_sha256":  modelHash,
			"prediction":    1,
			"raw_is_digit":  true,
			"probabilities": []float64{0.1, 0.9},
			"input_tokens":  250,
		}))
	}))
	defer server.Close()
	judge, err := escalationmodal.NewJudge(server.URL, apiKey, server.Client())
	require.NoError(t, err)
	verdict, err := judge.Judge(context.Background(), llmescalation.JudgeRequest{Transcript: "five completed turns"})
	require.NoError(t, err)
	require.True(t, verdict.Escalate)
	require.Equal(t, 250, verdict.Usage.InputTokens)

	modelHash = "wrong-release"
	_, err = judge.Judge(context.Background(), llmescalation.JudgeRequest{Transcript: "five completed turns"})
	require.ErrorIs(t, err, llmescalation.ErrInvalidJudgment)
}
