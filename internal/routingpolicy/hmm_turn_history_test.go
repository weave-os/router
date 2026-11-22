package routingpolicy_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
)

type usagePinStore struct {
	*memPinStore
	usage []sessionpin.Usage
	roles []string
}

func (u *usagePinStore) UpdateUsage(_ context.Context, _ [sessionpin.SessionKeyLen]byte, role string, usage sessionpin.Usage) error {
	u.roles = append(u.roles, role)
	u.usage = append(u.usage, usage)
	return nil
}

func TestRecordHMMTurnWritesHistoryAndUsage(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	store := &usagePinStore{memPinStore: newMemPinStore()}
	key := [sessionpin.SessionKeyLen]byte{7}

	role, recorded := routingpolicy.NewSessionPins(store, func() time.Time { return now }, "hmm").RecordHMMTurn(context.Background(), routingpolicy.HMMTurnRecord{
		SessionKey: key, InstallationID: uuid.New(), PinRole: sessionpin.DefaultRole, Reason: "hmm-cluster", HistoryReason: "hmm-history",
		Usage: sessionpin.Usage{InputTokens: 10, ServedModel: "model-a", ServedProvider: "anthropic"},
	})

	require.True(t, recorded)
	assert.Equal(t, routingpolicy.HMMHistoryRole(sessionpin.DefaultRole), role)
	history, found, err := store.Get(context.Background(), key, role)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "anthropic", history.Provider)
	assert.Equal(t, "hmm-cluster", history.Reason)
	assert.Equal(t, routingpolicy.PinExpiry("hmm-history", now), history.PinnedUntil)
	require.Len(t, store.usage, 1)
	assert.Equal(t, now, store.usage[0].EndedAt)
	assert.EqualValues(t, "hmm", store.usage[0].Strategy)
}

func TestRecordHMMTurnWithoutUsageKeepsThePriorProvider(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	store := &usagePinStore{memPinStore: newMemPinStore()}
	key := [sessionpin.SessionKeyLen]byte{8}
	role := routingpolicy.HMMHistoryRole(sessionpin.DefaultRole)
	require.NoError(t, store.Upsert(context.Background(), sessionpin.Pin{SessionKey: key, Role: role, Provider: "openai", Strategy: "hmm", PinnedUntil: now.Add(time.Hour)}))

	_, recorded := routingpolicy.NewSessionPins(store, func() time.Time { return now }, "hmm").RecordHMMTurn(context.Background(), routingpolicy.HMMTurnRecord{
		SessionKey: key, InstallationID: uuid.New(), PinRole: sessionpin.DefaultRole,
		Usage: sessionpin.Usage{ServedModel: "model-a", ServedProvider: "anthropic"},
	})

	assert.False(t, recorded)
	assert.Empty(t, store.usage)
	history, _, err := store.Get(context.Background(), key, role)
	require.NoError(t, err)
	assert.Equal(t, "openai", history.Provider)
}
