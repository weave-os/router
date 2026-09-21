package proxy

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"
)

type classifierMemoryStore struct {
	mu      sync.Mutex
	threads map[uuid.UUID]router.ClassifierThread
	turns   map[uuid.UUID]map[string]router.ClassifierPrediction
}

func (s *classifierMemoryStore) Create(_ context.Context, thread router.ClassifierThread) (router.ClassifierThread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, previous := range s.threads {
		if previous.InstallationID == thread.InstallationID && previous.CredentialSHA256 == thread.CredentialSHA256 && previous.RequestID == thread.RequestID {
			return previous, nil
		}
	}
	s.threads[thread.ThreadID], s.turns[thread.ThreadID] = thread, make(map[string]router.ClassifierPrediction)
	return thread, nil
}

func (s *classifierMemoryStore) WithThread(ctx context.Context, thread router.ClassifierThread, classify func(router.ClassifierTurnStore) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.threads[thread.ThreadID]
	stored.ExpiresAt, thread.ExpiresAt = stored.ExpiresAt.UTC(), thread.ExpiresAt.UTC()
	if !ok || stored != thread {
		return router.ErrClassifierThreadInvalid
	}
	turns := classifierMemoryTurns(maps.Clone(s.turns[thread.ThreadID]))
	if err := classify(turns); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.turns[thread.ThreadID] = turns
	return nil
}

type classifierMemoryTurns map[string]router.ClassifierPrediction

func (s classifierMemoryTurns) Get(_ context.Context, digest string) (router.ClassifierPrediction, bool, error) {
	prediction, found := s[digest]
	return prediction, found, nil
}
func (s classifierMemoryTurns) RootTurnDigest(context.Context) (string, error) {
	for _, prediction := range s {
		if prediction.Features.UserMessageCount == 1 {
			return prediction.TurnDigest, nil
		}
	}
	return "", nil
}
func (s classifierMemoryTurns) Insert(_ context.Context, prediction router.ClassifierPrediction) error {
	for _, previous := range s {
		if previous.Features.UserMessageCount == prediction.Features.UserMessageCount {
			return router.ErrClassifierHistoryUnavailable
		}
	}
	s[prediction.TurnDigest] = prediction
	return nil
}

type atomicClassifierFunc func(context.Context, router.AtomicClassificationRequest) (router.ClassifierPrediction, error)

func (f atomicClassifierFunc) Classify(ctx context.Context, request router.AtomicClassificationRequest) (router.ClassifierPrediction, error) {
	return f(ctx, request)
}

func classifierSessionFixture(t *testing.T, classify atomicClassifierFunc) (*Service, context.Context, *classifierMemoryStore) {
	t.Helper()
	installation := uuid.New()
	store := &classifierMemoryStore{threads: make(map[uuid.UUID]router.ClassifierThread), turns: make(map[uuid.UUID]map[string]router.ClassifierPrediction)}
	svc := &Service{now: time.Now}
	require.NoError(t, svc.WithClassifierSessions(ClassifierSessionConfig{Release: "llm-classifier-v1.0.0", ReleaseSHA256: strings.Repeat("a", 64), SelectionPolicySHA256: strings.Repeat("b", 64), SigningKey: []byte(strings.Repeat("k", 32)), InstallationIDs: []uuid.UUID{installation}}, store, classify))
	ctx := context.WithValue(context.Background(), InstallationIDContextKey{}, installation.String())
	ctx = context.WithValue(ctx, APIKeyIDContextKey{}, "test-credential")
	return svc, ctx, store
}

func classifierMedium(context.Context, router.AtomicClassificationRequest) (router.ClassifierPrediction, error) {
	return router.ClassifierPrediction{Complexity: router.ClassifierMedium, Probabilities: []float64{0.1, 0.7, 0.1, 0.1}}, nil
}

func classifierAdmit(t *testing.T, svc *Service, ctx context.Context) context.Context {
	t.Helper()
	ticket, err := svc.StartClassifierThread(ctx, uuid.New())
	require.NoError(t, err)
	admitted, err := svc.AdmitClassifierThread(ctx, ticket)
	require.NoError(t, err)
	return admitted
}

