package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// demotionCall is one recorded ExpireAndDemoteModel invocation.
type demotionCall struct {
	role   string
	model  string
	reason sessionpin.DemotionReason
}

type demotionStubPinStore struct {
	mu        sync.Mutex
	demotions []demotionCall
	// expired records the marker row each demotion wrote, in call order.
	expired []sessionpin.Pin
	// upserts records plain Upsert calls, which the demotion path must not make.
	upserts []sessionpin.Pin
}

func (s *demotionStubPinStore) Get(context.Context, [sessionpin.SessionKeyLen]byte, string) (sessionpin.Pin, bool, error) {
	return sessionpin.Pin{}, false, nil
}

func (s *demotionStubPinStore) Upsert(_ context.Context, p sessionpin.Pin) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upserts = append(s.upserts, p)
	return nil
}

func (s *demotionStubPinStore) UpdateUsage(context.Context, [sessionpin.SessionKeyLen]byte, string, sessionpin.Usage) error {
	return nil
}

func (s *demotionStubPinStore) IncrementUpstreamErrors(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) (int, error) {
	return 0, nil
}

func (s *demotionStubPinStore) ResetUpstreamErrors(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) error {
	return nil
}

func (s *demotionStubPinStore) IncrementOverloadErrors(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) (int, error) {
	return 0, nil
}

func (s *demotionStubPinStore) ResetOverloadErrors(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) error {
	return nil
}

func (s *demotionStubPinStore) DisableProvider(context.Context, [sessionpin.SessionKeyLen]byte, string, string, router.Strategy) error {
	return nil
}

func (s *demotionStubPinStore) ExpireAndDemoteModel(_ context.Context, expired sessionpin.Pin, model string, reason sessionpin.DemotionReason) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.demotions = append(s.demotions, demotionCall{role: expired.Role, model: model, reason: reason})
	s.expired = append(s.expired, expired)
	return nil
}

func (s *demotionStubPinStore) Consume(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) (sessionpin.Pin, bool, error) {
	return sessionpin.Pin{}, false, nil
}

func (s *demotionStubPinStore) SweepExpired(context.Context) error { return nil }

func newDemotionTestService(store sessionpin.Store, flagOn bool) *Service {
	return NewService(
		nil,
		nil,
		nil,
		false,
		nil,
		store,
		false,
		"anthropic", "claude-haiku-4-5",
		nil,
	).WithCommittedStreamArmDemotion(flagOn)
}

const demotedArm = "claude-opus-4-7"

