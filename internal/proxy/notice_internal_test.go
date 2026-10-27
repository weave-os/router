package proxy

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/subscriptions/entitlement"
	"weave-os/router/internal/translate"
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
			name: "Weave credits",
			ctx: billing.WithPrepaidAuthorization(context.Background(), billing.PrepaidAuthorization{
				CapacitySource: entitlement.CapacitySourcePrepaid,
			}),
			provider: providers.ProviderOpenAI,
			model:    "gpt-6-luna",
			want:     "funded by Weave credits",
		},
		{
			name: "managed Claude subscription",
			ctx: context.WithValue(WithManagedSubscriptionUsage(context.Background()), ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{
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
			}), ManagedSubscriptionProvidersContextKey{}, map[auth.SubscriptionProvider]struct{}{
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
			name:     "unknown source",
			ctx:      context.Background(),
			provider: providers.ProviderOpenAI,
			model:    "gpt-6-luna",
			want:     "funded by funding source unavailable",
		},
		{
			name: "paid retry replaces subscription source",
			ctx: billing.WithPrepaidAuthorization(context.Background(), billing.PrepaidAuthorization{
				CapacitySource: entitlement.CapacitySourcePrepaid,
			}),
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
			got := addRoutingFundingSource(test.ctx, test.provider, test.model, notice)
			assert.Contains(t, got.Text, test.want)
			if test.oldSource != "" {
				assert.NotContains(t, got.Text, "funded by "+test.oldSource)
			}
		})
	}
}