func TestClassifierHandshakeAuthenticationAndIdempotency(t *testing.T) {
	svc, ctx, _ := classifierSessionFixture(t, classifierMedium)
	legacy := router.WithStrategy(ctx, router.StrategyHMMEmbedding)
	unchanged, err := svc.AdmitClassifierThread(legacy, "")
	require.NoError(t, err)
	require.Equal(t, legacy, unchanged)
	id := uuid.New()
	ticket, err := svc.StartClassifierThread(ctx, id)
	require.NoError(t, err)
	replayed, err := svc.StartClassifierThread(ctx, id)
	require.NoError(t, err)
	require.Equal(t, ticket, replayed)
	admitted, err := svc.AdmitClassifierThread(ctx, ticket)
	require.NoError(t, err)
	require.Equal(t, router.StrategyLLMClassifier, router.StrategyFromContext(admitted))
	child := classifierAdmit(t, svc, ctx)
	require.NotEqual(t, deriveSessionKeyForRequest(admitted, nil, "test-credential"), deriveSessionKeyForRequest(child, nil, "test-credential"))
	for _, foreign := range []context.Context{
		context.WithValue(ctx, InstallationIDContextKey{}, uuid.NewString()),
		context.WithValue(ctx, APIKeyIDContextKey{}, "other-credential"),
		requestcontext.WithServingIdentity(ctx, requestcontext.ServingIdentity{ReleaseID: "managed-release"}),
	} {
		_, err := svc.AdmitClassifierThread(foreign, ticket)
		require.Error(t, err)
	}
	_, err = svc.AdmitClassifierThread(ctx, ticket+"invalid")
	require.ErrorIs(t, err, router.ErrClassifierThreadInvalid)
	svc.now = func() time.Time { return time.Now().Add(31 * 24 * time.Hour) }
	_, err = svc.AdmitClassifierThread(ctx, ticket)
	require.ErrorIs(t, err, router.ErrClassifierThreadInvalid)
}

func TestClassifierThreadReleaseCannotDrift(t *testing.T) {
	svc, ctx, _ := classifierSessionFixture(t, classifierMedium)
	id := uuid.New()
	ticket, err := svc.StartClassifierThread(ctx, id)
	require.NoError(t, err)
	svc.classifierSessions.config.ReleaseSHA256 = strings.Repeat("b", 64)
	_, err = svc.StartClassifierThread(ctx, id)
	require.ErrorIs(t, err, router.ErrClassifierThreadInvalid)
	_, err = svc.AdmitClassifierThread(ctx, ticket)
	require.ErrorIs(t, err, router.ErrClassifierThreadInvalid)
	svc.classifierSessions.config.ReleaseSHA256 = strings.Repeat("a", 64)
	svc.classifierSessions.config.SelectionPolicySHA256 = strings.Repeat("c", 64)
	_, err = svc.StartClassifierThread(ctx, id)
	require.ErrorIs(t, err, router.ErrClassifierThreadInvalid)
	_, err = svc.AdmitClassifierThread(ctx, ticket)
	require.ErrorIs(t, err, router.ErrClassifierThreadInvalid)
}

func TestClassifierThreadTicketRoundTripAcrossTimeZones(t *testing.T) {
	for _, zone := range []*time.Location{time.FixedZone("UTC clock", 0), time.FixedZone("offset clock", -7*60*60)} {
		t.Run(zone.String(), func(t *testing.T) {
			svc, ctx, _ := classifierSessionFixture(t, classifierMedium)
			now := time.Now().In(zone)
			svc.now = func() time.Time { return now }
			ctx = classifierAdmit(t, svc, ctx)
			input, err := classifierContextAtUserBoundary(classifierTestObservation(classifierTestText(translate.EscalationRoleUser, "first")))
			require.NoError(t, err)
			prediction, err := svc.classifyThread(ctx, input)
			require.NoError(t, err)
			require.Equal(t, router.ClassifierMedium, prediction.Complexity)
		})
	}
}

