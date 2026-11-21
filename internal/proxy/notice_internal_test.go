package proxy

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNoticePreludeDoesNotCommitProviderAttempt(t *testing.T) {
	for _, format := range []translate.Format{translate.FormatAnthropic, translate.FormatOpenAI, translate.FormatOpenAIResponses, translate.FormatGemini} {
		t.Run(fmt.Sprint(format), func(t *testing.T) {
			rec := httptest.NewRecorder()
			buffer := newPreludeBuffer(rec)
			caps := translate.NoticeCapabilities{Format: format, Presentation: translate.NoticeText, Streaming: true, Eager: true}
			delivery := translate.NewNoticeDelivery(context.Background(), translate.NewProtocolNoticeSink(buffer, caps), caps)
			notice := translate.NewRouterNotice(translate.NoticeModelSelection, "✦ **Weave Router** → model\n\n", "model", "attempt-one")
			_, err := delivery.Deliver(notice)
			require.NoError(t, err)
			assert.Contains(t, rec.Body.String(), "Weave Router")
			assert.True(t, buffer.PreludeSent())
			assert.False(t, buffer.Committed())
			buffer.Seal()
			_, err = delivery.Write([]byte(": abandoned partial"))
			require.NoError(t, err)
			assert.False(t, buffer.Committed())
			buffer.Discard()
			delivery.ResetAttempt(true)
			outcome, err := delivery.Deliver(notice)
			require.NoError(t, err)
			assert.Equal(t, translate.NoticeDuplicate, outcome)
			assert.NotContains(t, rec.Body.String(), "abandoned")
			assert.False(t, buffer.Committed())
			buffer.Seal()
			_, err = delivery.Write([]byte(": real provider record\n\n"))
			require.NoError(t, err)
			assert.True(t, buffer.Committed())
			assert.Contains(t, rec.Body.String(), "real provider record")
		})
	}
}

func TestAddRoutingFundingSource(t *testing.T) {
	tests := []struct {
		name      string
		ctx       context.Context
		provider  string
		model     string
		want      string
		absent    bool
		oldSource string
	}{
		{
			name: "Claude subscription",
			ctx: requestcontext.WithCredentials(context.Background(), &requestcontext.Credentials{
				Source: requestcontext.SourceSubscription,
			}),
			provider: providers.ProviderAnthropic,
			model:    "claude-opus-5",
			want:     "funded by Claude subscription",
		},
		{
			name: "Codex subscription",
			ctx: requestcontext.WithCredentials(context.Background(), &requestcontext.Credentials{
				Source: requestcontext.SourceCodexSubscription,
			}),
			provider: providers.ProviderOpenAI,
			model:    "gpt-6.1-sol",
			want:     "funded by Codex subscription",
		},
		{
			name:     "Weave credits",
			ctx:      billing.WithOrganizationPrepaid(context.Background()),
			provider: providers.ProviderOpenAI,
			model:    "gpt-6-luna",
			want:     "funded by Weave credits",
		},
		{
			name: "managed Claude subscription",
			ctx: context.WithValue(WithManagedSubscriptionUsage(context.Background()), requestcontext.ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{
				auth.SubscriptionProviderClaude: {},
			}),
			provider: providers.ProviderAnthropic,
			model:    "claude-opus-5",
			want:     "funded by Claude subscription",
		},
		{
			name: "managed Codex subscription is selected over BYOK",
			ctx: context.WithValue(requestcontext.WithCredentials(WithManagedSubscriptionUsage(context.Background()), &requestcontext.Credentials{
				Source: requestcontext.SourceBYOK,
			}), requestcontext.ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{
				auth.SubscriptionProviderCodex: {},
			}),
			provider: providers.ProviderOpenAI,
			model:    "gpt-6.1-sol",
			want:     "funded by Codex subscription",
		},
		{
			name: "customer API key",
			ctx: requestcontext.WithCredentials(context.Background(), &requestcontext.Credentials{
				Source: requestcontext.SourceBYOK,
			}),
			provider: providers.ProviderAnthropic,
			model:    "claude-opus-5",
			want:     "funded by your provider API key",
		},
		{
			name:     "organization prepaid credits",
			ctx:      billing.WithOrganizationPrepaid(context.Background()),
			provider: providers.ProviderOpenAI,
			model:    "gpt-6-luna",
			want:     "funded by Weave credits",
		},
		{
			name:     "unknown source shows no funding",
			ctx:      context.Background(),
			provider: providers.ProviderOpenAI,
			model:    "gpt-6-luna",
			absent:   true,
		},
		{
			name:      "unknown source drops a stale funding label",
			ctx:       context.Background(),
			provider:  providers.ProviderOpenAI,
			model:     "gpt-6-luna",
			absent:    true,
			oldSource: "Claude subscription",
		},
		{
			name:      "paid retry replaces subscription source",
			ctx:       billing.WithOrganizationPrepaid(context.Background()),
			provider:  providers.ProviderOpenAI,
			model:     "gpt-6-luna",
			want:      "funded by Weave credits",
			oldSource: "Claude subscription",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			text := "✦ **Weave Router** → gpt-6-luna · best pick for this turn"
			if test.oldSource != "" {
				text += " · funded by " + test.oldSource
			}
			text += "\n\n"
			notice := translate.NewRouterNotice(translate.NoticeModelSelection, text, "gpt-6-luna", "")
			got := addRoutingFundingSource(test.ctx, seatAdmission{now: time.Now()}, test.provider, test.model, notice)
			if test.absent {
				assert.Equal(t, "✦ **Weave Router** → gpt-6-luna · best pick for this turn\n\n", got.Text)
				return
			}
			assert.Contains(t, got.Text, test.want)
			if test.oldSource != "" {
				assert.NotContains(t, got.Text, "funded by "+test.oldSource)
			}
		})
	}
}