// Classification of the committed-stream failure path: only an upstream-owned
// end of an already-committed stream may strike the arm out for the session.
func TestMaybeDemoteArmAfterCommittedStreamFailure_Classification(t *testing.T) {
	cases := []struct {
		name      string
		committed bool
		flagOn    bool
		err       error
		want      bool
	}{
		{name: "committed 502 stream failure", committed: true, flagOn: true, err: &providers.UpstreamStatusError{Status: http.StatusBadGateway}, want: true},
		{name: "committed buffered 500", committed: true, flagOn: true, err: &providers.UpstreamErrorResponse{Status: http.StatusInternalServerError}, want: true},
		{name: "committed status-0 transport cut", committed: true, flagOn: true, err: errors.New("upstream call: unexpected EOF"), want: true},
		{name: "committed idle watchdog", committed: true, flagOn: true, err: fmt.Errorf("stream: %w", providers.ErrUpstreamIdleTimeout), want: true},
		{name: "committed output stall", committed: true, flagOn: true, err: fmt.Errorf("stream: %w", providers.ErrUpstreamOutputStall), want: true},
		{name: "uncommitted 502", committed: false, flagOn: true, err: &providers.UpstreamStatusError{Status: http.StatusBadGateway}, want: false},
		{name: "committed success", committed: true, flagOn: true, err: nil, want: false},
		{name: "client cancellation", committed: true, flagOn: true, err: fmt.Errorf("copy body: %w", context.Canceled), want: false},
		{name: "client deadline", committed: true, flagOn: true, err: fmt.Errorf("copy body: %w", context.DeadlineExceeded), want: false},
		{name: "provider overloaded 529", committed: true, flagOn: true, err: &providers.UpstreamErrorResponse{Status: providerOverloadedStatus}, want: false},
		{name: "committed 4xx", committed: true, flagOn: true, err: &providers.UpstreamErrorResponse{Status: http.StatusBadRequest}, want: false},
		{name: "flag off", committed: true, flagOn: false, err: &providers.UpstreamStatusError{Status: http.StatusBadGateway}, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &demotionStubPinStore{}
			svc := newDemotionTestService(store, tc.flagOn)
			installationID := uuid.New()
			ctx := router.WithStrategy(context.Background(), router.StrategyHMM)

			demoted := svc.maybeDemoteArmAfterCommittedStreamFailure(
				ctx,
				tc.committed,
				false,
				tc.err,
				demotedArm,
				"cluster:v0.57 model=claude-opus-4-7 provider=anthropic",
				installationID,
				nonZeroSessionKey(),
				sessionpin.DefaultRole, sessionpin.DefaultRole,
			)

			if !tc.want {
				assert.Empty(t, demoted)
				assert.Empty(t, store.demotions, "arm must stay eligible")
				assert.Empty(t, store.upserts, "pin must not be evicted")
				return
			}

			assert.Equal(t, demotedArm, demoted)
			assert.Equal(t, []demotionCall{
				{role: sessionpin.DefaultRole, model: demotedArm, reason: sessionpin.DemotionReasonCommittedStreamFailure},
				{role: hmmHistoryRole(sessionpin.DefaultRole), model: demotedArm, reason: sessionpin.DemotionReasonCommittedStreamFailure},
			}, store.demotions, "the strike must land on both rows the next turn merges")
			assert.Empty(t, store.upserts, "expiry must ride the guarded demotion write, not a plain upsert")
			require.Len(t, store.expired, 2, "demotion must expire the pin and its HMM history row")
			expired := store.expired[0]
			assert.Equal(t, sessionpin.DefaultRole, expired.Role)
			assert.Equal(t, installationID, expired.InstallationID)
			assert.Equal(t, router.StrategyFromContext(ctx), expired.Strategy, "the write must be guarded by this request's strategy")
			assert.Equal(t, hmmHistoryRole(sessionpin.DefaultRole), store.expired[1].Role)
			assert.Empty(t, expired.Model, "expired pin must clear model so loadPin discards it")
			assert.True(t, expired.PinnedUntil.Before(time.Now()))
			assert.Equal(t, string(sessionpin.DemotionReasonCommittedStreamFailure), expired.Reason)
		})
	}
}

// A fresh authoritative pick that dies after commit must be demoted too:
// unlike the overload breaker, this path is not gated on a prior sticky hit.
func TestMaybeDemoteArmAfterCommittedStreamFailure_FiresWithoutStickyPin(t *testing.T) {
	store := &demotionStubPinStore{}
	svc := newDemotionTestService(store, true)

	demoted := svc.maybeDemoteArmAfterCommittedStreamFailure(
		context.Background(),
		true,
		false,
		&providers.UpstreamStatusError{Status: http.StatusBadGateway},
		demotedArm,
		"hmm:authoritative model=claude-opus-4-7",
		uuid.New(),
		nonZeroSessionKey(),
		sessionpin.DefaultRole, sessionpin.DefaultRole,
	)

	assert.Equal(t, demotedArm, demoted)
	assert.Len(t, store.demotions, 2)
}

// Hard-pinned turns (utility turns, native web-search passthrough) never write
// session routing state: a passthrough sub-turn that dies after commit must
// not expire the conversation's pin or strike out its arm.
func TestMaybeDemoteArmAfterCommittedStreamFailure_HardPinnedSkipped(t *testing.T) {
	store := &demotionStubPinStore{}
	svc := newDemotionTestService(store, true)

	demoted := svc.maybeDemoteArmAfterCommittedStreamFailure(
		context.Background(),
		true,
		true,
		&providers.UpstreamStatusError{Status: http.StatusBadGateway},
		demotedArm,
		nativeWebSearchPassthroughReason,
		uuid.New(),
		nonZeroSessionKey(),
		sessionpin.DefaultRole, sessionpin.DefaultRole,
	)

	assert.Empty(t, demoted)
	assert.Empty(t, store.demotions)
	assert.Empty(t, store.upserts, "a hard-pinned turn must not evict the session pin")
}