func TestClassifierPredictionsSurviveRetriesReplicasAndToolLoops(t *testing.T) {
	var requests []router.AtomicClassificationRequest
	svc, ctx, store := classifierSessionFixture(t, func(ctx context.Context, input router.AtomicClassificationRequest) (router.ClassifierPrediction, error) {
		requests = append(requests, input)
		return classifierMedium(ctx, input)
	})
	ctx = classifierAdmit(t, svc, ctx)
	first := classifierTestText(translate.EscalationRoleUser, "first")
	input, err := classifierContextAtUserBoundary(classifierTestObservation(first))
	require.NoError(t, err)
	var group sync.WaitGroup
	errorsCh := make(chan error, 8)
	for range 8 {
		group.Add(1)
		go func() { defer group.Done(); _, err := svc.classifyThread(ctx, input); errorsCh <- err }()
	}
	group.Wait()
	close(errorsCh)
	for err := range errorsCh {
		require.NoError(t, err)
	}
	require.Len(t, requests, 1)
	loop := classifierTestObservation(first,
		translate.EscalationMessage{Role: translate.EscalationRoleAssistant, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockText, Text: ""}, {Type: translate.EscalationBlockToolCall, ID: "call", Name: "test"}}},
		translate.EscalationMessage{Role: translate.EscalationRoleTool, Blocks: []translate.EscalationBlock{{Type: translate.EscalationBlockToolResult, CallID: "call", IsError: func() *bool { b := true; return &b }()}}},
	)
	input, err = classifierContextAtUserBoundary(loop)
	require.NoError(t, err)
	replica := &Service{now: time.Now}
	require.NoError(t, replica.WithClassifierSessions(svc.classifierSessions.config, store, svc.classifierSessions.classifier))
	prediction, err := replica.classifyThread(ctx, input)
	require.NoError(t, err)
	require.Equal(t, router.ClassifierMedium, prediction.Complexity)
	require.Len(t, requests, 1)
	loop.Messages = append(loop.Messages, classifierTestText(translate.EscalationRoleUser, "next"))
	input, err = classifierContextAtUserBoundary(loop)
	require.NoError(t, err)
	_, err = replica.classifyThread(ctx, input)
	require.NoError(t, err)
	require.Len(t, requests, 2)
	require.Equal(t, router.ClassifierFeatures{UserMessageCount: 2, ToolCallCount: 1, ToolErrorCount: 1}, requests[1].User.Features)
	require.Equal(t, []router.PredictedClassifierResponse{{ResponseIndex: 0, Content: "", Complexity: router.ClassifierMedium}}, requests[1].User.PrecedingResponses)
	compacted, err := classifierContextAtUserBoundary(classifierTestObservation(classifierTestText(translate.EscalationRoleUser, "summary")))
	require.NoError(t, err)
	_, err = replica.classifyThread(ctx, compacted)
	require.ErrorIs(t, err, router.ErrClassifierHistoryUnavailable)
	require.Len(t, requests, 2, "compaction must fail before inference")
}

func TestClassifierLastTenAtomicResponsesUseHistoricalPredictions(t *testing.T) {
	var requests []router.AtomicClassificationRequest
	svc, ctx, _ := classifierSessionFixture(t, func(ctx context.Context, input router.AtomicClassificationRequest) (router.ClassifierPrediction, error) {
		requests = append(requests, input)
		return classifierMedium(ctx, input)
	})
	ctx = classifierAdmit(t, svc, ctx)
	observation := classifierTestObservation(classifierTestText(translate.EscalationRoleUser, "first"))
	for turn := 0; turn < 13; turn++ {
		input, err := classifierContextAtUserBoundary(observation)
		require.NoError(t, err)
		_, err = svc.classifyThread(ctx, input)
		require.NoError(t, err)
		observation.Messages = append(observation.Messages, classifierTestText(translate.EscalationRoleAssistant, fmt.Sprintf("answer-%d", turn)), classifierTestText(translate.EscalationRoleUser, fmt.Sprintf("user-%d", turn+1)))
	}
	last := requests[12]
	require.Equal(t, 13, last.User.Features.UserMessageCount)
	require.Equal(t, 12, last.CompletedResponseCount)
	require.Len(t, last.User.PrecedingResponses, 10)
	require.Equal(t, "answer-2", last.User.PrecedingResponses[0].Content)
	require.Equal(t, 11, last.User.PrecedingResponses[9].ResponseIndex)
}

func TestClassifierImportedHistoryAndFailedInferenceNeverCommit(t *testing.T) {
	failure := errors.New("unavailable")
	svc, ctx, store := classifierSessionFixture(t, func(context.Context, router.AtomicClassificationRequest) (router.ClassifierPrediction, error) {
		return router.ClassifierPrediction{}, failure
	})
	ctx = classifierAdmit(t, svc, ctx)
	observation := classifierTestObservation(classifierTestText(translate.EscalationRoleUser, "first"))
	input, err := classifierContextAtUserBoundary(observation)
	require.NoError(t, err)
	_, err = svc.classifyThread(ctx, input)
	require.ErrorIs(t, err, failure)
	for _, turns := range store.turns {
		require.Empty(t, turns)
	}
	observation.Messages = append(observation.Messages, classifierTestText(translate.EscalationRoleAssistant, "previous"), classifierTestText(translate.EscalationRoleUser, "second"))
	input, err = classifierContextAtUserBoundary(observation)
	require.NoError(t, err)
	_, err = svc.classifyThread(ctx, input)
	require.ErrorIs(t, err, router.ErrClassifierHistoryUnavailable)
}
