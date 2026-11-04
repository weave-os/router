package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability/otel"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/subscriptions"
	"weave-os/router/internal/translate"
)

type fundingStreamProvider struct{ bypassFakeProvider }

func (p *fundingStreamProvider) Proxy(ctx context.Context, decision router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	return p.bypassFakeProvider.Proxy(ctx, decision, prep, w, r)
}

func TestFundingBypassEmptyStreamCannotSucceed(t *testing.T) {
	upstream := &fundingStreamProvider{}
	svc := newBypassService(upstream)
	env, err := translate.ParseAnthropic([]byte(`{"model":"claude-sonnet-4-6","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	ctx := billing.WithSubscriptionOnly(context.Background(), billing.SubscriptionOnlyCreditsDepleted)
	err = svc.bypassToAnthropic(ctx, env, env.RoutingFeatures(false), false, time.Now(), "synthetic", "synthetic", turntype.MainLoop, reasonUsageBypass, httptest.NewRequest(http.MethodPost, "/v1/messages", nil), rec)
	require.ErrorIs(t, err, errBypassRetryable)
	assert.Empty(t, rec.Body.String())
}

func TestFundingBypassCommittedFailureCannotRecover(t *testing.T) {
	for _, failure := range []error{errors.New("synthetic connection reset"), nil} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			upstream := &fundingStreamProvider{bypassFakeProvider{
				respBody: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":7}}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial answer\"}}\n\n",
				proxyErr: failure,
			}}
			svc := newBypassService(upstream)
			env, err := translate.ParseAnthropic([]byte(`{"model":"claude-sonnet-4-6","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			ctx := billing.WithSubscriptionOnly(context.Background(), billing.SubscriptionOnlyCreditsDepleted)
			err = svc.bypassToAnthropic(ctx, env, env.RoutingFeatures(false), false, time.Now(), "synthetic", "synthetic", turntype.MainLoop, reasonUsageBypass, httptest.NewRequest(http.MethodPost, "/v1/messages", nil), rec)
			require.Error(t, err)
			require.NotErrorIs(t, err, errBypassRetryable)
			assert.Equal(t, 1, strings.Count(rec.Body.String(), "event: message_start\n"))
			assert.Equal(t, 1, strings.Count(rec.Body.String(), "event: error\n"))
			assert.Contains(t, rec.Body.String(), "partial answer")
			assert.NotContains(t, rec.Body.String(), "event: message_stop")
		})
	}
}

