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
	mu                sync.Mutex
	threads           map[uuid.UUID]router.ClassifierThread
	turns             map[uuid.UUID]map[string]router.ClassifierPrediction
	prefixCheckpoints map[uuid.UUID]router.ClassifierPrefixCheckpoint
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
	turns := &classifierMemoryTurns{predictions: maps.Clone(s.turns[thread.ThreadID]), prefixCheckpoint: s.prefixCheckpoints[thread.ThreadID]}
	if err := classify(turns); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.turns[thread.ThreadID] = turns.predictions
	s.prefixCheckpoints[thread.ThreadID] = turns.prefixCheckpoint
	return nil
}

type classifierMemoryTurns struct {
	predictions      map[string]router.ClassifierPrediction
	prefixCheckpoint router.ClassifierPrefixCheckpoint
}

func (s *classifierMemoryTurns) PrefixCheckpoint() router.ClassifierPrefixCheckpoint {
	return s.prefixCheckpoint
}

func (s *classifierMemoryTurns) SetPrefixCheckpoint(_ context.Context, prefixCheckpoint router.ClassifierPrefixCheckpoint) error {
	s.prefixCheckpoint = prefixCheckpoint
	return nil
}

func (s classifierMemoryTurns) Get(_ context.Context, digest string) (router.ClassifierPrediction, bool, error) {
	prediction, found := s.predictions[digest]
	return prediction, found, nil
}
func (s classifierMemoryTurns) PredictionBeforeMessage(_ context.Context, messageIndex int) (router.ClassifierPrediction, bool, error) {
	var latest router.ClassifierPrediction
	for _, prediction := range s.predictions {
		if prediction.InputMessageCount <= messageIndex && prediction.InputMessageCount > latest.InputMessageCount {
			latest = prediction
		}
	}
	return latest, latest.InputMessageCount > 0, nil
}
func (s classifierMemoryTurns) RootTurnDigest(context.Context) (string, error) {
	for _, prediction := range s.predictions {
		if prediction.TurnDigest == prediction.RootTurnDigest {
			return prediction.TurnDigest, nil
		}
	}
	return "", nil
}
func (s classifierMemoryTurns) Insert(_ context.Context, prediction router.ClassifierPrediction) error {
	for _, previous := range s.predictions {
		if previous.InputMessageCount == prediction.InputMessageCount || previous.TurnDigest == prediction.TurnDigest {
			return router.ErrClassifierHistoryUnavailable
		}
	}
	s.predictions[prediction.TurnDigest] = prediction
	return nil
}

type atomicClassifierFunc func(context.Context, router.AtomicClassificationRequest) (router.ClassifierPrediction, error)

func (f atomicClassifierFunc) Classify(ctx context.Context, request router.AtomicClassificationRequest) (router.ClassifierPrediction, error) {
	return f(ctx, request)
}

