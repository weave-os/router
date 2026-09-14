package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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

// demotionCall is one recorded DemoteModel invocation.
type demotionCall struct {
	role   string
	model  string
	reason sessionpin.DemotionReason
}

type demotionStubPinStore struct {
	mu        sync.Mutex
	demotions []demotionCall
	upserts   []sessionpin.Pin
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

func (s *demotionStubPinStore) DemoteModel(_ context.Context, _ [sessionpin.SessionKeyLen]byte, role, model string, reason sessionpin.DemotionReason, _ router.Strategy) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.demotions = append(s.demotions, demotionCall{role: role, model: model, reason: reason})
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

			demoted := svc.maybeDemoteArmAfterCommittedStreamFailure(
				context.Background(),
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
			require.Len(t, store.upserts, 2, "demotion must expire the pin and its HMM history row")
			expired := store.upserts[0]
			assert.Equal(t, sessionpin.DefaultRole, expired.Role)
			assert.Equal(t, installationID, expired.InstallationID)
			assert.Equal(t, hmmHistoryRole(sessionpin.DefaultRole), store.upserts[1].Role)
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

// rowBackedPinStore mirrors the adapter's row semantics: Upsert creates the
// (session_key, role) row, DemoteModel is an UPDATE that touches nothing when
// that row is absent.
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

func (s *rowBackedPinStore) DemoteModel(ctx context.Context, key [sessionpin.SessionKeyLen]byte, role, model string, reason sessionpin.DemotionReason, strategy router.Strategy) error {
	if err := s.demotionStubPinStore.DemoteModel(ctx, key, role, model, reason, strategy); err != nil {
		return err
	}
	row, ok := s.rows[role]
	if !ok {
		return nil
	}
	for _, m := range row.DemotedModels {
		if m == model {
			return nil
		}
	}
	row.DemotedModels = append(row.DemotedModels, model)
	s.rows[role] = row
	return nil
}

// The strike has to survive the session that has no pin row yet — a fresh
// authoritative pick, a post-sweep turn, an escalation. Ordering the eviction
// upsert before the demote UPDATE is what makes one strike hold there, and
// writing it to the _hmm_history row too is what keeps it past the sweep of
// the expired base row while HMM turns keep refreshing only the history row.
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
				require.True(t, ok, "eviction must seed row %q before the demotion writes to it", role)
				assert.Equal(t, []string{demotedArm}, row.DemotedModels, "row %q", role)
			}
		})
	}
}

// The completion line's reason field is only populated on a real demotion.
func TestArmDemotionReason(t *testing.T) {
	assert.Equal(t, "", armDemotionReason(""))
	assert.Equal(t, "committed_stream_failure", armDemotionReason(demotedArm))
}
