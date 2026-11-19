package proxy

import (
	"context"
	"net/http"
	"testing"
	"time"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeStruggleStore records inserts and answers the durable budget query.
type fakeStruggleStore struct {
	events    []conversation.StruggleShadowEvent
	counts    map[string]int64
	countErr  error
	insertErr error
	// countStays0 keeps the durable counter at zero after an insert, isolating
	// the replica-local cache as the only thing that can suppress a repeat.
	countStays0 bool
	// countCalls counts durable budget queries, so a test can assert the
	// per-turn cost of a long session.
	countCalls int
}

func newFakeStruggleStore() *fakeStruggleStore {
	return &fakeStruggleStore{counts: map[string]int64{}}
}

func (f *fakeStruggleStore) InsertStruggleShadowEvent(_ context.Context, p conversation.StruggleShadowEvent) error {
	if f.insertErr != nil {
		return f.insertErr
	}
	f.events = append(f.events, p)
	if !f.countStays0 {
		f.counts[p.Role+"\x00"+p.Reason]++
	}
	return nil
}

func (f *fakeStruggleStore) CountStruggleShadowEvents(_ context.Context, _ []byte, role, reason string) (int64, error) {
	f.countCalls++
	if f.countErr != nil {
		return 0, f.countErr
	}
	return f.counts[role+"\x00"+reason], nil
}

func newStruggleTestService(store conversation.StruggleShadowStore) *Service {
	return &Service{
		struggleShadowEnabled: true,
		struggleTracker:       conversation.NewStruggleTracker(),
		struggleShadowStore:   store,
	}
}

func fireStruggle(s *Service, reason string, installationID uuid.UUID, key [sessionpin.SessionKeyLen]byte) {
	s.handleStruggleShadow(context.Background(), reason, installationID, key, "default_high",
		"claude-sonnet-5", "tool_result", 95, 42*time.Minute, true, 31522)
}

func TestHandleStruggleShadow_RecordsEventWithSnapshot(t *testing.T) {
	store := newFakeStruggleStore()
	svc := newStruggleTestService(store)
	installationID := uuid.New()
	key := [sessionpin.SessionKeyLen]byte{1, 2, 3}

	fireStruggle(svc, conversation.StruggleReasonLate, installationID, key)

	if len(store.events) != 1 {
		t.Fatalf("events = %d, want 1", len(store.events))
	}
	got := store.events[0]
	if got.Reason != conversation.StruggleReasonLate {
		t.Errorf("reason = %q, want %q", got.Reason, conversation.StruggleReasonLate)
	}
	if got.TurnCount != 95 {
		t.Errorf("turn_count = %d, want 95", got.TurnCount)
	}
	if got.WallSeconds != int64((42 * time.Minute).Seconds()) {
		t.Errorf("wall_seconds = %d, want %d", got.WallSeconds, int64((42 * time.Minute).Seconds()))
	}
	if !got.SessionEverSwitched {
		t.Error("session_ever_switched = false, want true")
	}
	if got.EstInputTokens != 31522 {
		t.Errorf("est_input_tokens = %d, want 31522", got.EstInputTokens)
	}
	if got.InstallationID != installationID.String() {
		t.Errorf("installation_id = %q, want %q", got.InstallationID, installationID)
	}
}

func TestHandleStruggleShadow_OncePerSessionRoleReason(t *testing.T) {
	store := newFakeStruggleStore()
	svc := newStruggleTestService(store)
	installationID := uuid.New()
	key := [sessionpin.SessionKeyLen]byte{9}

	for i := 0; i < 5; i++ {
		fireStruggle(svc, conversation.StruggleReasonLate, installationID, key)
	}

	if len(store.events) != 1 {
		t.Fatalf("events = %d, want 1 (repeat turns must not re-record)", len(store.events))
	}
}

func TestHandleStruggleShadow_ReplicaCacheAvoidsBudgetQueryPerTurn(t *testing.T) {
	// The durable budget query must not run on every turn of a long session:
	// once this replica has fired, the in-proc cache short-circuits it. Asserted
	// with a store whose counter stays 0, so only the LRU can suppress.
	store := newFakeStruggleStore()
	store.countStays0 = true
	svc := newStruggleTestService(store)
	installationID := uuid.New()
	key := [sessionpin.SessionKeyLen]byte{11}

	for i := 0; i < 4; i++ {
		fireStruggle(svc, conversation.StruggleReasonLate, installationID, key)
	}

	if len(store.events) != 1 {
		t.Fatalf("events = %d, want 1 (replica-local cache must suppress repeats)", len(store.events))
	}
	if store.countCalls != 1 {
		t.Fatalf("budget queries = %d, want 1 (cache must short-circuit later turns)", store.countCalls)
	}
}

func TestHandleStruggleShadow_EarlyAndLateRecordSeparately(t *testing.T) {
	store := newFakeStruggleStore()
	svc := newStruggleTestService(store)
	installationID := uuid.New()
	key := [sessionpin.SessionKeyLen]byte{7}

	fireStruggle(svc, conversation.StruggleReasonEarly, installationID, key)
	fireStruggle(svc, conversation.StruggleReasonLate, installationID, key)

	if len(store.events) != 2 {
		t.Fatalf("events = %d, want 2 (operating points are independent budgets)", len(store.events))
	}
}

func TestHandleStruggleShadow_DurableBudgetSuppressesAcrossReplicas(t *testing.T) {
	store := newFakeStruggleStore()
	// A sibling replica already recorded this session's late event.
	store.counts["default_high\x00"+conversation.StruggleReasonLate] = 1
	svc := newStruggleTestService(store)

	fireStruggle(svc, conversation.StruggleReasonLate, uuid.New(), [sessionpin.SessionKeyLen]byte{4})

	if len(store.events) != 0 {
		t.Fatalf("events = %d, want 0 (durable budget must suppress)", len(store.events))
	}
}

func TestHandleStruggleShadow_InsertFailureRetriesNextTurn(t *testing.T) {
	store := newFakeStruggleStore()
	store.insertErr = context.DeadlineExceeded
	svc := newStruggleTestService(store)
	installationID := uuid.New()
	key := [sessionpin.SessionKeyLen]byte{5}

	fireStruggle(svc, conversation.StruggleReasonLate, installationID, key)
	// The failed insert must not latch the replica-local cache, so a later turn
	// gets another chance once the store recovers.
	store.insertErr = nil
	fireStruggle(svc, conversation.StruggleReasonLate, installationID, key)

	if len(store.events) != 1 {
		t.Fatalf("events = %d, want 1 (retry after a failed insert)", len(store.events))
	}
}

func TestHandleStruggleShadow_BudgetLookupErrorStillRecords(t *testing.T) {
	store := newFakeStruggleStore()
	store.countErr = context.DeadlineExceeded
	svc := newStruggleTestService(store)

	fireStruggle(svc, conversation.StruggleReasonLate, uuid.New(), [sessionpin.SessionKeyLen]byte{6})

	// Fail-open: in shadow mode an extra row beats a lost one.
	if len(store.events) != 1 {
		t.Fatalf("events = %d, want 1 (budget lookup failure must not drop the event)", len(store.events))
	}
}

func TestHandleStruggleShadow_NilStoreLogsWithoutPanic(t *testing.T) {
	svc := newStruggleTestService(nil)
	// Nil store degrades to log-only; the replica-local cache still de-dupes.
	fireStruggle(svc, conversation.StruggleReasonLate, uuid.New(), [sessionpin.SessionKeyLen]byte{8})
}

func TestHandleStruggleShadow_NilInstallationSkipsStore(t *testing.T) {
	store := newFakeStruggleStore()
	svc := newStruggleTestService(store)

	fireStruggle(svc, conversation.StruggleReasonLate, uuid.Nil, [sessionpin.SessionKeyLen]byte{10})

	// A row without a valid installation would violate the FK.
	if len(store.events) != 0 {
		t.Fatalf("events = %d, want 0 (nil installation must not write)", len(store.events))
	}
}

// Regression (cursor bugbot): PinTurnCount was copied from the pin before this
// turn's upsert incremented turn_count, so the stored count lagged by one and
// the operating points fired on turns 31/81 instead of ON turns 30/80 as Phase
// 0 mined them. The stamped count is completed turns + this in-flight turn.
func TestRunTurnLoop_PinTurnCountsCurrentTurnInclusively(t *testing.T) {
	fr := &tierProbeRouter{available: map[string]struct{}{"claude-sonnet-4-6": {}}}
	store := newStubPinStore()
	store.getFound = true
	store.getPin = sessionpin.Pin{
		Provider:        providers.ProviderAnthropic,
		Model:           "claude-sonnet-4-6",
		Reason:          "fake",
		PinnedUntil:     time.Now().Add(time.Hour),
		TurnCount:       conversation.StruggleEarlyTurns - 1,
		FirstPinnedAt:   time.Now().Add(-conversation.StruggleEarlyWall - time.Minute),
		LastServedModel: "claude-sonnet-4-6",
	}
	svc := NewService(fr, nil, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithAvailableModels(fr.available).
		EditContinuityConfig(func(c *continuity.Config) { c.PlannerEnabled = false })

	env, err := translate.ParseAnthropic(
		[]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"continue"}]}`),
	)
	require.NoError(t, err)
	feats := env.RoutingFeatures()

	res, err := svc.runTurnLoop(context.Background(), env, feats, "key-struggle", uuid.New(), "", http.Header{}, router.Request{
		RequestedModel: feats.Model,
	})
	require.NoError(t, err)

	assert.Equal(t, conversation.StruggleEarlyTurns, res.PinTurnCount,
		"the in-flight turn counts inclusively: stored %d becomes %d, not %d",
		conversation.StruggleEarlyTurns-1, conversation.StruggleEarlyTurns, conversation.StruggleEarlyTurns-1)
	assert.False(t, res.PinFirstPinnedAt.IsZero())
}