// A caller whose per-cluster allowlists admit only the failed model has pinned
// themselves to it; the override layer would fall open to whatever the roster
// has left, so the strike is not written at all.
func TestMaybeDemoteArmAfterCommittedStreamFailure_ClusterAllowlistPinSkipped(t *testing.T) {
	cases := []struct {
		name        string
		keyLists    map[string][]string
		userLists   map[string][]string
		wantDemoted bool
	}{
		{name: "no lists configured", wantDemoted: true},
		{name: "key lists pin every cluster to the failed model", keyLists: map[string][]string{"medium": {demotedArm}, "high": {demotedArm}, "maximum": {demotedArm}}, wantDemoted: false},
		{name: "user lists pin every cluster to the failed model", userLists: map[string][]string{"high": {demotedArm}}, wantDemoted: false},
		{name: "lists name an Anthropic sibling", keyLists: map[string][]string{"high": {demotedArm, "claude-sonnet-5"}}, wantDemoted: true},
		{name: "lists name another vendor on a different cluster", keyLists: map[string][]string{"high": {demotedArm}, "low": {"gpt-5.6-luna"}}, wantDemoted: true},
		{name: "intersection with the org list collapses to the failed model", keyLists: map[string][]string{"high": {demotedArm, "claude-sonnet-5"}}, userLists: map[string][]string{"high": {demotedArm}}, wantDemoted: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &demotionStubPinStore{}
			svc := newDemotionTestService(store, true)
			ctx := context.Background()
			if tc.keyLists != nil {
				ctx = context.WithValue(ctx, ClusterModelListsContextKey{}, tc.keyLists)
			}
			if tc.userLists != nil {
				ctx = context.WithValue(ctx, auth.UserClusterModelListsContextKey{}, tc.userLists)
			}

			demoted := svc.maybeDemoteArmAfterCommittedStreamFailure(
				ctx,
				true,
				false,
				&providers.UpstreamStatusError{Status: http.StatusBadGateway},
				demotedArm,
				"hmm:authoritative model=claude-opus-4-7",
				uuid.New(),
				nonZeroSessionKey(),
				sessionpin.DefaultRole, sessionpin.DefaultRole,
			)

			if !tc.wantDemoted {
				assert.Empty(t, demoted)
				assert.Empty(t, store.demotions)
				assert.Empty(t, store.upserts, "a pinned caller keeps its pin rows")
				return
			}
			assert.Equal(t, demotedArm, demoted)
			assert.Len(t, store.demotions, 2)
		})
	}
}

func TestDemotionRoles(t *testing.T) {
	cases := []struct {
		name    string
		role    string
		pinRole string
		want    []string
	}{
		{name: "base role sticky", role: "default", pinRole: "default", want: []string{"default", "default_hmm_history"}},
		{name: "hmm history sticky", role: "default_hmm_history", pinRole: "default", want: []string{"default_hmm_history", "default"}},
		{name: "sub-agent role", role: "subagent", pinRole: "subagent", want: []string{"subagent", "subagent_hmm_history"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, demotionRoles(tc.role, tc.pinRole))
		})
	}
}

// The explicit-force escape hatch: /force-model must not be undone by an
// automatic strike-out.
func TestMaybeDemoteArmAfterCommittedStreamFailure_UserForcedSkipped(t *testing.T) {
	for _, reason := range []string{translate.ReasonUserForceModel, translate.ReasonUserForceModel + "+tier_clamp"} {
		store := &demotionStubPinStore{}
		svc := newDemotionTestService(store, true)

		demoted := svc.maybeDemoteArmAfterCommittedStreamFailure(
			context.Background(),
			true,
			false,
			&providers.UpstreamStatusError{Status: http.StatusBadGateway},
			demotedArm,
			reason,
			uuid.New(),
			nonZeroSessionKey(),
			sessionpin.DefaultRole, sessionpin.DefaultRole,
		)

		assert.Empty(t, demoted, "user-forced pins (%q) must not be demoted", reason)
		assert.Empty(t, store.demotions)
	}
}

// No addressable pin row ⇒ no write: a zero session key or an unauthenticated
// request would otherwise share/create a bogus row.
func TestMaybeDemoteArmAfterCommittedStreamFailure_UnaddressableSkipped(t *testing.T) {
	cases := []struct {
		name           string
		installationID uuid.UUID
		sessionKey     [sessionpin.SessionKeyLen]byte
	}{
		{name: "zero session key", installationID: uuid.New(), sessionKey: [sessionpin.SessionKeyLen]byte{}},
		{name: "nil installation", installationID: uuid.Nil, sessionKey: nonZeroSessionKey()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &demotionStubPinStore{}
			svc := newDemotionTestService(store, true)

			demoted := svc.maybeDemoteArmAfterCommittedStreamFailure(
				context.Background(),
				true,
				false,
				&providers.UpstreamStatusError{Status: http.StatusBadGateway},
				demotedArm,
				"cluster:v0.57 model=claude-opus-4-7 provider=anthropic",
				tc.installationID,
				tc.sessionKey,
				sessionpin.DefaultRole, sessionpin.DefaultRole,
			)

			assert.Empty(t, demoted)
			assert.Empty(t, store.demotions)
			assert.Empty(t, store.upserts)
		})
	}
}

