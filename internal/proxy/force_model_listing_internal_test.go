package proxy

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"workweave/router/internal/providers"
	"workweave/router/internal/router/catalog"
	"workweave/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// listingService builds a Service that can serve every catalog provider, so
// the listing is gated only by what the test's context excludes. Routing
// targets are derived the way the composition root derives them.
func listingService() *Service {
	clients := make(map[string]providers.Client)
	keyedProviders := make(map[string]struct{})
	for _, m := range catalog.Models {
		for _, b := range m.Providers {
			clients[b.Provider] = nil
			keyedProviders[b.Provider] = struct{}{}
		}
	}
	return &Service{
		providers:                clients,
		deploymentKeyedProviders: keyedProviders,
		availableModels:          catalog.RoutingTargetSet(keyedProviders),
	}
}

func listedIDs(entries []forceModelListEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.ID)
	}
	return out
}

// The listing must name every model a pin would accept. qwen/qwen3.8-max is
// the specific miss that started this: it routes in prod but is absent from
// the cluster artifact's roster, so a roster-derived listing hides it.
func TestPinnableModels_IncludesRoutableAndPassthrough(t *testing.T) {
	entries := listingService().pinnableModels(context.Background())
	ids := listedIDs(entries)

	assert.Contains(t, ids, "qwen/qwen3.8-max")
	assert.Contains(t, ids, "claude-opus-5")
	assert.Contains(t, ids, "qwen/qwen3-coder")
	// Untiered rows are pinnable even though automatic routing never picks
	// them; omitting them tells a user a model they can reach doesn't exist.
	assert.Contains(t, ids, "claude-opus-4-8", "retired-but-pinnable models must be listed")

	byID := make(map[string]forceModelListEntry, len(entries))
	for _, e := range entries {
		byID[e.ID] = e
	}
	assert.True(t, byID["qwen/qwen3.8-max"].Routable)
	assert.False(t, byID["claude-opus-4-8"].Routable, "untiered catalog rows are passthrough-only")
	assert.Equal(t, providers.ProviderFireworks, byID["qwen/qwen3.8-max"].Provider)
}

// Every listed id must survive the pin gate — the listing is worthless if it
// advertises a model the very next command refuses.
func TestPinnableModels_EveryEntryResolvesAndIsPermitted(t *testing.T) {
	s := listingService()
	ctx := context.Background()

	for _, e := range s.pinnableModels(ctx) {
		id, provider, known := resolveForceModel(e.ID)
		require.True(t, known, "listed model %q must resolve", e.ID)
		assert.Equal(t, e.ID, id)

		_, reason := s.forcedModelBinding(ctx, id, provider)
		assert.Empty(t, reason, "listed model %q must be permitted", e.ID)
	}
}

// Routing targets sort ahead of passthrough-only rows: the models automatic
// routing can pick are what a user is usually after.
func TestPinnableModels_RoutableSortFirst(t *testing.T) {
	entries := listingService().pinnableModels(context.Background())
	require.NotEmpty(t, entries)

	seenPassthrough := false
	for _, e := range entries {
		if !e.Routable {
			seenPassthrough = true
			continue
		}
		assert.False(t, seenPassthrough, "routable %q sorted after a passthrough entry", e.ID)
	}
}

// An excluded model must not be advertised — it would be refused on the very
// next turn, which is exactly the confusion the listing exists to end.
func TestPinnableModels_OmitsExcludedModels(t *testing.T) {
	ctx := context.WithValue(context.Background(),
		InstallationExcludedModelsContextKey{}, []string{"qwen/qwen3.8-max"})

	ids := listedIDs(listingService().pinnableModels(ctx))
	assert.NotContains(t, ids, "qwen/qwen3.8-max")
	assert.Contains(t, ids, "claude-opus-5", "unrelated models stay listed")
}