func classifierSessionFixture(t *testing.T, classify atomicClassifierFunc) (*Service, context.Context, *classifierMemoryStore) {
	t.Helper()
	installation := uuid.New()
	store := &classifierMemoryStore{threads: make(map[uuid.UUID]router.ClassifierThread), turns: make(map[uuid.UUID]map[string]router.ClassifierPrediction), prefixCheckpoints: make(map[uuid.UUID]router.ClassifierPrefixCheckpoint)}
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
			input, err := classifierContextForCall(classifierTestObservation(classifierTestText(translate.EscalationRoleUser, "first")))
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
	input, err := classifierContextForCall(classifierTestObservation(first))
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
	input, err = classifierContextForCall(loop)
	require.NoError(t, err)
	replica := &Service{now: time.Now}
	require.NoError(t, replica.WithClassifierSessions(svc.classifierSessions.config, store, svc.classifierSessions.classifier))
	prediction, err := replica.classifyThread(ctx, input)
	require.NoError(t, err)
	require.Equal(t, router.ClassifierMedium, prediction.Complexity)
	require.Len(t, requests, 2)
	require.Equal(t, router.ClassifierFeatures{UserMessageCount: 1, ToolCallCount: 1, ToolErrorCount: 1}, requests[1].User.Features)
	require.Equal(t, []router.PredictedClassifierResponse{{ResponseIndex: 0, Content: "", Complexity: router.ClassifierMedium}}, requests[1].User.PrecedingResponses)
	loop.Messages = append(loop.Messages, classifierTestText(translate.EscalationRoleUser, "next"))
	input, err = classifierContextForCall(loop)
	require.NoError(t, err)
	_, err = replica.classifyThread(ctx, input)
	require.NoError(t, err)
	require.Len(t, requests, 3)
	require.Equal(t, router.ClassifierFeatures{UserMessageCount: 2, ToolCallCount: 1, ToolErrorCount: 1}, requests[2].User.Features)
	require.Equal(t, []router.PredictedClassifierResponse{{ResponseIndex: 0, Content: "", Complexity: router.ClassifierMedium}}, requests[2].User.PrecedingResponses)
	compacted, err := classifierContextForCall(classifierTestObservation(classifierTestText(translate.EscalationRoleUser, "summary")))
	require.NoError(t, err)
	_, err = replica.classifyThread(ctx, compacted)
	require.ErrorIs(t, err, router.ErrClassifierHistoryUnavailable)
	require.Len(t, requests, 3, "compaction must fail before inference")
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
		input, err := classifierContextForCall(observation)
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
	input, err := classifierContextForCall(observation)
	require.NoError(t, err)
	_, err = svc.classifyThread(ctx, input)
	require.ErrorIs(t, err, failure)
	for _, turns := range store.turns {
		require.Empty(t, turns)
	}
	observation.Messages = append(observation.Messages, classifierTestText(translate.EscalationRoleAssistant, "previous"), classifierTestText(translate.EscalationRoleUser, "second"))
	input, err = classifierContextForCall(observation)
	require.NoError(t, err)
	_, err = svc.classifyThread(ctx, input)
	require.ErrorIs(t, err, router.ErrClassifierHistoryUnavailable)
}

