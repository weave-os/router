package translate_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResponsesFailureDeliveryContextOverflow(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, prelude := range []bool{false, true} {
			if !streaming && prelude {
				continue
			}
			rec := httptest.NewRecorder()
			caps := translate.NoticeCapabilities{Format: translate.FormatOpenAIResponses, Streaming: streaming, Presentation: translate.NoticeText}
			delivery := translate.NewNoticeDelivery(context.Background(), translate.NewProtocolNoticeSink(rec, caps), caps)
			if prelude {
				_, err := delivery.Deliver(translate.NewRouterNotice(translate.NoticeModelSelection, "synthetic routing notice", "synthetic", ""))
				require.NoError(t, err)
			}
			failure := translate.TerminalFailure{Status: http.StatusBadRequest, Type: "invalid_request_error", Code: "context_length_exceeded", Message: "prompt is too long"}
			require.NoError(t, delivery.DeliverFailure(failure, streaming))
			if streaming {
				assert.Equal(t, http.StatusOK, rec.Code)
				assert.Equal(t, 1, strings.Count(rec.Body.String(), "event: response.created"))
				assert.Equal(t, 1, strings.Count(rec.Body.String(), "event: response.failed"))
				assert.Contains(t, rec.Body.String(), `"code":"context_length_exceeded"`)
				assert.NotContains(t, rec.Body.String(), "upstream_error")
			} else {
				assert.Equal(t, http.StatusBadRequest, rec.Code)
				assert.JSONEq(t, `{"error":{"message":"prompt is too long","type":"invalid_request_error","param":null,"code":"context_length_exceeded"}}`, rec.Body.String())
			}
		}
	}
}