// A model whose only provider isn't registered can't be dispatched to, so
// listing it would advertise a pin that goes nowhere.
func TestPinnableModels_OmitsUnregisteredProviders(t *testing.T) {
	s := &Service{
		providers:                map[string]providers.Client{providers.ProviderAnthropic: nil},
		deploymentKeyedProviders: map[string]struct{}{providers.ProviderAnthropic: {}},
	}

	ids := listedIDs(s.pinnableModels(context.Background()))
	require.NotEmpty(t, ids)
	assert.Contains(t, ids, "claude-opus-5")
	for _, id := range ids {
		assert.False(t, strings.HasPrefix(id, "qwen/"),
			"model %q has no registered provider on this deployment", id)
	}
}

func TestRenderForceModelListing_AnthropicNamesModelsAndAliases(t *testing.T) {
	entries := listingService().pinnableModels(context.Background())
	out := renderForceModelListing(entries, translate.FormatAnthropic)

	assert.True(t, strings.HasPrefix(out, "✦ **Weave Router** → "),
		"must carry the routing-marker prefix so ingress strips it from later turns")
	assert.True(t, strings.HasSuffix(out, "\n\n"), "routing markers end in a blank line")
	assert.Contains(t, out, "`qwen/qwen3.8-max`")
	assert.Contains(t, out, "Routing targets")
	assert.Contains(t, out, "Passthrough only")
	assert.Contains(t, out, "/unforce-model")
	// An alias is what makes the listing actionable for someone who typed a
	// shorthand rather than a full slug.
	assert.Contains(t, out, "qwen-max")
}

func TestRenderForceModelListing_OpenAIIsPlainText(t *testing.T) {
	entries := listingService().pinnableModels(context.Background())
	out := renderForceModelListing(entries, translate.FormatOpenAI)

	assert.True(t, strings.HasPrefix(out, "Weave Router: "))
	assert.NotContains(t, out, "✦", "the OpenAI surface takes plain text")
	assert.NotContains(t, out, "**")
	assert.Contains(t, out, "qwen/qwen3.8-max")
}

func TestRenderForceModelListing_EmptyIsExplained(t *testing.T) {
	out := renderForceModelListing(nil, translate.FormatAnthropic)
	assert.Contains(t, out, "no models are available to pin")
	assert.Contains(t, out, "allowed/excluded")
}

// The rejection must keep the exact phrase the Claude Code statusline greps
// for to classify this ack as a no-op that leaves the prior pin intact.
func TestRenderForceModelRejection_PreservesStatuslineSentinel(t *testing.T) {
	entries := listingService().pinnableModels(context.Background())

	anthropic := renderForceModelRejection("qwen 9.9", entries, translate.FormatAnthropic)
	assert.Contains(t, anthropic, "isn't a recognized model")
	assert.True(t, strings.HasPrefix(anthropic, "✦ **Weave Router** → "))
	assert.True(t, strings.HasSuffix(anthropic, "\n\n"))

	openai := renderForceModelRejection("qwen 9.9", entries, translate.FormatOpenAI)
	assert.Contains(t, openai, "isn't a recognized model")
	assert.NotContains(t, openai, "✦")
}

func TestRenderForceModelRejection_SuggestsNearMisses(t *testing.T) {
	entries := listingService().pinnableModels(context.Background())

	out := renderForceModelRejection("qwen3.9", entries, translate.FormatAnthropic)
	assert.Contains(t, out, "Did you mean")
	assert.Contains(t, out, "qwen/", "a qwen typo should surface qwen models")

	// With nothing close, still point at the listing rather than dead-ending.
	none := renderForceModelRejection("zzzzzzzz", entries, translate.FormatAnthropic)
	assert.NotContains(t, none, "Did you mean")
	assert.Contains(t, none, "/force-model")
}

func TestSuggestModelIDs_RanksClosestFirstAndCaps(t *testing.T) {
	entries := listingService().pinnableModels(context.Background())

	got := suggestModelIDs("qwen3.8", entries)
	require.NotEmpty(t, got)
	assert.Equal(t, "qwen/qwen3.8-max", got[0])
	assert.LessOrEqual(t, len(got), maxSuggestions)

	// A near-miss version must lead with the closest model, not the
	// alphabetically first one that happens to share the "qwen3" prefix.
	near := suggestModelIDs("qwen 3.9", entries)
	require.NotEmpty(t, near)
	assert.Equal(t, "qwen/qwen3.8-max", near[0])

	assert.Empty(t, suggestModelIDs("", entries))
}

