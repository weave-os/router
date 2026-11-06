package proxy

import (
	"context"
	"net/http"
	"strings"
	"time"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/subscriptions"
	"weave-os/router/internal/subscriptions/entitlement"
	"weave-os/router/internal/translate"
)

// The coordinator outlives binding attempts; the buffer's provider commitment
// remains the retry gate even after a notice becomes visible.
type requestNoticeStream struct {
	delivery   *translate.NoticeDelivery
	lastNotice translate.RouterNotice
}

func (s *requestNoticeStream) prepare(ctx context.Context, sink http.ResponseWriter, prelude *preludeBuffer, format translate.Format, streaming bool, notice translate.RouterNotice) (http.ResponseWriter, error) {
	if s.delivery == nil {
		capabilities := translate.NoticeCapabilities{Format: format, Streaming: streaming, Presentation: translate.NoticeText, Eager: streaming && (format == translate.FormatOpenAI || format == translate.FormatOpenAIResponses)}
		s.delivery = translate.NewNoticeDelivery(ctx, translate.NewProtocolNoticeSink(sink, capabilities), capabilities)
	}
	if trace := attemptTraceFromContext(ctx); trace != nil && streaming {
		s.delivery.SetBeforeProviderContent(func() (translate.RouterNotice, bool) {
			return slowStartNotice(ctx, trace, slowStarts, time.Now())
		})
	}
	retainNotices := prelude == nil || prelude.PreludeSent()
	s.delivery.ResetAttempt(retainNotices)
	if !retainNotices {
		s.lastNotice = translate.RouterNotice{}
	}
	if notice.Text != "" && (notice.Kind != s.lastNotice.Kind || notice.Text != s.lastNotice.Text) {
		outcome, err := s.delivery.Deliver(notice)
		if err != nil {
			return nil, err
		}
		if outcome == translate.NoticeDelivered {
			s.lastNotice = notice
		}
	}
	return s.delivery, nil
}

func addRoutingFundingSource(ctx context.Context, provider, model string, notice translate.RouterNotice) translate.RouterNotice {
	if notice.Kind != translate.NoticeModelSelection || notice.Text == "" {
		return notice
	}
	credentialSource := ""
	if credentials := CredentialsFromContext(ctx); credentials != nil {
		credentialSource = credentials.Source
	}
	if managedSubscriptionCanServe(ctx, provider, model) {
		if source, eligible := managedSubscriptionProviderFromUpstream(provider, model); eligible {
			switch source {
			case subscriptions.ProviderClaude:
				return setFundingSource(notice, "Claude subscription")
			case subscriptions.ProviderCodex:
				return setFundingSource(notice, "Codex subscription")
			}
		}
	}

	switch credentialSource {
	case credSourceSubscription:
		return setFundingSource(notice, "Claude subscription")
	case credSourceSubscriptionOverage:
		return setFundingSource(notice, "Claude subscription overage")
	case credSourceCodexSubscription:
		return setFundingSource(notice, "Codex subscription")
	case credSourceBYOK, credSourceClient:
		return setFundingSource(notice, "your provider API key")
	}

	switch telemetryCapacitySource(ctx, credentialSource) {
	case entitlement.CapacitySourcePrepaid:
		return setFundingSource(notice, "Weave credits")
	case entitlement.CapacitySourceIncludedRouter:
		return setFundingSource(notice, "Weave Router allowance")
	case entitlement.CapacitySourceBillingOverride:
		return setFundingSource(notice, "Weave-managed billing")
	}
	if billing.OrganizationPrepaidFromContext(ctx) {
		return setFundingSource(notice, "Weave credits")
	}
	return setFundingSource(notice, "")
}

func setFundingSource(notice translate.RouterNotice, source string) translate.RouterNotice {
	lineEnd := strings.IndexByte(notice.Text, '\n')
	firstLine := notice.Text
	if lineEnd >= 0 {
		firstLine = notice.Text[:lineEnd]
	}
	if labelStart := strings.LastIndex(firstLine, " · funded by "); labelStart >= 0 {
		firstLine = firstLine[:labelStart]
	}
	if source != "" {
		firstLine += " · funded by " + source
	}
	if lineEnd < 0 {
		notice.Text = firstLine
		return notice
	}
	notice.Text = firstLine + notice.Text[lineEnd:]
	return notice
}
