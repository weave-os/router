package policyshadow_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/router"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/telemetry/policyshadow"
)

const (
	recipeInstallationID = "11111111-1111-4111-8111-111111111111"
	defaultProfileKey    = "00000000-0000-5000-8000-0000000000d1"
	efficientProfileKey  = "00000000-0000-5000-8000-0000000000e1"
	promptSentinel       = "PROMPT-SENTINEL-must-never-be-stored"
)

type recordingStore struct {
	mu     sync.Mutex
	events []telemetry.PolicyShadowDecision
	done   chan struct{}
}

func newRecordingStore() *recordingStore { return &recordingStore{done: make(chan struct{}, 4)} }

func (s *recordingStore) InsertPolicyShadowDecision(_ context.Context, event telemetry.PolicyShadowDecision) error {
	s.mu.Lock()
	s.events = append(s.events, event)
	s.mu.Unlock()
	s.done <- struct{}{}
	return nil
}

func (s *recordingStore) wait(t *testing.T) telemetry.PolicyShadowDecision {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
		t.Fatal("recipe shadow did not record")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.events[len(s.events)-1]
}

type fakeProfiles struct {
	profileKey string
	assigned   bool
	err        error
	asked      []router.RoutingModel
}

func (f *fakeProfiles) ProfileKey(_ context.Context, _ string, model router.RoutingModel) (string, bool, error) {
	f.asked = append(f.asked, model)
	return f.profileKey, f.assigned, f.err
}

type fakeReplayer struct {
	scored   bool
	pick     router.RecipeShadowPick
	err      error
	replayed chan router.Request
}

func (f *fakeReplayer) ServedFacts(router.Decision) (router.RecipeShadowServed, bool) {
	return router.RecipeShadowServed{RosterArmID: "anthropic/claude-opus-5.5", PolicyGroup: "high", PredictedLabel: "high"}, f.scored
}

func (f *fakeReplayer) Replay(_ context.Context, _ string, _ router.Decision, req router.Request) (router.RecipeShadowPick, error) {
	if f.replayed != nil {
		f.replayed <- req
	}
	return f.pick, f.err
}

func recipeTurn() policyshadow.RecipeTurn {
	return policyshadow.RecipeTurn{
		InstallationID:    recipeInstallationID,
		ServingStrategy:   router.StrategyHMM,
		ServingProfileKey: defaultProfileKey,
		Served:            router.Decision{Model: "claude-opus-5-5", Provider: "anthropic", Metadata: &router.RoutingMetadata{RouteID: "route-1"}},
		Request:           router.Request{OrganizationID: "org-1", ClientApp: "claude-code", PromptText: promptSentinel},
	}
}

func recorder(store *recordingStore, profiles *fakeProfiles, replayer *fakeReplayer) policyshadow.RecipeRecorder {
	return policyshadow.RecipeRecorder{Store: store, Profiles: profiles, Replayer: replayer, ShadowModel: router.ModelAutoEfficient, Now: time.Now}
}

func TestRecipeShadowRecordsWouldHavePickedWithoutContent(t *testing.T) {
	store := newRecordingStore()
	profiles := &fakeProfiles{profileKey: efficientProfileKey, assigned: true}
	replayer := &fakeReplayer{scored: true, pick: router.RecipeShadowPick{
		Model: "claude-sonnet-5-5", Provider: "anthropic", RosterArmID: "anthropic/claude-sonnet-5.5",
		PolicyGroup: "high", PolicySHA256: "sha256:efficient",
	}}

	recorder(store, profiles, replayer).Fire(context.Background(), recipeTurn())
	event := store.wait(t)

	assert.Equal(t, []router.RoutingModel{router.ModelAutoEfficient}, profiles.asked)
	assert.Equal(t, recipeInstallationID, event.InstallationID)
	assert.Equal(t, string(router.ModelAutoEfficient), event.ShadowRoutingModel)
	assert.Equal(t, defaultProfileKey, event.ServingProfileKey)
	assert.Equal(t, efficientProfileKey, event.ShadowProfileKey)
	assert.Equal(t, "claude-opus-5-5", event.ServingModel)
	assert.Equal(t, "claude-sonnet-5-5", event.ShadowModel)
	assert.Equal(t, "anthropic/claude-opus-5.5", event.ServingRosterArmID)
	assert.Equal(t, "anthropic/claude-sonnet-5.5", event.ShadowRosterArmID)
	assert.Equal(t, "high", event.ClassifierPredictedLabel)
	assert.Equal(t, "high", event.ServingPolicyGroup)
	assert.Equal(t, "high", event.ShadowPolicyGroup)
	assert.Equal(t, "sha256:efficient", event.ShadowPolicyArtifactSHA256)
	assert.False(t, event.ModelsAgree)
	assert.Empty(t, event.ShadowError)

	encoded, err := json.Marshal(event)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), promptSentinel, "the shadow row must hold identifiers only, never prompt text")
}