// End-to-end through the command handler: the reported failure was that
// "/fm qwen 3.8" acked "force-model applied: qwen/qwen3-coder" — a different
// model, under a message that reads as success. Drive the real parse →
// resolve → pin path and assert on what the pin store actually recorded.
func TestHandleForceModelCommand_MultiWordNamePinsTheNamedModel(t *testing.T) {
	for _, tc := range []struct {
		command string
		wantID  string
	}{
		{"/fm qwen 3.8", "qwen/qwen3.8-max"},
		{"/fm qwen max", "qwen/qwen3.8-max"},
		{"/force-model qwen 3.8 now fix the bug", "qwen/qwen3.8-max"},
		{"/fm qwen", "qwen/qwen3-coder"},
	} {
		t.Run(tc.command, func(t *testing.T) {
			store := &recordingPinStore{}
			svc := NewService(nil, nil, nil, false, nil, store, false,
				providers.ProviderAnthropic, "claude-haiku-4-5", nil).
				WithDeploymentKeyedProviders(keyed(providers.ProviderFireworks))

			env := forceModelCommandEnv(t, tc.command)
			cmd, found := env.ExtractForceModelCommand(forceModelNameKnown)
			require.True(t, found)

			rec := httptest.NewRecorder()
			require.NoError(t, svc.handleForceModelCommand(
				context.Background(), rec, env, cmd,
				uuid.New(), DeriveSessionKey(env, "key-1"), 10))

			require.Len(t, store.upserts, 1, "an accepted force must write exactly one pin")
			assert.Equal(t, tc.wantID, store.upserts[0].Model)
			assert.Contains(t, rec.Body.String(), "force-model applied: "+tc.wantID)
		})
	}
}

// A bare /force-model lists rather than pinning, and must leave any existing
// pin untouched — it is a question, not a state change.
func TestHandleForceModelCommand_BareCommandListsWithoutPinning(t *testing.T) {
	store := &recordingPinStore{}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderAnthropic, providers.ProviderFireworks))

	env := forceModelCommandEnv(t, "/force-model")
	cmd, found := env.ExtractForceModelCommand(forceModelNameKnown)
	require.True(t, found)
	require.True(t, cmd.List)

	rec := httptest.NewRecorder()
	require.NoError(t, svc.handleForceModelCommand(
		context.Background(), rec, env, cmd,
		uuid.New(), DeriveSessionKey(env, "key-1"), 10))

	assert.Empty(t, store.upserts, "listing must not write or clear a pin")
	body := rec.Body.String()
	assert.Contains(t, body, "claude-opus-5")
	assert.Contains(t, body, "qwen/qwen3.8-max",
		"a model that routes in prod must appear in the listing")
	assert.NotContains(t, body, "force-model applied")
}

// The unrecognized-model reply must point at ids that actually work here.
func TestHandleForceModelCommand_UnknownModelSuggestsRealIDs(t *testing.T) {
	store := &recordingPinStore{}
	svc := NewService(nil, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithDeploymentKeyedProviders(keyed(providers.ProviderFireworks))

	env := forceModelCommandEnv(t, "/fm qwen 9.9")
	cmd, found := env.ExtractForceModelCommand(forceModelNameKnown)
	require.True(t, found)

	rec := httptest.NewRecorder()
	require.NoError(t, svc.handleForceModelCommand(
		context.Background(), rec, env, cmd,
		uuid.New(), DeriveSessionKey(env, "key-1"), 10))

	assert.Empty(t, store.upserts, "an unknown model must not be pinned")
	body := rec.Body.String()
	assert.Contains(t, body, "isn't a recognized model",
		"the statusline greps for this phrase to treat the ack as a no-op")
	assert.Contains(t, body, "qwen/", "a qwen near-miss should suggest qwen models")
}

// forceModelCommandEnv builds an Anthropic request whose sole user message is
// the given command text.
func forceModelCommandEnv(t *testing.T, text string) *translate.RequestEnvelope {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"model":      "claude-sonnet-4-6",
		"max_tokens": 1024,
		"messages": []any{
			map[string]any{"role": "user", "content": text},
		},
	})
	require.NoError(t, err)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	return env
}
