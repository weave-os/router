package proxy

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

func TestRoutingIdentityAcrossHarnessesAndProtocols(t *testing.T) {
	for _, client := range []string{ClientAppCodex, ClientAppClaudeCode, ClientAppOpencode, ClientAppCursor, ClientAppGeminiCLI, "pi", "generic"} {
		for _, protocol := range []struct {
			surface requestcontext.ConversationSurface
			body    string
			parse   func([]byte) (*translate.RequestEnvelope, error)
		}{
			{requestcontext.ConversationAnthropic, `{"messages":[{"role":"user","content":"synthetic"}]}`, translate.ParseAnthropic},
			{requestcontext.ConversationChat, `{"messages":[{"role":"system","content":"synthetic preamble"},{"role":"user","content":"synthetic"}]}`, translate.ParseOpenAI},
			{requestcontext.ConversationResponses, `{"input":[{"role":"user","content":"synthetic"}]}`, func(body []byte) (*translate.RequestEnvelope, error) {
				converted, err := translate.ConvertResponsesToChatCompletions(body)
				if err != nil {
					return nil, err
				}
				return translate.ParseOpenAI(converted.Body)
			}},
			{requestcontext.ConversationGemini, `{"contents":[{"role":"user","parts":[{"text":"synthetic"}]}]}`, translate.ParseGemini},
		} {
			t.Run(client+"/"+string(protocol.surface), func(t *testing.T) {
				headers := make(http.Header)
				headers.Set("X-App", client)
				headers.Set("Session-Id", "root")
				ctx := requestcontext.WithClientIdentity(context.Background(), ClientIdentityFromHeaders(headers))
				env, err := protocol.parse([]byte(protocol.body))
				require.NoError(t, err)
				require.False(t, automaticStateScopeForRequest(ctx, env, "credential").Persistent)
				body := []byte(`{"weave_routing":{"conversation_id":"root","thread_id":"child","parent_thread_id":"parent"},` + protocol.body[1:])
				ctx, clean, err := withRequestRoutingIdentity(ctx, headers, body)
				require.NoError(t, err)
				require.False(t, gjson.GetBytes(clean, requestcontext.RoutingIdentityField).Exists())
				env, err = protocol.parse(clean)
				require.NoError(t, err)
				scope := automaticStateScopeForRequest(ctx, env, "credential")
				require.True(t, scope.Persistent)
				expected := requestcontext.ThreadIdentity{ConversationID: "root", ThreadID: "child", Source: requestcontext.ThreadSourceExplicit, Status: requestcontext.ThreadIdentityAvailable}.AutomaticScope("credential", uuid.Nil.String())
				require.Equal(t, expected.Key, scope.Key)
			})
		}
	}
}

func TestBodyConversationWithoutThreadOverridesLegacySession(t *testing.T) {
	for _, legacySession := range []string{"", "legacy-root"} {
		t.Run(legacySession, func(t *testing.T) {
			headers := make(http.Header)
			if legacySession != "" {
				headers.Set("Session-Id", legacySession)
			}
			ctx := requestcontext.WithClientIdentity(context.Background(), ClientIdentityFromHeaders(headers))
			ctx, clean, err := withRequestRoutingIdentity(ctx, headers, []byte(`{"weave_routing":{"conversation_id":"body-root"}}`))
			require.NoError(t, err)
			require.JSONEq(t, `{}`, string(clean))
			require.Equal(t, "body-root", clientSessionIDForRequest(ctx, nil))
			require.Equal(t, "body-root", ClientIdentityFrom(ctx).SessionID)
			require.Equal(t, requestcontext.ThreadIdentityMissing, requestcontext.ThreadIdentityFromContext(ctx).Status)
			require.False(t, automaticStateScopeForRequest(ctx, nil, "credential").Persistent)
		})
	}
}