// rowBackedPinStore mirrors the adapter's row semantics: Upsert creates or
// replaces the (session_key, role) row (routing_strategy included, demoted
// models preserved); ExpireAndDemoteModel seeds a missing row, expires and
// strikes a row still owned by the caller's strategy, and leaves a row another
// strategy has taken over untouched.
type rowBackedPinStore struct {
	demotionStubPinStore
	rows map[string]sessionpin.Pin
}

func newRowBackedPinStore() *rowBackedPinStore {
	return &rowBackedPinStore{rows: map[string]sessionpin.Pin{}}
}

func (s *rowBackedPinStore) Upsert(ctx context.Context, p sessionpin.Pin) error {
	if err := s.demotionStubPinStore.Upsert(ctx, p); err != nil {
		return err
	}
	existing := s.rows[p.Role]
	p.DemotedModels = existing.DemotedModels
	s.rows[p.Role] = p
	return nil
}

func (s *rowBackedPinStore) ExpireAndDemoteModel(ctx context.Context, expired sessionpin.Pin, model string, reason sessionpin.DemotionReason) error {
	if err := s.demotionStubPinStore.ExpireAndDemoteModel(ctx, expired, model, reason); err != nil {
		return err
	}
	row, ok := s.rows[expired.Role]
	if !ok {
		expired.DemotedModels = []string{model}
		s.rows[expired.Role] = expired
		return nil
	}
	if !(row.Strategy == expired.Strategy || (row.Strategy == "" && expired.Strategy != router.StrategyHMMBeta)) {
		return nil
	}
	demoted := row.DemotedModels
	if !slices.Contains(demoted, model) {
		demoted = append(demoted, model)
	}
	expired.DemotedModels = demoted
	s.rows[expired.Role] = expired
	return nil
}

// The strike has to survive the session that has no pin row yet — a fresh
// authoritative pick, a post-sweep turn, an escalation. Seeding the row in the
// same write as the strike is what makes one strike hold there, and writing it
// to the _hmm_history row too is what keeps it past the sweep of the expired
// base row while HMM turns keep refreshing only the history row.
func TestMaybeDemoteArmAfterCommittedStreamFailure_PersistsWithoutExistingRow(t *testing.T) {
	cases := []struct {
		name string
		role string
	}{
		{name: "base role", role: sessionpin.DefaultRole},
		{name: "hmm history role", role: hmmHistoryRole(sessionpin.DefaultRole)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newRowBackedPinStore()
			svc := newDemotionTestService(store, true)

			demoted := svc.maybeDemoteArmAfterCommittedStreamFailure(
				context.Background(),
				true,
				false,
				&providers.UpstreamStatusError{Status: http.StatusBadGateway},
				demotedArm,
				"hmm:authoritative model=claude-opus-4-7",
				uuid.New(),
				nonZeroSessionKey(),
				tc.role, sessionpin.DefaultRole,
			)

			assert.Equal(t, demotedArm, demoted)
			for _, role := range []string{sessionpin.DefaultRole, hmmHistoryRole(sessionpin.DefaultRole)} {
				row, ok := store.rows[role]
				require.True(t, ok, "demotion must seed row %q", role)
				assert.Equal(t, []string{demotedArm}, row.DemotedModels, "row %q", role)
				assert.Empty(t, row.Model, "row %q must be expired so the next turn re-routes", role)
			}
		})
	}
}

func newRescuedDemotionTestService(store sessionpin.Store, flagOn bool) *Service {
	return NewService(
		nil,
		nil,
		nil,
		false,
		nil,
		store,
		false,
		"anthropic", "claude-haiku-4-5",
		nil,
	).WithRescuedFailureArmDemotion(flagOn)
}

func rescuedPrimaryDecision(reason string) router.Decision {
	return router.Decision{Provider: providers.ProviderAnthropic, Model: demotedArm, Reason: reason}
}

