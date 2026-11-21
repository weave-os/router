package conversation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/feedback"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeRouterFeedbackStore struct {
	events []feedback.RouterFeedbackEvent
	err    error
}

func (f *fakeRouterFeedbackStore) InsertRouterFeedback(_ context.Context, event feedback.RouterFeedbackEvent) error {
	f.events = append(f.events, event)
	return f.err
}

type fakeFeedbackTurns struct {
	turn     conversation.FeedbackTurn
	notFound bool
	err      error
}

func (f fakeFeedbackTurns) TurnBySequence(context.Context, uuid.UUID, []byte, string, int) (conversation.FeedbackTurn, bool, error) {
	return f.turn, f.notFound, f.err
}

type fakeServedModels string

func (f fakeServedModels) ServedModel(context.Context, router.Strategy, [sessionpin.SessionKeyLen]byte, string) string {
	return string(f)
}

type fakeRequestFeedback struct {
	upserts []conversation.RequestFeedback
}

func (f *fakeRequestFeedback) UpsertRequestFeedback(_ context.Context, feedback conversation.RequestFeedback) error {
	f.upserts = append(f.upserts, feedback)
	return nil
}

type noReporters struct{}

func (noReporters) Reporter(router.Strategy) (policy.FeedbackReporter, bool) { return nil, false }
func (noReporters) Owns(router.Strategy) bool                                { return false }

func feedbackRequest(cmd translate.RouterFeedbackResult) conversation.RouterFeedbackRequest {
	return conversation.RouterFeedbackRequest{
		Command:        cmd,
		InstallationID: uuid.New(),
		SessionKey:     [sessionpin.SessionKeyLen]byte{1},
		Role:           sessionpin.DefaultRole,
		RequestedModel: "requested-model",
	}
}

func recorder(store feedback.RouterFeedbackStore) conversation.RouterFeedback {
	return conversation.RouterFeedback{
		Store:     store,
		Reporters: noReporters{},
		Now:       func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
	}
}

func TestRouterFeedbackRejectsEmptyFeedback(t *testing.T) {
	store := &fakeRouterFeedbackStore{}
	result, err := recorder(store).Record(context.Background(), feedbackRequest(translate.RouterFeedbackResult{Feedback: "   "}))
	require.NoError(t, err)
	assert.Equal(t, conversation.RouterFeedbackEmpty, result)
	assert.Empty(t, store.events)
}

func TestRouterFeedbackMissingSequenceIsNotRecorded(t *testing.T) {
	store := &fakeRouterFeedbackStore{}
	feedback := recorder(store)
	feedback.Turns = fakeFeedbackTurns{notFound: true, err: errors.New("no rows")}
	result, err := feedback.Record(context.Background(), feedbackRequest(translate.RouterFeedbackResult{Rating: translate.RouterFeedbackRatingDown, Sequence: 3}))
	require.NoError(t, err)
	assert.Equal(t, conversation.RouterFeedbackMissingSequence, result)
	assert.Empty(t, store.events)
}

func TestRouterFeedbackRecordsSequenceTurn(t *testing.T) {
	store := &fakeRouterFeedbackStore{}
	requests := &fakeRequestFeedback{}
	feedback := recorder(store)
	feedback.Turns = fakeFeedbackTurns{turn: conversation.FeedbackTurn{DecisionModel: "served-model", RequestID: "request-1", RouteID: "route-1"}}
	feedback.ServedModels = fakeServedModels("pinned-model")
	feedback.Requests = requests

	result, err := feedback.Record(context.Background(), feedbackRequest(translate.RouterFeedbackResult{Rating: translate.RouterFeedbackRatingUp, Sequence: 2}))
	require.NoError(t, err)
	assert.Equal(t, conversation.RouterFeedbackRecorded, result)
	require.Len(t, store.events, 1)
	event := store.events[0]
	assert.Equal(t, "served-model", event.ServedModel)
	assert.Equal(t, "👍", event.Feedback)
	assert.Equal(t, conversation.RouterFeedbackSourceUser, event.Source)
	assert.Equal(t, "request-1", event.RequestID)
	require.Len(t, requests.upserts, 1)
	assert.Equal(t, "request-1", requests.upserts[0].RequestID)
}

func TestRouterFeedbackFallsBackToServedModelLookup(t *testing.T) {
	store := &fakeRouterFeedbackStore{}
	requests := &fakeRequestFeedback{}
	feedback := recorder(store)
	feedback.ServedModels = fakeServedModels("pinned-model")
	feedback.Requests = requests

	_, err := feedback.Record(context.Background(), feedbackRequest(translate.RouterFeedbackResult{Feedback: " too slow "}))
	require.NoError(t, err)
	require.Len(t, store.events, 1)
	assert.Equal(t, "pinned-model", store.events[0].ServedModel)
	assert.Equal(t, "too slow", store.events[0].Feedback)
	assert.Empty(t, requests.upserts, "request feedback needs a resolved request and a rating")
}

func TestRouterFeedbackStoreErrorIsReturned(t *testing.T) {
	store := &fakeRouterFeedbackStore{err: errors.New("insert failed")}
	_, err := recorder(store).Record(context.Background(), feedbackRequest(translate.RouterFeedbackResult{Rating: translate.RouterFeedbackRatingDown}))
	require.Error(t, err)
}

func TestNilLifecycleHasNoRouterFeedbackStore(t *testing.T) {
	var lifecycle *conversation.Lifecycle
	assert.Nil(t, lifecycle.RouterFeedbackStore())
}
