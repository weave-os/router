package translate_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNoticeDelivery_RejectsNoticesMissingRouterIdentity(t *testing.T) {
	valid := translate.NewRouterNotice(syntheticNoticeKind, "synthetic status\n\n", "model", "attempt-a")
	foreign := valid
	foreign.Provenance = "client"
	kindless := valid
	kindless.Kind = ""
	for name, notice := range map[string]translate.RouterNotice{"foreign provenance": foreign, "missing kind": kindless} {
		delivery := noticeDelivery(context.Background(), httptest.NewRecorder(), translate.FormatAnthropic)
		outcome, err := delivery.Deliver(notice)
		require.ErrorIs(t, err, translate.ErrInvalidNotice, name)
		assert.Equal(t, translate.NoticeSuppressed, outcome, name)
	}
}

func TestNoticeDelivery_DeferIfEagerOnlyHoldsForEagerStreams(t *testing.T) {
	notice := translate.NewRouterNotice(syntheticNoticeKind, "synthetic status\n\n", "model", "attempt-a")
	for _, eager := range []bool{false, true} {
		caps := translate.NoticeCapabilities{Format: translate.FormatAnthropic, Presentation: translate.NoticeText, Streaming: true, Eager: eager}
		rec := httptest.NewRecorder()
		delivery := translate.NewNoticeDelivery(context.Background(), translate.NewProtocolNoticeSink(rec, caps), caps)
		assert.Equal(t, eager, delivery.DeferIfEager(notice), "eager=%v", eager)
	}
}
