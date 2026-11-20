package hmm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyclient"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
)

var weaveDecisionGroupPattern = regexp.MustCompile(`group=([a-z]+)`)

func TestDecisionReasonTextIsTheContractWeaveParses(t *testing.T) {
	tests := []struct {
		group string
		want  string
	}{
		{group: "high", want: "hmm_policy(group=high,arm=moonshotai/kimi-k2.7-code,fallback_depth=0)"},
		{group: "low", want: "hmm_policy:tool_execution(group=low,arm=moonshotai/kimi-k2.7-code,fallback_depth=0)"},
	}
	for _, tt := range tests {
		t.Run(tt.group, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"schema_version": policy.SchemaVersionV4, "classifier_artifact_id": "synthetic-classifier",
					"classifier_artifact_sha256": strings.Repeat("a", 64), "predicted_label": tt.group,
					"class_order": []string{tt.group}, "class_probabilities": map[string]float64{tt.group: 1},
				})
			}))
			defer server.Close()
			routed := New(policyclient.New(server.URL, server.Client(), 0), map[string]struct{}{providers.ProviderFireworks: {}})
			routed.WithArmSelector(func(context.Context, policy.SelectionInput) (policy.SelectionPick, error) {
				return policy.SelectionPick{Group: tt.group, Arm: "moonshotai/kimi-k2.7-code", Trace: policy.SelectionTrace{SelectedArm: "moonshotai/kimi-k2.7-code", SelectedGroup: tt.group}}, nil
			})

			decision, err := routed.Route(context.Background(), router.Request{PromptText: "synthetic prompt"})

			require.NoError(t, err)
			assert.Equal(t, tt.want, decision.Reason, "Weave parses this text; changing it needs a paired weave-os/weave change")
			match := weaveDecisionGroupPattern.FindStringSubmatch(decision.Reason)
			require.Len(t, match, 2)
			assert.Equal(t, tt.group, match[1])
		})
	}
}