// Classification of the rescued-failure path: the rescue must have run, and
// the primary's error must be one the arm owns. Whether the rescuer then
// served is irrelevant; the primary failed either way.
func TestMaybeDemoteArmAfterRescuedFailure_Classification(t *testing.T) {
	cases := []struct {
		name      string
		rescueRan bool
		flagOn    bool
		err       error
		want      bool
	}{
		{name: "rescued buffered 502", rescueRan: true, flagOn: true, err: &providers.UpstreamErrorResponse{Status: http.StatusBadGateway}, want: true},
		{name: "rescued buffered 500", rescueRan: true, flagOn: true, err: &providers.UpstreamErrorResponse{Status: http.StatusInternalServerError}, want: true},
		{name: "rescued 429", rescueRan: true, flagOn: true, err: &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests}, want: true},
		{name: "rescued transport error", rescueRan: true, flagOn: true, err: errors.New("upstream call: connection reset"), want: true},
		{name: "rescued idle watchdog", rescueRan: true, flagOn: true, err: fmt.Errorf("stream: %w", providers.ErrUpstreamIdleTimeout), want: true},
		{name: "rescued output stall chained with cancellation", rescueRan: true, flagOn: true, err: fmt.Errorf("%w: %w", providers.ErrUpstreamOutputStall, context.Canceled), want: true},
		{name: "rescued billing block", rescueRan: true, flagOn: true, err: &providers.UpstreamErrorResponse{Status: http.StatusPaymentRequired}, want: true},
		{name: "no rescue ran", rescueRan: false, flagOn: true, err: &providers.UpstreamErrorResponse{Status: http.StatusBadGateway}, want: false},
		{name: "no primary error", rescueRan: true, flagOn: true, err: nil, want: false},
		{name: "provider overloaded 529", rescueRan: true, flagOn: true, err: &providers.UpstreamErrorResponse{Status: providerOverloadedStatus}, want: false},
		{name: "gateway lacks model", rescueRan: true, flagOn: true, err: &providers.UpstreamErrorResponse{Status: http.StatusNotFound}, want: false},
		{name: "subscription pool exhausted", rescueRan: true, flagOn: true, err: ErrSubscriptionPoolExhausted, want: false},
		{name: "client cancellation", rescueRan: true, flagOn: true, err: fmt.Errorf("copy body: %w", context.Canceled), want: false},
		{name: "flag off", rescueRan: true, flagOn: false, err: &providers.UpstreamErrorResponse{Status: http.StatusBadGateway}, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &demotionStubPinStore{}
			svc := newRescuedDemotionTestService(store, tc.flagOn)
			installationID := uuid.New()

			demoted := svc.maybeDemoteArmAfterRescuedFailure(
				context.Background(),
				tc.rescueRan,
				false,
				tc.err,
				rescuedPrimaryDecision("hmm:authoritative model=claude-opus-4-7"),
				installationID,
				nonZeroSessionKey(),
				sessionpin.DefaultRole, sessionpin.DefaultRole,
			)

			if !tc.want {
				assert.Empty(t, demoted)
				assert.Empty(t, store.demotions, "arm must stay eligible")
				assert.Empty(t, store.upserts, "pin must not be evicted")
				return
			}

			assert.Equal(t, demotedArm, demoted)
			assert.Equal(t, []demotionCall{
				{role: sessionpin.DefaultRole, model: demotedArm, reason: sessionpin.DemotionReasonRescuedFailure},
				{role: hmmHistoryRole(sessionpin.DefaultRole), model: demotedArm, reason: sessionpin.DemotionReasonRescuedFailure},
			}, store.demotions, "the strike must land on both rows the next turn merges")
			assert.Empty(t, store.upserts, "expiry must ride the guarded demotion write, not a plain upsert")
			require.Len(t, store.expired, 2, "demotion must expire the pin and its HMM history row")
			assert.Equal(t, installationID, store.expired[0].InstallationID)
			assert.Equal(t, string(sessionpin.DemotionReasonRescuedFailure), store.expired[0].Reason)
		})
	}
}