func TestFundingBypassCancellationDoesNotDispatch(t *testing.T) {
	upstream := &fundingStreamProvider{}
	svc := newBypassService(upstream)
	env, err := translate.ParseAnthropic([]byte(`{"model":"claude-sonnet-4-6","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(billing.WithSubscriptionOnly(context.Background(), billing.SubscriptionOnlyCreditsDepleted))
	cancel()
	rec := httptest.NewRecorder()
	err = svc.bypassToAnthropic(ctx, env, env.RoutingFeatures(false), false, time.Now(), "synthetic", "synthetic", turntype.MainLoop, reasonUsageBypass, httptest.NewRequest(http.MethodPost, "/v1/messages", nil), rec)
	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, upstream.dispatches)
	assert.Empty(t, rec.Body.String())
}

func TestFundingNoticePolicyAndReplay(t *testing.T) {
	const model = "synthetic-model"
	const marker = "✦ **Weave Router** → synthetic-model\n\n"
	for _, warning := range subscriptionFundingWarnings {
		t.Run(string(warning.reason)+"/"+string(warning.source), func(t *testing.T) {
			ctx := billing.WithSubscriptionOnly(context.Background(), warning.reason)
			notice := routerNoticeForRequest(ctx, http.Header{}, warning.source, model, marker)
			assert.Equal(t, translate.NoticeFundingWarning, notice.Kind)
			assert.Equal(t, translate.NoticeFromRouter, notice.Provenance)
			assert.Equal(t, model, notice.Model)
			if warning.source == subscriptions.ProviderClaude {
				assert.Contains(t, notice.Text, "Anthropic subscription")
			} else {
				assert.Contains(t, notice.Text, "ChatGPT (Codex) subscription")
			}
			if warning.reason == billing.SubscriptionOnlyCreditsDepleted {
				assert.Contains(t, notice.Text, "credits are depleted")
				assert.Contains(t, notice.Text, topUpURL)
			} else {
				assert.Contains(t, notice.Text, "spend cap has been reached")
				assert.NotContains(t, notice.Text, topUpURL)
			}
			rec := httptest.NewRecorder()
			caps := translate.NoticeCapabilities{Format: translate.FormatOpenAIResponses, Streaming: true, Presentation: translate.NoticeText}
			delivery := translate.NewNoticeDelivery(ctx, translate.NewProtocolNoticeSink(rec, caps), caps)
			_, err := delivery.Deliver(notice)
			require.NoError(t, err)
			var responsesText string
			for _, line := range strings.Split(rec.Body.String(), "\n") {
				if payload, ok := strings.CutPrefix(line, "data: "); ok {
					if delta := gjson.Get(payload, "delta"); delta.Type == gjson.String {
						responsesText += delta.String()
					}
				}
			}
			require.Contains(t, responsesText, strings.TrimSpace(notice.Text))
			for _, body := range []string{
				`{"messages":[{"role":"assistant","content":` + jsonString(notice.Text+"provider answer") + `}]}`,
				`{"messages":[{"role":"assistant","content":[{"type":"text","text":` + jsonString(notice.Text+"provider answer") + `}]}]}`,
				`{"input":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":` + jsonString(responsesText+"provider answer") + `}]}]}`,
			} {
				cleaned, err := translate.StripRoutingMarkerFromMessages([]byte(body))
				require.NoError(t, err)
				cleaned, err = translate.StripRoutingBadgeFromResponsesInput(cleaned)
				require.NoError(t, err)
				assert.NotContains(t, string(cleaned), "paid model fallback is disabled")
				assert.Contains(t, string(cleaned), "provider answer")
				echoCtx := withSubscriptionOnlyWarningEcho(ctx, []byte(body))
				echoCtx = withSubscriptionOnlyWarningEcho(echoCtx, []byte(`{"messages":[]}`))
				replayed := routerNoticeForRequest(echoCtx, http.Header{}, warning.source, model, marker)
				assert.Equal(t, translate.NoticeModelSelection, replayed.Kind)
				assert.Equal(t, marker, replayed.Text)
			}
		})
	}
}

func TestFundingNoticeDoesNotEstablishProviderFacts(t *testing.T) {
	rec := httptest.NewRecorder()
	buffer := newPreludeBuffer(rec)
	caps := translate.NoticeCapabilities{Format: translate.FormatAnthropic, Streaming: true, Presentation: translate.NoticeText, Eager: true}
	delivery := translate.NewNoticeDelivery(context.Background(), translate.NewProtocolNoticeSink(buffer, caps), caps)
	extractor := otel.NewUsageExtractor(delivery, providers.ProviderAnthropic)
	terminal := newStreamTerminalObserver(extractor, nativeStreamAnthropic)
	terminal.Header().Set("Content-Type", "text/event-stream")
	terminal.WriteHeader(http.StatusOK)
	_, err := delivery.Deliver(translate.NewRouterNotice(translate.NoticeFundingWarning, subscriptionOnlyWarningMarker, "synthetic-model", ""))
	require.NoError(t, err)
	progress := 0
	require.True(t, terminal.ArmOutputProgress(func() { progress++ }))
	_, err = delivery.Deliver(translate.NewRouterNotice(translate.NoticeFundingWarning, subscriptionSpendCapWarningMarker, "synthetic-model", ""))
	require.NoError(t, err)
	assert.Contains(t, rec.Body.String(), "credits are depleted")
	assert.Zero(t, progress)
	input, output := extractor.Tokens()
	assert.Zero(t, input)
	assert.Zero(t, output)
	assert.ErrorIs(t, terminal.streamErr(), translate.ErrStreamEmpty)
	assert.False(t, buffer.Committed())
	buffer.Seal()
	_, err = terminal.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":7}}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"provider answer\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":2}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	require.NoError(t, err)
	assert.Equal(t, 1, progress)
	input, output = extractor.Tokens()
	assert.Equal(t, 7, input)
	assert.Equal(t, 2, output)
	require.NoError(t, terminal.streamErr())
	assert.True(t, buffer.Committed())
}

