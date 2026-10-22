package proxy

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
