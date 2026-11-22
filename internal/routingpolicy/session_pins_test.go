package routingpolicy_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/routingpolicy"
	"weave-os/router/internal/translate"
)

type pinKey struct {
	session [sessionpin.SessionKeyLen]byte
	role    string
}

// memPinStore implements the Get/Upsert half of sessionpin.Store; any other
// method panics through the nil embedded interface.
type memPinStore struct {
	sessionpin.Store
	pins   map[pinKey]sessionpin.Pin
	getErr error
}

func newMemPinStore() *memPinStore { return &memPinStore{pins: map[pinKey]sessionpin.Pin{}} }

func (m *memPinStore) Get(_ context.Context, key [sessionpin.SessionKeyLen]byte, role string) (sessionpin.Pin, bool, error) {
	if m.getErr != nil {
		return sessionpin.Pin{}, false, m.getErr
	}
	p, ok := m.pins[pinKey{key, role}]
	return p, ok, nil
}

func (m *memPinStore) Upsert(_ context.Context, p sessionpin.Pin) error {
	m.pins[pinKey{p.SessionKey, p.Role}] = p
	return nil
}

var sessionKey = [sessionpin.SessionKeyLen]byte{1, 2, 3}

func strategyCtx() context.Context {
	return router.WithStrategy(context.Background(), router.Strategy("hmm"))
}

func TestSessionPinsLoadWithStoreState(t *testing.T) {
	ctx := strategyCtx()
	store := newMemPinStore()
	pins := routingpolicy.NewSessionPins(store, time.Now, router.StrategyFromContext(ctx))

	_, active, noStoredState := pins.LoadWithStoreState(ctx, sessionKey, "main")
	assert.False(t, active)
	assert.True(t, noStoredState, "an empty store is a fresh session")

	store.getErr = errors.New("store down")
	_, active, noStoredState = pins.LoadWithStoreState(ctx, sessionKey, "main")
	assert.False(t, active)
	assert.False(t, noStoredState, "an outage must not read as a fresh session")
	store.getErr = nil

	store.pins[pinKey{sessionKey, "main"}] = sessionpin.Pin{Model: "m", Strategy: "hmm", PinnedUntil: time.Now().Add(-time.Minute)}
	pin, active := pins.Load(ctx, sessionKey, "main")
	assert.False(t, active, "an expired pin is a routing miss")
	assert.Equal(t, "m", pin.Model, "an expired pin still returns its history")

	store.pins[pinKey{sessionKey, "main"}] = sessionpin.Pin{Model: "m", Strategy: "other", PinnedUntil: time.Now().Add(time.Hour)}
	_, active = pins.Load(ctx, sessionKey, "main")
	assert.False(t, active, "a pin written by another strategy is ignored")

	store.pins[pinKey{sessionKey, "main"}] = sessionpin.Pin{Model: "m", Strategy: "hmm", PinnedUntil: time.Now().Add(time.Hour)}
	_, active = pins.Load(ctx, sessionKey, "main")
	assert.True(t, active)
}

func TestSessionPinsWriteNewAndRefresh(t *testing.T) {
	ctx := strategyCtx()
	store := newMemPinStore()
	pins := routingpolicy.NewSessionPins(store, time.Now, router.StrategyFromContext(ctx))
	installation := uuid.New()

	pins.WriteNew(ctx, uuid.Nil, sessionKey, "main", router.Decision{Model: "m"})
	assert.Empty(t, store.pins, "pins are never written without an installation")

	pins.WriteNew(ctx, installation, sessionKey, "main", router.Decision{
		Provider: "p", Model: "m", Effort: "high", Reason: "scored",
		Metadata: &router.RoutingMetadata{PairedModel: "pm", PairedProvider: "pp", PolicyGroup: "g"},
	})
	written := store.pins[pinKey{sessionKey, "main"}]
	assert.Equal(t, "m", written.Model)
	assert.Equal(t, "pm", written.PairedModel)
	assert.Equal(t, "g", written.PolicyGroup)
	assert.Equal(t, router.Strategy("hmm"), written.Strategy)
	assert.WithinDuration(t, time.Now().Add(routingpolicy.PinSessionTTL), written.PinnedUntil, time.Minute)

	written.LastInputTokens = 900
	pins.RefreshVotes(ctx, installation, sessionKey, written, "main", router.Decision{Provider: "p", Model: "m"}, 1, 2)
	refreshed := store.pins[pinKey{sessionKey, "main"}]
	assert.Equal(t, "high", refreshed.Effort, "refreshing the same model keeps its effort")
	assert.Equal(t, "pm", refreshed.PairedModel, "a refresh carries the scored pair forward")
	assert.Equal(t, "g", refreshed.PolicyGroup)
	assert.Equal(t, 900, refreshed.LastInputTokens)
	assert.Equal(t, 1, refreshed.ConsecutiveDowngradeVotes)
	assert.Equal(t, 2, refreshed.ConsecutiveUpgradeVotes)
}

func TestSessionPinsForcedPinsNeverExpire(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	assert.Equal(t, routingpolicy.PinNeverExpires, routingpolicy.PinExpiry(translate.ReasonUserForceModel, now))
	assert.Equal(t, now.Add(routingpolicy.PinSessionTTL), routingpolicy.PinExpiry("cluster", now))

	store := newMemPinStore()
	pins := routingpolicy.NewSessionPins(store, time.Now, router.StrategyFromContext(strategyCtx()))
	pins.AnchorForceModelHistory(strategyCtx(), uuid.New(), sessionKey, "", sessionpin.Pin{Provider: "p", Model: "forced"})
	history, ok := store.pins[pinKey{sessionKey, routingpolicy.ForceModelHistoryRole("")}]
	require.True(t, ok)
	assert.Equal(t, "forced", history.Model)
	assert.Equal(t, routingpolicy.PinNeverExpires, history.PinnedUntil)
}

func TestSessionPinsWriteRepinStoresAFreshPinForTheTarget(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	store := newMemPinStore()
	key := [sessionpin.SessionKeyLen]byte{9}
	installationID := uuid.New()

	require.NoError(t, routingpolicy.NewSessionPins(store, func() time.Time { return now }, "hmm").
		WriteRepin(context.Background(), installationID, key, "main_loop", "openai", "gpt-x", "cyber-refusal-repin"))

	pin, found, err := store.Get(context.Background(), key, "main_loop")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, sessionpin.Pin{
		SessionKey: key, Role: "main_loop", InstallationID: installationID,
		Provider: "openai", Model: "gpt-x", Reason: "cyber-refusal-repin",
		Strategy: "hmm", TurnCount: 1, PinnedUntil: routingpolicy.PinExpiry("cyber-refusal-repin", now),
	}, pin)
}