func TestFundingNoticeBufferedAndStreamingCapabilities(t *testing.T) {
	for _, format := range []translate.Format{translate.FormatAnthropic, translate.FormatOpenAI, translate.FormatOpenAIResponses, translate.FormatGemini} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%v/stream=%v", format, streaming), func(t *testing.T) {
				rec := httptest.NewRecorder()
				caps := translate.NoticeCapabilities{Format: format, Streaming: streaming, Presentation: translate.NoticeText}
				delivery := translate.NewNoticeDelivery(context.Background(), translate.NewProtocolNoticeSink(rec, caps), caps)
				notice := translate.NewRouterNotice(translate.NoticeFundingWarning, subscriptionSpendCapWarningMarkerCodex, "synthetic-model", "")
				outcome, err := delivery.Deliver(notice)
				require.NoError(t, err)
				if !streaming && format != translate.FormatOpenAIResponses {
					assert.Equal(t, translate.NoticeSuppressed, outcome)
					assert.Empty(t, rec.Body.String())
					return
				}
				assert.Equal(t, translate.NoticeDelivered, outcome)
				if !streaming {
					_, err = delivery.Write([]byte(`{"id":"synthetic","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"provider answer"}]}]}`))
					require.NoError(t, err)
				}
				assert.Contains(t, rec.Body.String(), "spend cap has been reached")
				assert.Contains(t, rec.Body.String(), "ChatGPT (Codex)")
			})
		}
	}
}

func TestFundingBypassBufferedResponseStaysWarningFree(t *testing.T) {
	upstream := &bypassFakeProvider{respBody: `{"id":"synthetic","type":"message","content":[{"type":"text","text":"provider answer"}],"usage":{"input_tokens":3,"output_tokens":2}}`}
	svc := newBypassService(upstream)
	env := bypassAnthropicEnvelope(t)
	ctx := billing.WithSubscriptionOnly(context.Background(), billing.SubscriptionOnlySpendCapReached)
	rec := httptest.NewRecorder()
	err := svc.bypassToAnthropic(ctx, env, env.RoutingFeatures(false), false, time.Now(), "synthetic", "synthetic", turntype.MainLoop, reasonUsageBypass, httptest.NewRequest(http.MethodPost, "/v1/messages", nil), rec)
	require.NoError(t, err)
	assert.JSONEq(t, upstream.respBody, rec.Body.String())
	assert.Equal(t, "application/json", rec.Result().Header.Get("Content-Type"))
}

func TestFundingBypassPartialStreamLeavesRecoveryPristine(t *testing.T) {
	for _, reason := range []billing.SubscriptionOnlyReason{billing.SubscriptionOnlyCreditsDepleted, billing.SubscriptionOnlySpendCapReached} {
		t.Run(string(reason), func(t *testing.T) {
			upstream := &fundingStreamProvider{bypassFakeProvider{respBody: "event: message_start\ndata: {", proxyErr: errors.New("synthetic connection reset")}}
			svc := newBypassService(upstream)
			env, err := translate.ParseAnthropic([]byte(`{"model":"claude-sonnet-4-6","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			ctx := billing.WithSubscriptionOnly(context.Background(), reason)
			err = svc.bypassToAnthropic(ctx, env, env.RoutingFeatures(false), false, time.Now(), "synthetic", "synthetic", turntype.MainLoop, reasonUsageBypass, req, rec)
			require.ErrorIs(t, err, errBypassRetryable)
			assert.Empty(t, rec.Body.String(), "an abandoned partial record and its synthetic warning must not escape into recovery")
			assert.False(t, rec.Flushed)
			assert.Empty(t, rec.Header().Get("Content-Type"))
		})
	}
}

func TestFundingBypassBufferedFailureCannotRecover(t *testing.T) {
	upstream := &bypassFakeProvider{respBody: `{"id":"synthetic","content":[`, proxyErr: errors.New("synthetic connection reset")}
	svc := newBypassService(upstream)
	env := bypassAnthropicEnvelope(t)
	ctx := billing.WithSubscriptionOnly(context.Background(), billing.SubscriptionOnlyCreditsDepleted)
	err := svc.bypassToAnthropic(ctx, env, env.RoutingFeatures(false), false, time.Now(), "synthetic", "synthetic", turntype.MainLoop, reasonUsageBypass, httptest.NewRequest(http.MethodPost, "/v1/messages", nil), httptest.NewRecorder())
	require.Error(t, err)
	require.NotErrorIs(t, err, errBypassRetryable)
}