func TestRoutingFundingSourceSkipsSpentManagedSeats(t *testing.T) {
	resetAt := time.Now().Add(48 * time.Hour)
	ctx := WithManagedSubscriptionCandidates(billing.WithOrganizationPrepaid(WithManagedSubscriptionUsage(context.Background())), []*auth.SubscriptionAccount{
		{ID: "synthetic-spent-codex", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateExhausted, CooldownUntil: &resetAt},
		{ID: "synthetic-reconnect-codex", Provider: auth.SubscriptionProviderCodex, State: auth.SubscriptionAccountStateReconnectRequired},
	})
	notice := translate.NewRouterNotice(translate.NoticeModelSelection, "✦ **Weave Router** → gpt-6-luna · model selected for this turn\n\n", "gpt-6-luna", "")

	got := addRoutingFundingSource(ctx, seatAdmission{now: time.Now()}, providers.ProviderOpenAI, "gpt-6-luna", notice)

	assert.Contains(t, got.Text, "funded by Weave credits")
	assert.NotContains(t, got.Text, "Codex subscription")
}

func TestRoutingFundingSourceSkipsObservedSpentManagedSeats(t *testing.T) {
	observer := usage.NewObserver([]byte("synthetic-salt"), time.Hour, time.Now)
	observer.Record(observer.Key([]byte("subscription-account:synthetic-active-codex")), usage.Snapshot{Primary: usage.Window{UsedPercent: 1.0, WindowMinutes: 300}})
	ctx := WithManagedSubscriptionCandidates(billing.WithOrganizationPrepaid(WithManagedSubscriptionUsage(context.Background())), []*auth.SubscriptionAccount{
		{ID: "synthetic-active-codex", Provider: auth.SubscriptionProviderCodex, Enabled: true, State: auth.SubscriptionAccountStateActive},
	})
	notice := translate.NewRouterNotice(translate.NoticeModelSelection, "✦ **Weave Router** → gpt-6-luna · model selected for this turn\n\n", "gpt-6-luna", "")

	spent := addRoutingFundingSource(ctx, seatAdmission{now: time.Now(), observer: observer}, providers.ProviderOpenAI, "gpt-6-luna", notice)
	credited := addRoutingFundingSource(ctx, seatAdmission{now: time.Now(), observer: observer, codexExtraCredits: true}, providers.ProviderOpenAI, "gpt-6-luna", notice)

	assert.Contains(t, spent.Text, "funded by Weave credits")
	assert.Contains(t, credited.Text, "funded by Codex subscription", "extra credits keep an observed-spent seat fundable")
}

func TestAddRoutingFundingSourceSkipsSpentManagedPool(t *testing.T) {
	now := time.Now()
	spentSeatReset := now.Add(time.Hour)
	ctx := WithManagedSubscriptionCandidates(WithManagedSubscriptionUsage(context.Background()), []*auth.SubscriptionAccount{{
		ID: "claude-seat", Provider: auth.SubscriptionProviderClaude, Enabled: true,
		State: auth.SubscriptionAccountStateExhausted, CooldownUntil: &spentSeatReset,
	}})
	notice := translate.NewRouterNotice(translate.NoticeModelSelection, "✦ **Weave Router** → claude-opus-5 · best pick for this turn\n\n", "claude-opus-5", "")
	got := addRoutingFundingSource(ctx, seatAdmission{now: now}, providers.ProviderAnthropic, "claude-opus-5", notice)
	assert.NotContains(t, got.Text, "funded by Claude subscription")
}
