package proxy_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
)

func announcementReplayBody(t *testing.T, history ...string) []byte {
	t.Helper()
	input := []map[string]any{{"role": "user", "content": "Repository instructions for a synthetic parent and child."}}
	for _, text := range history {
		input = append(input, map[string]any{"role": "assistant", "content": []map[string]string{{"type": "output_text", "text": text}}})
	}
	body, err := json.Marshal(map[string]any{"model": "auto", "stream": true, "instructions": "Complete the requested task.", "input": input})
	require.NoError(t, err)
	return body
}

func announcementReplayContext() context.Context {
	return context.WithValue(context.Background(), proxy.ClientIdentityContextKey{}, proxy.ClientIdentity{
		ClientApp: proxy.ClientAppCodex, SessionID: "synthetic-shared-conversation",
	})
}

func testRoutingAnnouncementOverlap(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	t.Cleanup(func() {
		close(release)
		require.NoError(t, <-done)
	})
	decision := router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-6-luna", Reason: "test"}
	firstProvider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		close(started)
		<-release
		responsesTextUpstream(w)
	}}
	first := openAIChatServiceWithDecision(firstProvider, decision, nil)
	writer := newObservedResponseWriter()
	go func() {
		done <- first.ProxyOpenAIResponses(announcementReplayContext(), announcementReplayBody(t), writer,
			httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not start")
	}
	visible := strings.Join(responsesTextDeltas(t, []byte(writer.BodyString())), "")
	require.Contains(t, visible, "✦ **Weave Router** → gpt-6-luna")

	// Another worker resumes the same client after its eager badge, before
	// completion or usage history from the first request can be written.
	secondProvider := &fakeProvider{proxyResponse: responsesTextUpstream}
	second := openAIChatServiceWithDecision(secondProvider, decision, nil)
	rec := httptest.NewRecorder()
	require.NoError(t, second.ProxyOpenAIResponses(announcementReplayContext(), announcementReplayBody(t, visible), rec,
		httptest.NewRequest(http.MethodPost, "/v1/responses", nil)))
	text := strings.Join(responsesTextDeltas(t, rec.Body.Bytes()), "")
	assert.NotContains(t, text, "Weave Router", "a model already announced to this client must not repeat before completion")
	assert.Contains(t, text, "cached answer")
	require.Len(t, secondProvider.proxyBodies, 1)
	assert.NotContains(t, string(secondProvider.proxyBodies[0]), "Weave Router", "replay evidence must stay out of provider input")
}

func testLatestClientAnnouncement(t *testing.T) {
	const luna = "\u2063\u2060\u2063\u2060✦ **Weave Router** → gpt-6-luna · best pick for this turn\n\n"
	const terra = "\u2063\u2060\u2063\u2060✦ **Weave Router** → gpt-6.1-terra · best pick for this turn\n\n"
	for _, test := range []struct {
		name      string
		history   []string
		wantBadge bool
	}{
		{name: "same model", history: []string{luna}},
		{name: "latest model wins", history: []string{terra, luna}},
		{name: "switch back", history: []string{luna, terra}, wantBadge: true},
		{name: "first announcement", wantBadge: true},
		{name: "organic assistant prose", history: []string{"✦ **Weave Router** → gpt-6-luna · quoted documentation\n\n"}, wantBadge: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &fakeProvider{proxyResponse: responsesTextUpstream}
			svc := openAIChatServiceWithDecision(provider, router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-6-luna", Reason: "test"}, nil)
			rec := httptest.NewRecorder()
			require.NoError(t, svc.ProxyOpenAIResponses(announcementReplayContext(), announcementReplayBody(t, test.history...), rec,
				httptest.NewRequest(http.MethodPost, "/v1/responses", nil)))
			text := strings.Join(responsesTextDeltas(t, rec.Body.Bytes()), "")
			assert.Equal(t, test.wantBadge, strings.Contains(text, "✦ **Weave Router** → gpt-6-luna"))
			assert.Contains(t, text, "cached answer")
		})
	}
}

func TestRoutingAnnouncementReplay(t *testing.T) {
	t.Run("overlapping requests", testRoutingAnnouncementOverlap)
	t.Run("client announcement history", testLatestClientAnnouncement)
	t.Run("sidecar announcement history", testSidecarClientAnnouncement)
}

func testSidecarClientAnnouncement(t *testing.T) {
	const marker = "✦ **Weave Router** → Delegating work with gpt-6-luna\n↳ label: delegated_work"
	decision := router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-6-luna", Reason: "test",
		Metadata: &router.RoutingMetadata{DisplayMarker: marker},
	}
	var history []string
	for turn := range 2 {
		provider := &fakeProvider{proxyResponse: responsesTextUpstream}
		svc := openAIChatServiceWithDecision(provider, decision, nil)
		rec := httptest.NewRecorder()
		require.NoError(t, svc.ProxyOpenAIResponses(announcementReplayContext(), announcementReplayBody(t, history...), rec,
			httptest.NewRequest(http.MethodPost, "/v1/responses", nil)))
		text := strings.Join(responsesTextDeltas(t, rec.Body.Bytes()), "")
		assert.Contains(t, text, "cached answer")
		if turn == 0 {
			require.Contains(t, text, "Delegating work with gpt-6-luna")
			history = []string{text}
		} else {
			assert.NotContains(t, text, "Weave Router", "replaying a sidecar announcement must suppress an unchanged selection")
		}
		require.Len(t, provider.proxyBodies, 1)
		assert.NotContains(t, string(provider.proxyBodies[0]), "Delegating work with", "sidecar replay must be removed before dispatch")
	}
}