func TestStatelessRoutingAndDetachedUsageDoNotAccessAutomaticStore(t *testing.T) {
	env, err := translate.ParseAnthropic([]byte(`{"model":"claude-sonnet-4-6","max_tokens":4096,"messages":[{"role":"user","content":"Implement synthetic feature"}]}`))
	require.NoError(t, err)
	store := newStubPinStore()
	scorer := &tierProbeRouter{available: map[string]struct{}{catalog.ModelIDClaudeHaiku45.String(): {}}}
	svc := NewService(scorer, nil, nil, false, nil, store, false, providers.ProviderAnthropic, catalog.ModelIDClaudeHaiku45.String(), nil)
	ctx := context.Background()
	installation := uuid.New()
	features := env.RoutingFeatures(false)
	for range 2 {
		route, err := svc.runTurnLoop(ctx, env, features, "credential", installation, "", nil, router.Request{RequestedModel: features.Model})
		require.NoError(t, err)
		require.Equal(t, catalog.ModelIDClaudeHaiku45.String(), route.Decision.Model)
		require.Zero(t, route.SessionKey)
		svc.recordTurnUsage(ctx, route, route.Decision.Provider, route.Decision.Model, 100, 20, 0, 0, false)
		svc.recordHMMTurnHistory(ctx, route, route.Decision.Provider, route.Decision.Model, 100, 20, 0, 0, false)
		require.False(t, svc.compaction.checkAndRecord(route.SessionKey, installation, route.PinRole, 1, 0))
	}
	require.Empty(t, store.getRoles)
	require.Empty(t, store.upserts)
	require.Zero(t, store.usageHits)
	_, persistent := noProgressBucketKey([16]byte{}, installation, sessionpin.DefaultRole)
	require.False(t, persistent)
}

func TestForeignNativeHeadersDoNotOverrideExplicitIdentity(t *testing.T) {
	headers := make(http.Header)
	headers.Set("X-App", ClientAppCodex)
	headers.Set(requestcontext.OpenCodeSessionHeader, "foreign-session")
	ctx := requestcontext.WithClientIdentity(context.Background(), ClientIdentityFromHeaders(headers))
	ctx, _, err := withRequestRoutingIdentity(ctx, headers, []byte(`{}`))
	require.NoError(t, err)
	require.False(t, automaticStateScopeForRequest(ctx, nil, "credential").Persistent)
	body := []byte(`{"weave_routing":{"conversation_id":"root","thread_id":"actual-thread"}}`)
	ctx = requestcontext.WithClientIdentity(context.Background(), ClientIdentityFromHeaders(headers))
	ctx, _, err = withRequestRoutingIdentity(ctx, headers, body)
	require.NoError(t, err)
	require.Equal(t, "actual-thread", requestcontext.ThreadIdentityFromContext(ctx).ThreadID)
	require.True(t, automaticStateScopeForRequest(ctx, nil, "credential").Persistent)
}

func TestChildForceParentTargetsOnlyIssuingThread(t *testing.T) {
	ctx := statefulTestContext()
	store := newForceModelMapStore()
	svc := &Service{pinStore: store}
	installation := uuid.New()
	childCtx := withTestThread(ctx, "issuing-child")
	childKey := deriveSessionKeyForRequest(childCtx, nil, "credential")
	controlKey := deriveForceModelSessionKeyForRequest(childCtx, nil, "credential", childKey)
	_, ack, err := svc.applyForceModelCommand(childCtx, translate.ForceModelResult{Model: catalog.ModelIDClaudeSonnet46.String(), ParentOnly: true}, installation, childKey, controlKey)
	require.NoError(t, err)
	require.Equal(t, translate.CommandAccepted, ack.outcome.Disposition)
	for _, thread := range []string{"parent", "issuing-child", "grandchild", "sibling"} {
		threadCtx := withTestThread(ctx, thread)
		key := deriveSessionKeyForRequest(threadCtx, nil, "credential")
		_, forced, _ := svc.loadForceModelSessionPin(threadCtx, controlKey, key)
		require.Equal(t, thread == "issuing-child", forced)
	}
	_, ack, err = svc.applyForceModelCommand(context.Background(), translate.ForceModelResult{Model: catalog.ModelIDClaudeHaiku45.String(), ParentOnly: true}, installation, [16]byte{}, controlKey)
	require.NoError(t, err)
	require.Equal(t, translate.CommandRejected, ack.outcome.Disposition)
	_, forced, _ := svc.loadForceModelSessionPin(childCtx, controlKey, childKey)
	require.True(t, forced)
}