func TestRecipeShadowRecordsReplayErrorAsAReasonCode(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{err: fmt.Errorf("picked arm: %w", router.ErrRecipeShadowNoEligibleArm), want: "no_eligible_arm"},
		{err: router.ErrRecipeShadowNoClassifierFacts, want: "no_classifier_facts"},
		{err: errors.New("read gs://registry-bucket/objects/policy: not found"), want: "replay_failed"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			store := newRecordingStore()
			replayer := &fakeReplayer{scored: true, err: tc.err}

			recorder(store, &fakeProfiles{profileKey: efficientProfileKey, assigned: true}, replayer).Fire(context.Background(), recipeTurn())
			event := store.wait(t)

			assert.Equal(t, tc.want, event.ShadowError, "the row stores a closed reason code, never the raw error text")
			assert.Empty(t, event.ShadowModel)
			assert.False(t, event.ModelsAgree)
		})
	}
}

func TestRecipeShadowWritesNoRowWhenTheShadowProfileIsInactive(t *testing.T) {
	store := newRecordingStore()
	replayer := &fakeReplayer{scored: true, err: router.ErrRecipeShadowProfileInactive, replayed: make(chan router.Request, 1)}

	recorder(store, &fakeProfiles{profileKey: efficientProfileKey, assigned: true}, replayer).Fire(context.Background(), recipeTurn())

	<-replayer.replayed
	select {
	case <-store.done:
		t.Fatal("an inactive shadow profile must not write an error row")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRecipeShadowSkipsTurnsWithNothingToCompare(t *testing.T) {
	for name, tc := range map[string]struct {
		profiles *fakeProfiles
		replayer *fakeReplayer
		turn     func(policyshadow.RecipeTurn) policyshadow.RecipeTurn
	}{
		"not classifier scored":         {&fakeProfiles{profileKey: efficientProfileKey, assigned: true}, &fakeReplayer{scored: false}, nil},
		"no efficient profile assigned": {&fakeProfiles{}, &fakeReplayer{scored: true}, nil},
		"already serving the efficient profile": {&fakeProfiles{profileKey: efficientProfileKey, assigned: true}, &fakeReplayer{scored: true}, func(turn policyshadow.RecipeTurn) policyshadow.RecipeTurn {
			turn.ServingProfileKey = efficientProfileKey
			return turn
		}},
		"profile lookup failed": {&fakeProfiles{err: errors.New("db down")}, &fakeReplayer{scored: true}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			store := newRecordingStore()
			tc.replayer.replayed = make(chan router.Request, 1)
			turn := recipeTurn()
			if tc.turn != nil {
				turn = tc.turn(turn)
			}

			recorder(store, tc.profiles, tc.replayer).Fire(context.Background(), turn)

			select {
			case <-store.done:
				t.Fatal("a turn with nothing to compare must not write a shadow row")
			case <-tc.replayer.replayed:
				t.Fatal("a turn with nothing to compare must not replay selection")
			case <-time.After(100 * time.Millisecond):
			}
		})
	}
}