func TestClassifierNativeInputGroupsPersistExactBoundaries(t *testing.T) {
	fixtures := []struct {
		name      string
		parse     func([]byte) (*translate.RequestEnvelope, error)
		body      string
		userText  string
		userCount int
	}{
		{"messages trailing instructions", translate.ParseAnthropic,
			`{"system":"base instructions","messages":[{"role":"user","content":[{"type":"text","text":"workspace context"},{"type":"text","text":"first request"}]},{"role":"system","content":"turn instructions"}]}`,
			"workspace context\n\nfirst request", 1},
		{"responses setup message", translate.ParseOpenAI,
			`{"instructions":"base instructions","input":[{"role":"developer","content":"CLI instructions"},{"role":"user","content":"workspace context"},{"role":"user","content":"first request"}]}`,
			"workspace context\n\nfirst request", 2},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			var classificationRequests []router.AtomicClassificationRequest
			svc, ctx, store := classifierSessionFixture(t, func(ctx context.Context, input router.AtomicClassificationRequest) (router.ClassifierPrediction, error) {
				classificationRequests = append(classificationRequests, input)
				return classifierMedium(ctx, input)
			})
			ctx = classifierAdmit(t, svc, ctx)
			envelope, err := fixture.parse([]byte(fixture.body))
			require.NoError(t, err)
			observation, err := envelope.EscalationObservation()
			require.NoError(t, err)
			first, err := classifierContextForCall(observation)
			require.NoError(t, err)
			require.True(t, first.AtUserBoundary)
			require.Equal(t, fixture.userText, first.CurrentUserMessage)
			require.Equal(t, fixture.userCount, first.Features.UserMessageCount)
			require.Equal(t, first.TurnDigest, first.RootTurnDigest)
			for range 2 {
				_, err = svc.classifyThread(ctx, first)
				require.NoError(t, err)
			}
			require.Len(t, classificationRequests, 1)
			require.Empty(t, classificationRequests[0].User.PrecedingResponses)

			failed := true
			observation.Messages = append(observation.Messages,
				translate.EscalationMessage{Role: translate.EscalationRoleAssistant, Blocks: []translate.EscalationBlock{
					{Type: translate.EscalationBlockText, Text: ""},
					{Type: translate.EscalationBlockToolCall, ID: "call", Name: "test"},
				}},
				translate.EscalationMessage{Role: translate.EscalationRoleTool, Blocks: []translate.EscalationBlock{
					{Type: translate.EscalationBlockToolResult, CallID: "call", IsError: &failed},
				}},
				classifierTestText(translate.EscalationRoleSystem, "tool-loop instructions"),
			)
			loop, err := classifierContextForCall(observation)
			require.NoError(t, err)
			require.False(t, loop.AtUserBoundary)
			require.NotEqual(t, first.TurnDigest, loop.TurnDigest)
			replica := &Service{now: time.Now}
			require.NoError(t, replica.WithClassifierSessions(svc.classifierSessions.config, store, svc.classifierSessions.classifier))
			_, err = replica.classifyThread(ctx, loop)
			require.NoError(t, err)
			require.Len(t, classificationRequests, 2)
			instructionIndex := len(observation.Messages) - 1
			observation.Messages[instructionIndex].Blocks[0].Text = "rewritten tool-loop instructions"
			rewritten, err := classifierContextForCall(observation)
			require.NoError(t, err)
			_, err = replica.classifyThread(ctx, rewritten)
			require.ErrorIs(t, err, router.ErrClassifierHistoryUnavailable)
			observation.Messages[instructionIndex].Blocks[0].Text = "tool-loop instructions"
			// Appending instructions is a new call; reverting the committed
			// causal fence must not be accepted later.
			observation.Messages = append(observation.Messages, classifierTestText(translate.EscalationRoleDeveloper, "additional tool-loop instructions"))
			extended, err := classifierContextForCall(observation)
			require.NoError(t, err)
			_, err = replica.classifyThread(ctx, extended)
			require.NoError(t, err)
			_, err = replica.classifyThread(ctx, loop)
			require.ErrorIs(t, err, router.ErrClassifierHistoryUnavailable)
			require.Len(t, classificationRequests, 3)

			observation.Messages = append(observation.Messages,
				classifierTestText(translate.EscalationRoleUser, "follow-up context"),
				classifierTestText(translate.EscalationRoleDeveloper, "follow-up instructions"),
				classifierTestText(translate.EscalationRoleUser, "second request"),
				classifierTestText(translate.EscalationRoleSystem, "final instructions"),
			)
			next, err := classifierContextForCall(observation)
			require.NoError(t, err)
			require.True(t, next.AtUserBoundary)
			_, err = replica.classifyThread(ctx, next)
			require.NoError(t, err)
			require.Len(t, classificationRequests, 4)
			require.Equal(t, "follow-up context\n\nsecond request", classificationRequests[3].User.CurrentUserMessage)
			require.Equal(t, router.ClassifierFeatures{UserMessageCount: fixture.userCount + 2, ToolCallCount: 1, ToolErrorCount: 1}, classificationRequests[3].User.Features)
			require.Equal(t, []router.PredictedClassifierResponse{{ResponseIndex: 0, Content: "", Complexity: router.ClassifierMedium}}, classificationRequests[3].User.PrecedingResponses)

			// A new ticket cannot retrospectively invent either prediction.
			_, err = svc.classifyThread(classifierAdmit(t, svc, ctx), next)
			require.ErrorIs(t, err, router.ErrClassifierHistoryUnavailable)
			for _, index := range []int{0, len(observation.Messages) - 1} {
				original := observation.Messages[index].Blocks[0].Text
				observation.Messages[index].Blocks[0].Text = "rewritten instructions"
				changed, err := classifierContextForCall(observation)
				require.NoError(t, err)
				_, err = replica.classifyThread(ctx, changed)
				require.ErrorIs(t, err, router.ErrClassifierHistoryUnavailable)
				observation.Messages[index].Blocks[0].Text = original
			}
			require.Len(t, classificationRequests, 4, "a conflicting request must fail before inference")
			_, err = replica.classifyThread(ctx, next)
			require.NoError(t, err)
			require.Len(t, classificationRequests, 4)
		})
	}
}