func TestConversationForceWorksWithoutAutomaticIdentity(t *testing.T) {
	ctx := requestcontext.WithClientIdentity(context.Background(), ClientIdentity{SessionID: "synthetic-root"})
	store := newForceModelMapStore()
	svc := &Service{pinStore: store}
	installation := uuid.New()
	controlKey := deriveForceModelSessionKeyForRequest(ctx, nil, "credential", [16]byte{})
	_, _, err := svc.applyForceModelCommand(ctx, translate.ForceModelResult{Model: catalog.ModelIDClaudeSonnet46.String()}, installation, [16]byte{}, controlKey)
	require.NoError(t, err)
	env, err := translate.ParseAnthropic([]byte(`{"model":"claude-sonnet-4-6","max_tokens":4096,"messages":[{"role":"user","content":"synthetic descendant"}]}`))
	require.NoError(t, err)
	features := env.RoutingFeatures(false)
	route, err := svc.runTurnLoop(ctx, env, features, "credential", installation, "", nil, router.Request{RequestedModel: features.Model})
	require.NoError(t, err)
	require.Equal(t, catalog.ModelIDClaudeSonnet46.String(), route.Decision.Model)
	require.Equal(t, translate.ReasonUserForceModel, route.Decision.Reason)
	require.Zero(t, route.SessionKey)
	stored, found, err := store.Get(ctx, controlKey, forceModelSessionRole)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, pinNeverExpires, stored.PinnedUntil)
}

func TestAutomaticThreadPinsSurviveConcurrentDetachedWritebacks(t *testing.T) {
	installation := uuid.New()
	ctx := context.WithValue(statefulTestContext(), InstallationIDContextKey{}, installation.String())
	store := newForceModelMapStore()
	env, err := translate.ParseAnthropic([]byte(`{"model":"claude-sonnet-4-6","max_tokens":4096,"messages":[{"role":"user","content":"Identical inherited synthetic task"}]}`))
	require.NoError(t, err)
	features := env.RoutingFeatures(false)
	type routedThread struct {
		ctx    context.Context
		svc    *Service
		route  turnLoopResult
		model  string
		cancel context.CancelFunc
	}
	var threads []routedThread
	for _, thread := range []struct{ id, model string }{
		{"parent", catalog.ModelIDClaudeSonnet46.String()},
		{"child-one", catalog.ModelIDClaudeHaiku45.String()},
		{"child-two", catalog.ModelIDClaudeHaiku45.String()},
		{"fork", catalog.ModelIDClaudeHaiku45.String()},
	} {
		threadCtx, cancel := context.WithCancel(withTestThread(ctx, thread.id))
		scorer := &tierProbeRouter{available: map[string]struct{}{thread.model: {}}}
		svc := NewService(scorer, nil, nil, false, nil, store, false, providers.ProviderAnthropic, thread.model, nil)
		route, err := svc.runTurnLoop(threadCtx, env, features, "credential", installation, "", nil, router.Request{RequestedModel: features.Model})
		require.NoError(t, err)
		require.Equal(t, thread.model, route.Decision.Model)
		threads = append(threads, routedThread{threadCtx, svc, route, thread.model, cancel})
	}
	var writes sync.WaitGroup
	permits := make([]chan struct{}, len(threads))
	completed := make(chan struct{})
	for index, thread := range threads {
		thread.cancel()
		permit := make(chan struct{})
		permits[index] = permit
		writes.Go(func() {
			<-permit
			thread.svc.recordTurnUsage(thread.ctx, thread.route, providers.ProviderAnthropic, thread.model, 100, 20, 0, 0, false)
			completed <- struct{}{}
		})
	}
	for index := len(permits) - 1; index >= 0; index-- {
		close(permits[index])
		<-completed
	}
	writes.Wait()
	for _, thread := range threads {
		pin, found, err := store.Get(ctx, thread.route.SessionKey, thread.route.PinRole)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, thread.model, pin.LastServedModel)
		require.Equal(t, 20, pin.LastOutputTokens)
	}
}
