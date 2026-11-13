package proxy

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/sjson"

	"weave-os/router/internal/router"
)

const classifierSearchBody = `{"model":"auto","max_tokens":1024,"system":[{"type":"text","text":"Synthetic SDK instructions"},{"type":"text","text":"You are an assistant for performing a web search tool use"}],"messages":[{"role":"user","content":"Perform a web search for the query: synthetic language documentation"}],"tools":[{"type":"web_search_20250305","name":"web_search"}],"tool_choice":{"type":"auto"}}`

func TestClassifierSearchChildIsolationAndReplay(t *testing.T) {
	var inputs []router.AtomicClassificationRequest
	svc, principal, store := classifierSessionFixture(t, func(ctx context.Context, input router.AtomicClassificationRequest) (router.ClassifierPrediction, error) {
		inputs = append(inputs, input)
		return classifierMedium(ctx, input)
	})
	parent := classifierAdmit(t, svc, principal)
	parentThread := parent.Value(classifierThreadContextKey{}).(router.ClassifierThread)
	classify := func(ctx context.Context, body string) context.Context {
		t.Helper()
		captured, err := svc.withClassifierInput(ctx, []byte(body), router.EndpointAnthropicMessages)
		require.NoError(t, err)
		_, err = svc.classifyThread(captured, captured.Value(classifierInputContextKey{}).(router.ClassifierContext))
		require.NoError(t, err)
		return captured
	}
	const first = `{"messages":[{"role":"user","content":"Synthetic parent request"}]}`
	const resumed = `{"messages":[{"role":"user","content":"Synthetic parent request"},{"role":"assistant","content":"Synthetic answer"},{"role":"user","content":"Synthetic follow-up"}]}`
	classify(parent, first)
	checkpoint := store.prefixCheckpoints[parentThread.ThreadID]
	child := classify(parent, classifierSearchBody)
	childThread := child.Value(classifierThreadContextKey{}).(router.ClassifierThread)
	require.NotEqual(t, parentThread.ThreadID, childThread.ThreadID)
	require.NotEqual(t, deriveSessionKeyForRequest(parent, nil, "credential"), deriveSessionKeyForRequest(child, nil, "credential"))
	require.Equal(t, parentThread.InstallationID, childThread.InstallationID)
	require.Equal(t, parentThread.CredentialSHA256, childThread.CredentialSHA256)
	require.Equal(t, parentThread.ReleaseSHA256, childThread.ReleaseSHA256)
	require.Equal(t, parentThread.SelectionPolicySHA256, childThread.SelectionPolicySHA256)
	require.True(t, parentThread.ExpiresAt.Equal(childThread.ExpiresAt))
	require.Equal(t, checkpoint, store.prefixCheckpoints[parentThread.ThreadID])
	require.Len(t, inputs, 2)
	require.Equal(t, router.ClassifierFeatures{UserMessageCount: 1}, inputs[1].User.Features)
	require.Empty(t, inputs[1].User.PrecedingResponses)

	// Nested protocol preparation must not mint a grandchild.
	nested := classify(child, classifierSearchBody)
	require.Equal(t, childThread, nested.Value(classifierThreadContextKey{}))
	classify(parent, resumed)
	require.Len(t, inputs, 3)
	require.Equal(t, router.ClassifierFeatures{UserMessageCount: 2}, inputs[2].User.Features)
	require.Len(t, inputs[2].User.PrecedingResponses, 1)
	require.Equal(t, "Synthetic answer", inputs[2].User.PrecedingResponses[0].Content)

	// Parent advancement cannot change a retry's child identity.
	replica := &Service{now: svc.now}
	require.NoError(t, replica.WithClassifierSessions(svc.classifierSessions.config, store, svc.classifierSessions.classifier))
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			captured, err := replica.withClassifierInput(parent, []byte(classifierSearchBody), router.EndpointAnthropicMessages)
			if err != nil {
				t.Errorf("capture retry: %v", err)
				return
			}
			if captured.Value(classifierThreadContextKey{}) != childThread {
				t.Error("retry changed child identity")
			}
			_, err = replica.classifyThread(captured, captured.Value(classifierInputContextKey{}).(router.ClassifierContext))
			if err != nil {
				t.Errorf("classify retry: %v", err)
			}
		})
	}
	workers.Wait()
	require.Len(t, inputs, 3)
	require.Len(t, store.threads, 2)
	differentQueryChild := classify(parent, strings.ReplaceAll(classifierSearchBody, "language documentation", "compiler documentation"))
	require.NotEqual(t, childThread.ThreadID, differentQueryChild.Value(classifierThreadContextKey{}).(router.ClassifierThread).ThreadID)
	separateParent := classifierAdmit(t, svc, principal)
	differentParentChild := classify(separateParent, classifierSearchBody)
	require.NotEqual(t, childThread.ThreadID, differentParentChild.Value(classifierThreadContextKey{}).(router.ClassifierThread).ThreadID)

	// An ordinary history reset is still rejected, even after a helper succeeds.
	captured, err := svc.withClassifierInput(parent, []byte(first), router.EndpointAnthropicMessages)
	require.NoError(t, err)
	_, err = svc.classifyThread(captured, captured.Value(classifierInputContextKey{}).(router.ClassifierContext))
	require.ErrorIs(t, err, router.ErrClassifierHistoryUnavailable)
}