// The two demotion hooks are independent: a turn whose primary was rescued
// and whose rescuer then died after commit strikes both arms, each under its
// own reason, and neither hook fires when its own flag is off.
func TestRescuedAndCommittedDemotionsAreIndependentlyFlagged(t *testing.T) {
	const rescuer = "claude-sonnet-5"
	cases := []struct {
		name         string
		rescuedOn    bool
		committedOn  bool
		wantRescued  string
		wantCommited string
	}{
		{name: "both on", rescuedOn: true, committedOn: true, wantRescued: demotedArm, wantCommited: rescuer},
		{name: "rescued only", rescuedOn: true, committedOn: false, wantRescued: demotedArm},
		{name: "committed only", rescuedOn: false, committedOn: true, wantCommited: rescuer},
		{name: "both off", rescuedOn: false, committedOn: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &demotionStubPinStore{}
			svc := newDemotionTestService(store, tc.committedOn).WithRescuedFailureArmDemotion(tc.rescuedOn)
			installationID := uuid.New()
			key := nonZeroSessionKey()

			committed := svc.maybeDemoteArmAfterCommittedStreamFailure(
				context.Background(), true, false,
				&providers.UpstreamStatusError{Status: http.StatusBadGateway},
				rescuer, "hmm:authoritative model=claude-opus-4-7",
				installationID, key, sessionpin.DefaultRole, sessionpin.DefaultRole,
			)
			rescued := svc.maybeDemoteArmAfterRescuedFailure(
				context.Background(), true, false,
				&providers.UpstreamErrorResponse{Status: http.StatusBadGateway},
				rescuedPrimaryDecision("hmm:authoritative model=claude-opus-4-7"),
				installationID, key, sessionpin.DefaultRole, sessionpin.DefaultRole,
			)

			assert.Equal(t, tc.wantCommited, committed)
			assert.Equal(t, tc.wantRescued, rescued)
			var want []demotionCall
			if tc.wantCommited != "" {
				want = append(want,
					demotionCall{role: sessionpin.DefaultRole, model: rescuer, reason: sessionpin.DemotionReasonCommittedStreamFailure},
					demotionCall{role: hmmHistoryRole(sessionpin.DefaultRole), model: rescuer, reason: sessionpin.DemotionReasonCommittedStreamFailure},
				)
			}
			if tc.wantRescued != "" {
				want = append(want,
					demotionCall{role: sessionpin.DefaultRole, model: demotedArm, reason: sessionpin.DemotionReasonRescuedFailure},
					demotionCall{role: hmmHistoryRole(sessionpin.DefaultRole), model: demotedArm, reason: sessionpin.DemotionReasonRescuedFailure},
				)
			}
			assert.Equal(t, want, store.demotions)
		})
	}
}

// Hard-pinned and user-forced primaries keep their arm even when a rescue ran
// for them; the explicit choice outranks the automatic strike.
func TestMaybeDemoteArmAfterRescuedFailure_PinnedPrimarySkipped(t *testing.T) {
	cases := []struct {
		name       string
		hardPinned bool
		reason     string
	}{
		{name: "hard pinned", hardPinned: true, reason: nativeWebSearchPassthroughReason},
		{name: "user forced", reason: translate.ReasonUserForceModel},
		{name: "user forced with tier clamp", reason: translate.ReasonUserForceModel + "+tier_clamp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &demotionStubPinStore{}
			svc := newRescuedDemotionTestService(store, true)

			demoted := svc.maybeDemoteArmAfterRescuedFailure(
				context.Background(),
				true,
				tc.hardPinned,
				&providers.UpstreamErrorResponse{Status: http.StatusBadGateway},
				rescuedPrimaryDecision(tc.reason),
				uuid.New(),
				nonZeroSessionKey(),
				sessionpin.DefaultRole, sessionpin.DefaultRole,
			)

			assert.Empty(t, demoted)
			assert.Empty(t, store.demotions)
			assert.Empty(t, store.upserts, "a pinned turn must not evict the session pin")
		})
	}
}

