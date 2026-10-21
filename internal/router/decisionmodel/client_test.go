package decisionmodel_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/router/decisionmodel"

	"github.com/stretchr/testify/require"
)

func TestClientSendsZeroRetentionChoiceQuestions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/systemone", r.URL.Path)
		require.Equal(t, "Bearer key", r.Header.Get("Authorization"))
		var body struct {
			Model    string `json:"model"`
			Provider struct {
				Only           []string `json:"only"`
				AllowFallbacks bool     `json:"allow_fallbacks"`
				ZDR            bool     `json:"zdr"`
				DataCollection string   `json:"data_collection"`
			} `json:"provider"`
			Questions map[string]struct {
				Criteria map[string]string `json:"criteria"`
			} `json:"questions"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, decisionmodel.Model, body.Model)
		require.Equal(t, []string{"typesafe"}, body.Provider.Only)
		require.False(t, body.Provider.AllowFallbacks)
		require.True(t, body.Provider.ZDR)
		require.Equal(t, "deny", body.Provider.DataCollection)
		require.Contains(t, body.Questions["call_complexity"].Criteria, "high")
		_, _ = w.Write([]byte(`{"answers":{"call_complexity":{"type":"choice","choice":"low","probabilities":{"high":0.2,"low":0.8},"confidence":0.6}}}`))
	}))
	defer server.Close()
	answers, err := decisionmodel.NewClient(server.URL+"/api/v1", "key", decisionmodel.DefaultTimeout).Classify(context.Background(), decisionmodel.State{}, []decisionmodel.QuestionID{decisionmodel.QuestionCallComplexity})
	require.NoError(t, err)
	require.Equal(t, decisionmodel.ChoiceLow, answers[decisionmodel.QuestionCallComplexity].Choice)
}

func TestClientRejectsUnknownLabels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"answers":{"escalate":{"type":"choice","choice":"maybe"}}}`))
	}))
	defer server.Close()
	_, err := decisionmodel.NewClient(server.URL, "key", decisionmodel.DefaultTimeout).Classify(context.Background(), decisionmodel.State{}, []decisionmodel.QuestionID{decisionmodel.QuestionEscalate})
	require.ErrorContains(t, err, "unknown label")
}
