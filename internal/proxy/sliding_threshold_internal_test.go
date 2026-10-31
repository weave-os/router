package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/observability"
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

func TestSlidingUpgradeExplanationsPreserveRouting(t *testing.T) {
	models := map[escalation.Group]catalog.ModelID{
		escalation.Low:     catalog.ModelIDClaudeHaiku45,
		escalation.Medium:  catalog.ModelIDClaudeSonnet46,
		escalation.High:    catalog.ModelIDClaudeOpus47,
		escalation.Maximum: catalog.ModelIDClaudeOpus48,
	}
	for _, test := range []struct {
		name                string
		pinned              escalation.Group
		classified          escalation.Group
		probability         float64
		disabled            bool
		forced              escalation.Group
		wantGroup           escalation.Group
		wantSticky          bool
		wantExplanation     string
		wantNextExplanation string
	}{
		{name: "high prediction steps to medium", pinned: escalation.Low, classified: escalation.High, probability: 0.38, wantGroup: escalation.Medium, wantExplanation: "high difficulty predicted (38.0% confidence); gradual upgrade from low to medium (50% needed for high)", wantNextExplanation: "high difficulty predicted (38.0% confidence); kept medium model (40% needed for sliding upgrade; 50% confidence gate)"},
		{name: "maximum prediction steps from low to high", pinned: escalation.Low, classified: escalation.Maximum, probability: 0.38, wantGroup: escalation.High, wantExplanation: "maximum difficulty predicted (38.0% confidence); gradual upgrade from low to high (50% needed for maximum)"},
		{name: "maximum prediction steps from medium to high", pinned: escalation.Medium, classified: escalation.Maximum, probability: 0.38, wantGroup: escalation.High, wantExplanation: "maximum difficulty predicted (38.0% confidence); gradual upgrade from medium to high (50% needed for maximum)"},
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
			var logs bytes.Buffer
			ctx := router.WithStrategy(observability.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&logs, nil))), router.StrategyHMM)
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
				if index == 0 && test.wantExplanation != "" {
					assert.Equal(t, test.wantExplanation, routingReasonShort(turn))
					marker := routingMarkerFor(turn)
					assert.Contains(t, marker, test.wantExplanation)
					reasoningCtx := context.WithValue(ctx, InstallationShowModelSelectionReasoningContextKey{}, true)
					assert.Equal(t, marker, modelSelectionMarkerForRequest(reasoningCtx, turn, marker, turn.Decision.Model, ""))
					rescueMarker := modelSelectionMarkerForRequest(reasoningCtx, turn, siblingRoutingMarkerFor(turn, markerReasonSibling, "rescue-model"), "rescue-model", markerReasonSibling)
					assert.NotContains(t, rescueMarker, "gradual upgrade")
					assert.Contains(t, rescueMarker, markerReasonSibling)
					var adjustment map[string]any
					decoder := json.NewDecoder(&logs)
					for decoder.More() {
						var event map[string]any
						require.NoError(t, decoder.Decode(&event))
						if event["msg"] == "routing adjustment" {
							adjustment = event
						}
					}
					require.NotNil(t, adjustment)
					assert.Equal(t, string(test.classified), adjustment["classified_group"])
					assert.Equal(t, string(test.wantGroup), adjustment["routing_selected_group"])
					assert.Equal(t, test.wantExplanation, adjustment["routing_explanation"])
					assert.Equal(t, 0.3, adjustment["sliding_step_threshold"])
					assert.Equal(t, 0.5, adjustment["sliding_full_threshold"])
					assert.Equal(t, test.probability, adjustment["classification_probability"])
					completionFields := plannerLogFields(turn)
					assert.Contains(t, completionFields, "routing_explanation")
					assert.Contains(t, completionFields, test.wantExplanation)
				}
				if index == 1 && test.wantNextExplanation != "" {
					assert.Equal(t, test.wantNextExplanation, routingReasonShort(turn))
				}
				store.getPin = store.upserts[index]
			}
		})
	}
}
