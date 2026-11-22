package hmm_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
)

type historyRowKey struct {
	sessionKey [sessionpin.SessionKeyLen]byte
	role       string
}

type historyPinStore struct {
	sessionpin.Store
	pins  map[historyRowKey]sessionpin.Pin
	usage []sessionpin.Usage
	roles []string
}

func newHistoryPinStore() *historyPinStore {
	return &historyPinStore{pins: map[historyRowKey]sessionpin.Pin{}}
}

func (s *historyPinStore) Get(_ context.Context, key [sessionpin.SessionKeyLen]byte, role string) (sessionpin.Pin, bool, error) {
	p, ok := s.pins[historyRowKey{key, role}]
	return p, ok, nil
}

func (s *historyPinStore) Upsert(_ context.Context, p sessionpin.Pin) error {
	s.pins[historyRowKey{p.SessionKey, p.Role}] = p
	return nil
}

func (s *historyPinStore) UpdateUsage(_ context.Context, _ [sessionpin.SessionKeyLen]byte, role string, usage sessionpin.Usage) error {
	s.roles = append(s.roles, role)
	s.usage = append(s.usage, usage)
	return nil
}

func historyRows(store sessionpin.Store, now time.Time) continuity.HistoryRows {
	return continuity.HistoryRows{Store: store, Now: func() time.Time { return now }, Strategy: "hmm"}
}

func TestHistoryRoleDefaultsTheBaseRole(t *testing.T) {
	assert.Equal(t, sessionpin.DefaultRole+"_hmm_history", hmm.HistoryRole(""))
	assert.Equal(t, "main_hmm_history", hmm.HistoryRole("main"))
}

func TestLoadHistorySurvivesPinExpiry(t *testing.T) {
	store := newHistoryPinStore()
	key := [sessionpin.SessionKeyLen]byte{6}
	store.pins[historyRowKey{key, hmm.HistoryRole("")}] = sessionpin.Pin{Model: "prior", Strategy: "hmm", PinnedUntil: time.Now().Add(-time.Hour)}
	rows := continuity.HistoryRows{Store: store, Now: time.Now, Strategy: router.Strategy("hmm")}

	assert.Equal(t, "prior", hmm.LoadHistory(context.Background(), rows, key, "").Model, "history survives pin expiry")
	assert.Empty(t, hmm.LoadHistory(context.Background(), rows, key, "other").Model)
}

func TestRecordTurnHistoryWritesHistoryAndUsage(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	store := newHistoryPinStore()
	key := [sessionpin.SessionKeyLen]byte{7}

	role, recorded := hmm.RecordTurnHistory(context.Background(), historyRows(store, now), continuity.TurnHistoryRecord{
		SessionKey: key, InstallationID: uuid.New(), PinRole: sessionpin.DefaultRole,
		Fresh: router.Decision{Reason: "hmm_policy(group=cluster)"},
		Usage: sessionpin.Usage{InputTokens: 10, ServedModel: "model-a", ServedProvider: "anthropic"},
	})

	require.True(t, recorded)
	assert.Equal(t, hmm.HistoryRole(sessionpin.DefaultRole), role)
	history, found, err := store.Get(context.Background(), key, role)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "anthropic", history.Provider)
	assert.Equal(t, "hmm_policy(group=cluster)", history.Reason)
	assert.Equal(t, routingpolicy.PinExpiry(hmm.HistoryReason, now), history.PinnedUntil)
	require.Len(t, store.usage, 1)
	assert.Equal(t, now, store.usage[0].EndedAt)
	assert.EqualValues(t, "hmm", store.usage[0].Strategy)
}

func TestRecordTurnHistoryWithoutUsageKeepsThePriorProvider(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	store := newHistoryPinStore()
	key := [sessionpin.SessionKeyLen]byte{8}
	role := hmm.HistoryRole(sessionpin.DefaultRole)
	require.NoError(t, store.Upsert(context.Background(), sessionpin.Pin{SessionKey: key, Role: role, Provider: "openai", Strategy: "hmm", PinnedUntil: now.Add(time.Hour)}))

	_, recorded := hmm.RecordTurnHistory(context.Background(), historyRows(store, now), continuity.TurnHistoryRecord{
		SessionKey: key, InstallationID: uuid.New(), PinRole: sessionpin.DefaultRole,
		Usage: sessionpin.Usage{ServedModel: "model-a", ServedProvider: "anthropic"},
	})

	assert.False(t, recorded)
	assert.Empty(t, store.usage)
	history, _, err := store.Get(context.Background(), key, role)
	require.NoError(t, err)
	assert.Equal(t, "openai", history.Provider)
	assert.Equal(t, hmm.HistoryReason, history.Reason)
}

func TestRecordTurnHistoryStoresTheStrategyDecisionReason(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	hmmDecision := router.Decision{Reason: "routed", Metadata: &router.RoutingMetadata{Strategy: string(router.StrategyHMM)}}
	for name, tc := range map[string]struct {
		fresh, decision router.Decision
		want            string
	}{
		"fresh wins":      {fresh: hmmDecision, decision: router.Decision{Reason: "hmm_policy(group=x)"}, want: "routed"},
		"served decision": {fresh: router.Decision{Reason: "other"}, decision: router.Decision{Reason: "hmm_policy(group=x)"}, want: "hmm_policy(group=x)"},
		"neither is hmm":  {fresh: router.Decision{Reason: "other"}, decision: router.Decision{Reason: "planner_stay"}, want: hmm.HistoryReason},
	} {
		t.Run(name, func(t *testing.T) {
			store := newHistoryPinStore()
			key := [sessionpin.SessionKeyLen]byte{9}
			role, _ := hmm.RecordTurnHistory(context.Background(), historyRows(store, now), continuity.TurnHistoryRecord{
				SessionKey: key, InstallationID: uuid.New(), Fresh: tc.fresh, Decision: tc.decision,
				Usage: sessionpin.Usage{OutputTokens: 1, ServedProvider: "anthropic"},
			})
			assert.Equal(t, tc.want, store.pins[historyRowKey{key, role}].Reason)
		})
	}
}
