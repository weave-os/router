package proxy

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selection"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

type slidingTestClassifier struct {
	classification policy.Result
}

func (c slidingTestClassifier) Decide(context.Context, policy.Query) (policy.Result, error) {
	return c.classification, nil
}

func TestAuthoritativeSlidingUpgradeUsesClassifiedGroup(t *testing.T) {
	models := map[escalation.Group]catalog.ModelID{
		escalation.Low:     catalog.ModelIDClaudeHaiku45,
		escalation.Medium:  catalog.ModelIDClaudeSonnet46,
		escalation.High:    catalog.ModelIDClaudeOpus47,
		escalation.Maximum: catalog.ModelIDClaudeOpus48,
	}
	for _, test := range []struct {
		name        string
		pinned      escalation.Group
		classified  escalation.Group
		probability float64
		disabled    bool
		forced      escalation.Group
		wantGroup   escalation.Group
		wantSticky  bool
	}{
		{name: "low to high without a medium detour", pinned: escalation.Low, classified: escalation.High, probability: 0.38, wantGroup: escalation.High},
		{name: "low to maximum without a high detour", pinned: escalation.Low, classified: escalation.Maximum, probability: 0.38, wantGroup: escalation.Maximum},
		{name: "medium to maximum without a high detour", pinned: escalation.Medium, classified: escalation.Maximum, probability: 0.38, wantGroup: escalation.Maximum},
		{name: "medium to high clears its own threshold", pinned: escalation.Medium, classified: escalation.High, probability: 0.45, wantGroup: escalation.High},
		{name: "medium to high below threshold keeps pin", pinned: escalation.Medium, classified: escalation.High, probability: 0.38, wantGroup: escalation.Medium, wantSticky: true},
		{name: "disabled sliding threshold preserves confidence gate", pinned: escalation.Low, classified: escalation.High, probability: 0.38, disabled: true, wantGroup: escalation.Low, wantSticky: true},
		{name: "explicit cluster overrides prediction", pinned: escalation.Low, classified: escalation.High, probability: 0.8, forced: escalation.Medium, wantGroup: escalation.Medium},
	} {
		t.Run(test.name, func(t *testing.T) {
			deployed := make(map[string]struct{}, len(models))
			roster := &rosterdata.Roster{SchemaVersion: rosterdata.SchemaVersionV6, Clusters: make(map[string]rosterdata.Cluster)}
			order := []string{string(escalation.Low), string(escalation.Medium), string(escalation.High), string(escalation.Maximum)}
			probabilities := make(map[string]float64, len(order))
			for group, model := range models {
				deployed[model.String()] = struct{}{}
				roster.Clusters[string(group)] = rosterdata.Cluster{Arms: []string{providers.ProviderAnthropic + "/" + model.String()}}
				probabilities[string(group)] = (1 - test.probability) / 3
			}
			probabilities[string(test.classified)] = test.probability
			classifier := slidingTestClassifier{classification: policy.Result{
				SchemaVersion: policy.SchemaVersionV4, Score: test.probability,
				PredictedLabel: string(test.classified), ClassOrder: order, ClassProbabilities: probabilities,
			}}
			capabilities := policy.Capabilities{SchemaVersion: policy.SchemaVersionV4, AuthoritativePerTurnSelection: true}
			adapter := policy.NewSidecarRouter(policy.SidecarRouterConfig{Strategy: router.StrategyHMM}, classifier,
				policy.NewResolver(deployed, map[string]struct{}{providers.ProviderAnthropic: {}},
					func(model catalog.Model) string { return providers.ProviderAnthropic + "/" + model.ID }, policy.ManagedProviderPolicy())).
				WithArmSelector(selection.Selector(roster)).WithCapabilities(capabilities)
			store := newStubPinStore()
			store.getFound = true
			store.getPin = sessionpin.Pin{
				Model: models[test.pinned].String(), Provider: providers.ProviderAnthropic,
				PolicyGroup: string(test.pinned), Reason: "hmm_policy",
				PinnedUntil: time.Now().Add(time.Hour),
			}
			svc := NewService(nil, nil, nil, false, nil, store, false, providers.ProviderAnthropic, models[escalation.Low].String(), nil).
				WithAuthoritativeUpgradeGate(true).WithAuthoritativeUpgradeSlidingThreshold(!test.disabled).
				WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMM, Router: adapter, Capabilities: capabilities})
			ctx := router.WithStrategy(context.Background(), router.StrategyHMM)
			installation := uuid.New()
			bodies := []string{
				`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"Fix the synthetic parser regression."}]}`,
				`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"Fix the synthetic parser regression."},{"role":"assistant","content":[{"type":"tool_use","id":"call-test","name":"read_file","input":{"path":"parser.go"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-test","content":"synthetic parser source"}]}]}`,
			}
			for index, body := range bodies {
				env, err := translate.ParseAnthropic([]byte(body))
				require.NoError(t, err)
				features := env.RoutingFeatures(false)
				turn, err := svc.runTurnLoop(ctx, env, features, "synthetic-key", installation, "", http.Header{}, router.Request{
					RequestedModel: features.Model, ConversationMessages: conversationMessagesForRouting(env), ForceCluster: string(test.forced),
				})
				require.NoError(t, err)
				assert.Equal(t, models[test.wantGroup].String(), turn.Decision.Model, "turn %d", index)
				if index == 0 {
					assert.Equal(t, test.wantSticky, turn.StickyHit)
				}
				require.Len(t, store.upserts, index+1)
				assert.Equal(t, models[test.wantGroup].String(), store.upserts[index].Model)
				assert.Equal(t, string(test.wantGroup), store.upserts[index].PolicyGroup)
				store.getPin = store.upserts[index]
			}
		})
	}
}