// The pure-Opus red line, same as the committed-stream path: allowlists that
// admit only the primary mean there is no sibling to move to, so the arm is
// kept rather than letting the override layer fall open.
func TestMaybeDemoteArmAfterRescuedFailure_ClusterAllowlistPinSkipped(t *testing.T) {
	cases := []struct {
		name        string
		keyLists    map[string][]string
		wantDemoted bool
	}{
		{name: "no lists configured", wantDemoted: true},
		{name: "lists pin every cluster to the primary", keyLists: map[string][]string{"medium": {demotedArm}, "high": {demotedArm}, "maximum": {demotedArm}}, wantDemoted: false},
		{name: "lists name an Anthropic sibling", keyLists: map[string][]string{"high": {demotedArm, "claude-sonnet-5"}}, wantDemoted: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &demotionStubPinStore{}
			svc := newRescuedDemotionTestService(store, true)
			ctx := context.Background()
			if tc.keyLists != nil {
				ctx = context.WithValue(ctx, ClusterModelListsContextKey{}, tc.keyLists)
			}

			demoted := svc.maybeDemoteArmAfterRescuedFailure(
				ctx,
				true,
				false,
				&providers.UpstreamErrorResponse{Status: http.StatusBadGateway},
				rescuedPrimaryDecision("hmm:authoritative model=claude-opus-4-7"),
				uuid.New(),
				nonZeroSessionKey(),
				sessionpin.DefaultRole, sessionpin.DefaultRole,
			)

			if !tc.wantDemoted {
				assert.Empty(t, demoted)
				assert.Empty(t, store.demotions)
				return
			}
			assert.Equal(t, demotedArm, demoted)
			assert.Len(t, store.demotions, 2)
		})
	}
}

// Same row semantics as the committed-stream strike: the eviction upsert
// seeds both rows so the rescued-failure strike holds on a session that
// never pinned.
func TestMaybeDemoteArmAfterRescuedFailure_PersistsWithoutExistingRow(t *testing.T) {
	store := newRowBackedPinStore()
	svc := newRescuedDemotionTestService(store, true)

	demoted := svc.maybeDemoteArmAfterRescuedFailure(
		context.Background(),
		true,
		false,
		&providers.UpstreamErrorResponse{Status: http.StatusBadGateway},
		rescuedPrimaryDecision("hmm:authoritative model=claude-opus-4-7"),
		uuid.New(),
		nonZeroSessionKey(),
		sessionpin.DefaultRole, sessionpin.DefaultRole,
	)

	assert.Equal(t, demotedArm, demoted)
	for _, role := range []string{sessionpin.DefaultRole, hmmHistoryRole(sessionpin.DefaultRole)} {
		row, ok := store.rows[role]
		require.True(t, ok, "eviction must seed row %q before the demotion writes to it", role)
		assert.Equal(t, []string{demotedArm}, row.DemotedModels, "row %q", role)
	}
}

// A late failure from a request routed under one strategy must not touch the
// pin a newer request stored under another strategy for the same session.
// Request A (hmm) is still streaming when request B replaces the pin with a
// live hmm_beta row; A then fails after commit. Its expiry-and-strike must
// neither expire B's pin nor record A's demotion on B's row, and it must not
// hand the row back to hmm on the way.
func TestMaybeDemoteArmAfterCommittedStreamFailure_LateFailureLeavesReplacementStrategyRow(t *testing.T) {
	store := newRowBackedPinStore()
	svc := newDemotionTestService(store, true)
	installationID := uuid.New()
	key := nonZeroSessionKey()
	ctxA := router.WithStrategy(context.Background(), router.StrategyHMM)

	// Request A's pin, under hmm.
	require.NoError(t, store.Upsert(ctxA, sessionpin.Pin{
		SessionKey: key, Role: sessionpin.DefaultRole, InstallationID: installationID,
		Provider: "anthropic", Model: demotedArm, Strategy: router.StrategyHMM,
		TurnCount: 1, PinnedUntil: time.Now().Add(time.Hour),
	}))
	// Request B replaces it with a live hmm_beta pin on both rows.
	betaPin := sessionpin.Pin{
		SessionKey: key, Role: sessionpin.DefaultRole, InstallationID: installationID,
		Provider: "anthropic", Model: "claude-sonnet-4-6", Strategy: router.StrategyHMMBeta,
		TurnCount: 2, PinnedUntil: time.Now().Add(time.Hour),
	}
	require.NoError(t, store.Upsert(context.Background(), betaPin))
	betaHistory := betaPin
	betaHistory.Role = hmmHistoryRole(sessionpin.DefaultRole)
	require.NoError(t, store.Upsert(context.Background(), betaHistory))
	store.upserts = nil

	// Request A's committed stream now fails.
	demoted := svc.maybeDemoteArmAfterCommittedStreamFailure(
		ctxA,
		true,
		false,
		&providers.UpstreamStatusError{Status: http.StatusBadGateway},
		demotedArm,
		"hmm:authoritative model=claude-opus-4-7",
		installationID,
		key,
		sessionpin.DefaultRole, sessionpin.DefaultRole,
	)

	assert.Equal(t, demotedArm, demoted, "the turn still reports its own demotion attempt")
	assert.Empty(t, store.upserts, "no plain upsert may run ahead of the guarded write")
	for _, expired := range store.expired {
		assert.Equal(t, router.StrategyHMM, expired.Strategy, "row %q: the write must carry the failing request's strategy", expired.Role)
	}
	for role, want := range map[string]sessionpin.Pin{
		sessionpin.DefaultRole:                 betaPin,
		hmmHistoryRole(sessionpin.DefaultRole): betaHistory,
	} {
		row, ok := store.rows[role]
		require.True(t, ok, "row %q", role)
		assert.Equal(t, router.StrategyHMMBeta, row.Strategy, "row %q must stay owned by hmm_beta", role)
		assert.Equal(t, want.Model, row.Model, "row %q must keep its live beta pin", role)
		assert.True(t, row.PinnedUntil.After(time.Now()), "row %q must not be expired", role)
		assert.Empty(t, row.DemotedModels, "row %q must not carry the stale hmm demotion", role)
	}
}