func TestClassifierClaudeTransientToolHintReplay(t *testing.T) {
	for _, budget := range []string{
		"<total_tokens>14999980 tokens left</total_tokens>",
		"<total_tokens>14999980 tokens left</total_tokens>\n\nUSD budget: $0.1537425/$2; $1.8462575 remaining",
		"Keep the user updated.\n\n<total_tokens>14999980 tokens left</total_tokens>",
		"Keep the user updated.\n\n<total_tokens>14999980 tokens left</total_tokens>\n\nUSD budget: $0.15/$2; $1.85 remaining",
	} {
		t.Run(budget, func(t *testing.T) {
			svc, ctx, _ := classifierSessionFixture(t, classifierMedium)
			ctx = classifierAdmit(t, svc, ctx)
			observation := classifierTestObservation(classifierTestText(translate.EscalationRoleUser, "first"))
			first, err := classifierContextForCall(observation)
			require.NoError(t, err)
			_, err = svc.classifyThread(ctx, first)
			require.NoError(t, err)
			observation.Messages = append(observation.Messages,
				classifierTestText(translate.EscalationRoleAssistant, "intermediate"),
				translate.EscalationMessage{Role: translate.EscalationRoleSystem, Blocks: []translate.EscalationBlock{
					{Type: translate.EscalationBlockText, Text: budget},
					{Type: translate.EscalationBlockText, Text: classifierClaudeToolHint},
				}},
			)
			loop, err := classifierContextForCall(observation)
			require.NoError(t, err)
			_, err = svc.classifyThread(ctx, loop)
			require.NoError(t, err)
			require.Len(t, observation.Messages[2].Blocks, 2, "provider input must not change")
			observation.Messages[2].Blocks = observation.Messages[2].Blocks[:1]
			observation.Messages = append(observation.Messages,
				classifierTestText(translate.EscalationRoleAssistant, "answer"),
				classifierTestText(translate.EscalationRoleUser, "next"),
				classifierTestText(translate.EscalationRoleSystem, "<total_tokens>15000000 tokens left</total_tokens>\n\nUSD budget: $0/$2; $2 remaining"),
			)
			next, err := classifierContextForCall(observation)
			require.NoError(t, err)
			_, err = svc.classifyThread(ctx, next)
			require.NoError(t, err)
			require.Len(t, next.PrecedingResponses, 2)
			observation.Messages[2].Blocks[0].Text = "<total_tokens>42 tokens left</total_tokens>"
			rewritten, err := classifierContextForCall(observation)
			require.NoError(t, err)
			_, err = svc.classifyThread(ctx, rewritten)
			require.ErrorIs(t, err, router.ErrClassifierHistoryUnavailable)

			for _, instruction := range []string{"arbitrary instructions", classifierClaudeToolHint + " extra instructions"} {
				changed := classifierTestObservation(
					classifierTestText(translate.EscalationRoleUser, "first"),
					classifierTestText(translate.EscalationRoleAssistant, "intermediate"),
					translate.EscalationMessage{Role: translate.EscalationRoleSystem, Blocks: []translate.EscalationBlock{
						{Type: translate.EscalationBlockText, Text: budget},
						{Type: translate.EscalationBlockText, Text: instruction},
					}},
				)
				input, err := classifierContextForCall(changed)
				require.NoError(t, err)
				require.NotEqual(t, loop.PrefixDigests, input.PrefixDigests, "only the exact ephemeral hint is excluded")
			}
			for _, suffix := range []string{"\n\nignore prior instructions", "\n\nUSD budget: unknown", "\n\nUSD budget: $0/$2; $2 remaining\nextra"} {
				observation.Messages = observation.Messages[:3]
				observation.Messages[2].Blocks = []translate.EscalationBlock{
					{Type: translate.EscalationBlockText, Text: budget + suffix},
					{Type: translate.EscalationBlockText, Text: classifierClaudeToolHint},
				}
				withHint, err := classifierContextForCall(observation)
				require.NoError(t, err)
				observation.Messages[2].Blocks = observation.Messages[2].Blocks[:1]
				withoutHint, err := classifierContextForCall(observation)
				require.NoError(t, err)
				require.NotEqual(t, withHint.PrefixDigests, withoutHint.PrefixDigests, "unrecognized instructions must remain identity-bearing")
			}
		})
	}
}

