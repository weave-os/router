package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selection"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
)

type recoveryClassifier struct{ calls int }

func (c *recoveryClassifier) Decide(context.Context, policy.Query) (policy.Result, error) {
	c.calls++
	group := string(escalation.High)
	return policy.Result{SchemaVersion: policy.SchemaVersionV4, ClassOrder: []string{group},
		PredictedLabel: group, ClassProbabilities: map[string]float64{group: 1}}, nil
}

func TestSubscriptionSelectionRecoveryServesAfterSessionStrike(t *testing.T) {
	model := catalog.ModelIDGPT56Luna.String()
	var bearers []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveSyntheticCodexQuota(w, r) {
			return
		}
		bearers = append(bearers, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"recovered answer\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":11,\"output_tokens\":7}}}\n\n")
	}))
	defer upstream.Close()
	client := openai.NewClient("", upstream.URL)
	client.SetCodexBaseURL(upstream.URL)
	classifier := &recoveryClassifier{}
	policyRouter := hmm.NewForStrategy(router.StrategyHMMEmbedding, classifier, modelSet([]string{providers.ProviderOpenAI}))
	group := string(escalation.High)
	policyRouter.WithArmSelector(selection.Selector(&rosterdata.Roster{
		ClassOrder: []string{group}, Clusters: map[string]rosterdata.Cluster{group: {Arms: []string{providers.ProviderOpenAI + "/" + model}}},
	}))
	pins := &overwritingPinStore{found: true, pin: sessionpin.Pin{
		Provider: providers.ProviderOpenAI, Model: model, Reason: "hmm_policy(group=high)",
		PinnedUntil: pinNeverExpires, DemotedModels: []string{model},
	}}
	svc := NewService(nil, map[string]providers.Client{providers.ProviderOpenAI: client}, nil, false, nil, pins, false, providers.ProviderOpenAI, model, nil).
		WithManagedSubscriptions(&verificationAlternativeLeaser{}).
		WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMMEmbedding, Router: policyRouter})
	ctx := router.WithStrategy(managedSubscriptionContext(auth.SubscriptionProviderCodex), router.StrategyHMMEmbedding)
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenActiveContextKey{}, []string{model})
	body := []byte(`{"model":"weave-auto","stream":true,"messages":[{"role":"user","content":"inspect synthetic input"}]}`)
	recorder := httptest.NewRecorder()
	err := svc.ProxyOpenAIChatCompletion(ctx, body, recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body))))
	require.NoError(t, err)
	require.Contains(t, recorder.Body.String(), "recovered answer")
	require.Equal(t, []string{"Bearer alternative-seat"}, bearers, "recovery must stay on included funding")
	require.Equal(t, model, recorder.Header().Get(HeaderRouterModel))
	require.True(t, servedOnSubscription(ctx))
}