func TestClassifierSearchDoesNotResetOrdinaryThreads(t *testing.T) {
	for _, test := range []struct {
		name, path string
		value      any
	}{
		{"no helper marker", "system.1.text", "Synthetic parent instructions"},
		{"quoted helper marker", "system.1.text", "Quote: You are an assistant for performing a web search tool use"},
		{"no native search", "tools.0.type", "custom"},
		{"extra tool", "tools.1", map[string]string{"name": "SyntheticTool"}},
		{"continuation", "messages.1", map[string]string{"role": "assistant", "content": "Synthetic answer"}},
		{"no search query", "messages.0.content", "Synthetic unrelated request"},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc, principal, store := classifierSessionFixture(t, classifierMedium)
			parent := classifierAdmit(t, svc, principal)
			body, err := sjson.Set(classifierSearchBody, test.path, test.value)
			require.NoError(t, err)
			captured, err := svc.withClassifierInput(parent, []byte(body), router.EndpointAnthropicMessages)
			require.NoError(t, err)
			require.Equal(t, parent.Value(classifierThreadContextKey{}), captured.Value(classifierThreadContextKey{}))
			require.Len(t, store.threads, 1)
		})
	}
}

func TestClassifierSearchRejectsMissingParentAndRetiredChild(t *testing.T) {
	svc, principal, store := classifierSessionFixture(t, classifierMedium)
	parent := classifierAdmit(t, svc, principal)
	thread := parent.Value(classifierThreadContextKey{}).(router.ClassifierThread)
	delete(store.threads, thread.ThreadID)
	_, err := svc.withClassifierInput(parent, []byte(classifierSearchBody), router.EndpointAnthropicMessages)
	require.ErrorIs(t, err, router.ErrClassifierThreadInvalid)
	require.Empty(t, store.threads)
	store.threads[thread.ThreadID] = thread
	captured, err := svc.withClassifierInput(parent, []byte(classifierSearchBody), router.EndpointAnthropicMessages)
	require.NoError(t, err)
	child := captured.Value(classifierThreadContextKey{}).(router.ClassifierThread)
	child.ReleaseSHA256 = strings.Repeat("f", 64)
	store.threads[child.ThreadID] = child
	_, err = svc.withClassifierInput(parent, []byte(classifierSearchBody), router.EndpointAnthropicMessages)
	require.ErrorIs(t, err, router.ErrClassifierThreadInvalid)
	require.Len(t, store.threads, 2)
	thread.ThreadID = uuid.New()
	_, err = svc.withClassifierInput(context.WithValue(parent, classifierThreadContextKey{}, thread), []byte(classifierSearchBody), router.EndpointAnthropicMessages)
	require.ErrorIs(t, err, router.ErrClassifierThreadInvalid)
}