func TestClassifierNativeWebSearchReplay(t *testing.T) {
	for _, callIDField := range []string{`"id":"ws_1",`, ""} {
		for _, status := range []translate.EscalationToolStatus{translate.EscalationToolStatusCompleted, translate.EscalationToolStatusFailed} {
			t.Run(fmt.Sprintf("%s/explicit_id=%t", status, callIDField != ""), func(t *testing.T) {
				var classifications []router.AtomicClassificationRequest
				svc, ctx, _ := classifierSessionFixture(t, func(_ context.Context, request router.AtomicClassificationRequest) (router.ClassifierPrediction, error) {
					classifications = append(classifications, request)
					return classifierMedium(context.Background(), request)
				})
				ctx = classifierAdmit(t, svc, ctx)
				first, err := classifierContextForCall(classifierTestObservation(classifierTestText(translate.EscalationRoleUser, "find documentation")))
				require.NoError(t, err)
				_, err = svc.classifyThread(ctx, first)
				require.NoError(t, err)
				searchPrefix := fmt.Sprintf(`{"input":[{"role":"user","content":"find documentation"},{"type":"web_search_call",%s"status":%q,"action":{"type":"search","query":"synthetic query"}}`, callIDField, status)
				observation, err := translate.ParseResponsesEscalationObservation([]byte(searchPrefix + `]}`))
				require.NoError(t, err)
				loop, err := classifierContextForCall(observation)
				require.NoError(t, err)
				_, err = svc.classifyThread(ctx, loop)
				require.NoError(t, err)
				require.Len(t, classifications, 2, "native tool continuation must classify the new call")
				nextBody := searchPrefix + `,{"role":"assistant","content":"documentation summary"},{"role":"user","content":"next question"}]}`
				observation, err = translate.ParseResponsesEscalationObservation([]byte(nextBody))
				require.NoError(t, err)
				next, err := classifierContextForCall(observation)
				require.NoError(t, err)
				_, err = svc.classifyThread(ctx, next)
				require.NoError(t, err)
				require.Len(t, classifications, 3)
				toolErrors := 0
				if status == translate.EscalationToolStatusFailed {
					toolErrors = 1
				}
				require.Equal(t, router.ClassifierFeatures{UserMessageCount: 1, ToolCallCount: 1, ToolErrorCount: toolErrors}, classifications[1].User.Features)
				require.Equal(t, router.ClassifierFeatures{UserMessageCount: 2, ToolCallCount: 1, ToolErrorCount: toolErrors}, classifications[2].User.Features)
				require.Equal(t, []router.PredictedClassifierResponse{{ResponseIndex: 0, Content: "documentation summary", Complexity: router.ClassifierMedium}}, classifications[2].User.PrecedingResponses)
				changedIDBody := strings.Replace(nextBody, "ws_1", "ws_2", 1)
				if callIDField == "" {
					changedIDBody = strings.Replace(nextBody, `"status":`, `"id":"ws_2","status":`, 1)
				}
				for _, rewritten := range []string{
					strings.Replace(nextBody, "synthetic query", "rewritten query", 1),
					changedIDBody,
				} {
					observation, err = translate.ParseResponsesEscalationObservation([]byte(rewritten))
					require.NoError(t, err)
					changed, err := classifierContextForCall(observation)
					require.NoError(t, err)
					_, err = svc.classifyThread(ctx, changed)
					require.ErrorIs(t, err, router.ErrClassifierHistoryUnavailable)
				}
				require.Len(t, classifications, 3, "rewritten native calls fail before inference")
			})
		}
	}
}
