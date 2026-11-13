package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/router/escalation"
)

type escalationCommitTestStore struct {
	*escalationTestStore
	leaseToken     string
	commitThenFail bool
}

func (s *escalationCommitTestStore) Claim(ctx context.Context, scope [32]byte, installationID, token string, boundary [32]byte) (escalation.Session, bool, error) {
	if s.leaseToken != "" {
		return escalation.Session{}, false, nil
	}
	s.leaseToken = token
	return s.escalationTestStore.Claim(ctx, scope, installationID, token, boundary)
}

func (s *escalationCommitTestStore) Release(_ context.Context, _ [32]byte, token string) error {
	if s.leaseToken == token {
		s.leaseToken = ""
	}
	return nil
}

func (s *escalationCommitTestStore) Commit(ctx context.Context, scope, boundary [32]byte, token string, session escalation.Session, checkpoint escalation.Checkpoint) error {
	if s.commitThenFail {
		if err := s.escalationTestStore.Commit(ctx, scope, boundary, token, session, checkpoint); err != nil {
			return err
		}
		s.leaseToken = ""
	}
	return errors.New("commit connection lost")
}

func (s *escalationCommitTestStore) Invalidate(ctx context.Context, scope, boundary [32]byte, failedToken string) error {
	if _, committed := s.checkpoints[scope][boundary]; committed {
		return nil
	}
	if s.leaseToken == failedToken {
		s.leaseToken = ""
	}
	return s.escalationTestStore.Invalidate(ctx, scope, boundary, failedToken)
}

func TestEscalationCommitFailureReleasesLeaseAndPreservesOnlyCommittedFeatures(t *testing.T) {
	for _, committed := range []bool{false, true} {
		name := "rolled_back"
		if committed {
			name = "committed_but_acknowledgment_lost"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			scope, boundary := [32]byte{1}, [32]byte{2}
			store := &escalationCommitTestStore{escalationTestStore: newEscalationTestStore(), commitThenFail: committed}
			store.sessions[scope] = escalation.Session{Ordinal: 4, Floor: escalation.Medium, FeatureTurns: 4, FeatureState: json.RawMessage(`{"observed_turns":4}`), PreviousOutcome: &escalation.PreviousOutcome{StatusCode: 200}}
			token, installationID := uuid.NewString(), uuid.NewString()
			session, acquired, err := store.Claim(ctx, scope, installationID, token, boundary)
			require.NoError(t, err)
			require.True(t, acquired)
			session.Ordinal = 5
			session.FeatureTurns = 5
			session.FeatureState = json.RawMessage(`{"observed_turns":5}`)
			svc := (&Service{}).WithEscalation(store, nil)
			turn := &escalationTurn{scope: scope, boundary: boundary, token: token, session: session, checkpoint: escalation.Checkpoint{Ordinal: 5}}
			err = svc.finishEscalation(ctx, turn, &turnLoopResult{}, nil)
			require.ErrorContains(t, err, "commit connection lost")
			next, acquired, err := store.Claim(ctx, scope, installationID, uuid.NewString(), [32]byte{3})
			require.NoError(t, err)
			require.True(t, acquired, "the next turn must not wait for lease expiry")
			require.Equal(t, escalation.Medium, next.Floor)
			if committed {
				require.Equal(t, int64(5), next.Ordinal)
				require.Equal(t, int64(5), next.FeatureTurns)
				require.JSONEq(t, `{"observed_turns":5}`, string(next.FeatureState))
			} else {
				require.Equal(t, int64(4), next.Ordinal)
				require.Zero(t, next.FeatureTurns)
				require.Empty(t, next.FeatureState)
				require.Nil(t, next.PreviousOutcome)
			}
		})
	}
}
