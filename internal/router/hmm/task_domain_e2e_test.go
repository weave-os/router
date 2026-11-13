package hmm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/policyclient"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/taskdomain"
)

type taskOutcomeResolver struct{ outcome taskdomain.Outcome }

func (r taskOutcomeResolver) Resolve(context.Context, taskdomain.Input) taskdomain.Outcome {
	return r.outcome
}

func TestHTTPRouterCarriesTaskOutcomeThroughGoSelection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		assert.NotContains(t, request, "TaskDomain", "task text must not be added to the complexity wire contract")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"schema_version": policy.SchemaVersionV4, "classifier_artifact_id": "synthetic-classifier",
			"classifier_artifact_sha256": strings.Repeat("a", 64), "predicted_label": "low",
			"class_order": []string{"low"}, "class_probabilities": map[string]float64{"low": 1},
		})
	}))
	defer server.Close()
	task := taskdomain.Outcome{Status: taskdomain.Ready, ReleaseSHA256: strings.Repeat("b", 64), EvidenceSHA256: strings.Repeat("c", 64), Profile: taskdomain.Profile{taskdomain.UI: false, taskdomain.Logic: false, taskdomain.Data: false, taskdomain.Infra: true, taskdomain.Docs: false}}
	routed := New(policyclient.New(server.URL, server.Client(), 0, policyclient.WithTaskDomain(taskOutcomeResolver{task})), map[string]struct{}{providers.ProviderFireworks: {}})
	routed.WithArmSelector(func(_ context.Context, input policy.SelectionInput) (policy.SelectionPick, error) {
		require.NotNil(t, input.TaskDomain)
		assert.Equal(t, task, *input.TaskDomain)
		assert.Equal(t, map[string]float64{"low": 1}, input.ClassProbabilities)
		return policy.SelectionPick{Group: "low", Arm: "moonshotai/kimi-k2.7-code", Trace: policy.SelectionTrace{SelectedArm: "moonshotai/kimi-k2.7-code", SelectedGroup: "low", TaskDomain: input.TaskDomain}}, nil
	})
	decision, err := routed.Route(context.Background(), router.Request{PromptText: "Review deployment", TaskDomain: &taskdomain.Input{UserText: "Review deployment", ConversationKey: strings.Repeat("d", 64), RootSHA256: strings.Repeat("e", 64)}})
	require.NoError(t, err)
	assert.Equal(t, "moonshotai/kimi-k2.7", decision.Model)
	require.NotNil(t, decision.Metadata)
	require.NotNil(t, decision.Metadata.SelectionTrace)
	assert.Equal(t, task, *decision.Metadata.SelectionTrace.TaskDomain)
}