// A committed stream that ends because the client went away is not an
// upstream failure. httputil.StreamBody returns the response writer's error
// as-is (broken pipe, connection reset) with no cancellation in its chain and
// no status, so the request context is the only signal; the upstream
// watchdogs cancel too, but with their sentinel as the cause, and must keep
// demoting.
func TestIsCommittedStreamFailure_ClientDisconnect(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	causedByClient, cancelCause := context.WithCancelCause(context.Background())
	cancelCause(errors.New("client went away"))
	deadline, cancelDeadline := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelDeadline()
	stalled, cancelStalled := context.WithCancelCause(context.Background())
	cancelStalled(providers.ErrUpstreamOutputStall)

	brokenPipe := errors.New("write tcp 10.0.0.1:443->10.0.0.2:51234: write: broken pipe")

	cases := []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{name: "broken pipe, client still connected", ctx: context.Background(), err: brokenPipe, want: true},
		{name: "broken pipe after client cancel", ctx: canceled, err: brokenPipe, want: false},
		{name: "connection reset with cancel cause", ctx: causedByClient, err: errors.New("write: connection reset by peer"), want: false},
		{name: "unexpected EOF after client deadline", ctx: deadline, err: errors.New("upstream call: unexpected EOF"), want: false},
		{name: "watchdog sentinel in error, ctx canceled", ctx: canceled, err: fmt.Errorf("stream: %w", providers.ErrUpstreamIdleTimeout), want: true},
		{name: "watchdog sentinel as cancel cause, bare error", ctx: stalled, err: errors.New("context canceled"), want: true},
		{name: "upstream 502 after client cancel", ctx: canceled, err: &providers.UpstreamStatusError{Status: http.StatusBadGateway}, want: true},
		{name: "529 with client connected", ctx: context.Background(), err: &providers.UpstreamErrorResponse{Status: providerOverloadedStatus}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isCommittedStreamFailure(tc.ctx, tc.err))
		})
	}
}

// The completion line's reason field is only populated on a real demotion.
func TestArmDemotionReason(t *testing.T) {
	assert.Equal(t, "", armDemotionReason(""))
	assert.Equal(t, "committed_stream_failure", armDemotionReason(demotedArm))
}

// arm_demoted names the committed-stream strike when it landed and the
// rescued-primary strike otherwise; rescued_arm_demoted keeps the primary
// visible when both landed on one turn.
func TestArmDemotionLogFields(t *testing.T) {
	assert.Equal(t,
		[]any{"arm_demoted", "", "arm_demotion_reason", "", "rescued_arm_demoted", ""},
		armDemotionLogFields("", ""))
	assert.Equal(t,
		[]any{"arm_demoted", demotedArm, "arm_demotion_reason", "committed_stream_failure", "rescued_arm_demoted", ""},
		armDemotionLogFields(demotedArm, ""))
	assert.Equal(t,
		[]any{"arm_demoted", demotedArm, "arm_demotion_reason", "rescued_failure", "rescued_arm_demoted", demotedArm},
		armDemotionLogFields("", demotedArm))
	assert.Equal(t,
		[]any{"arm_demoted", rescuerModel, "arm_demotion_reason", "committed_stream_failure", "rescued_arm_demoted", demotedArm},
		armDemotionLogFields(rescuerModel, demotedArm))
}
