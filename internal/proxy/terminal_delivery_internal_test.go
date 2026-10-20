package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/translate"
)

func TestTerminalDeliveryOwnsFailureAcrossClientAndProviderCommit(t *testing.T) {
	formats := []translate.Format{translate.FormatAnthropic, translate.FormatOpenAI, translate.FormatOpenAIResponses, translate.FormatGemini}
	for _, format := range formats {
		for _, phase := range []string{"before_prelude", "visible_notice", "provider_output"} {
			for _, cause := range []error{errors.New("synthetic disconnect"), policy.ErrContextWindowExceeded, ErrSubscriptionPoolExhausted} {
				t.Run(fmt.Sprintf("%v/%s/%s", format, phase, cause.Error()), func(t *testing.T) {
					rec := httptest.NewRecorder()
					ctx, w, finish := beginTerminalDelivery(context.Background(), rec, format, true)
					prelude := newPreludeBuffer(w)
					marker := ""
					if phase == "visible_notice" {
						marker = "synthetic routing notice"
					}
					notices := &requestNoticeStream{}
					sink, err := notices.prepare(ctx, prelude, prelude, format, true, translate.NewRouterNotice(translate.NoticeModelSelection, marker, "synthetic", ""))
					require.NoError(t, err)
					if phase == "visible_notice" {
						require.NoError(t, prelude.CommitPrelude())
					}
					require.False(t, prelude.Committed(), "a notice cannot commit a provider attempt")
					prelude.Seal()
					if phase == "provider_output" {
						_, err = sink.Write([]byte(syntheticProviderPrelude(format)))
						require.NoError(t, err)
						require.True(t, prelude.Committed())
					}
					returned := cause
					finish(&returned)
					require.Same(t, cause, returned, "delivery must preserve the retry/telemetry cause")
					payload := rec.Body.String()
					switch format {
					case translate.FormatAnthropic:
						require.Equal(t, 1, strings.Count(payload, `"type":"error"`))
					case translate.FormatOpenAI:
						require.Equal(t, 1, strings.Count(payload, `"error":`))
					case translate.FormatOpenAIResponses:
						if phase != "before_prelude" || errors.Is(cause, policy.ErrContextWindowExceeded) {
							require.Equal(t, 1, strings.Count(payload, `"type":"response.failed"`))
							require.NotContains(t, payload, `"type":"response.completed"`)
						} else {
							require.Contains(t, payload, `"error":`)
						}
					case translate.FormatGemini:
						require.Equal(t, 1, strings.Count(payload, `"error":`))
						require.NotContains(t, payload, `"code":"`)
					}
					if errors.Is(cause, policy.ErrContextWindowExceeded) {
						require.Contains(t, payload, "prompt is too long")
						if format == translate.FormatOpenAI || format == translate.FormatOpenAIResponses {
							require.Contains(t, payload, openAIContextOverflowCode)
						}
					}
				})
			}
		}
	}
}

func syntheticProviderPrelude(format translate.Format) string {
	switch format {
	case translate.FormatAnthropic:
		return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"synthetic\",\"usage\":{}}}\n\n"
	case translate.FormatOpenAI:
		return "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n"
	case translate.FormatOpenAIResponses:
		return "data: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"synthetic\",\"status\":\"in_progress\",\"output\":[]}}\n\n"
	case translate.FormatGemini:
		return "data: {\"candidates\":[{\"index\":0,\"content\":{\"parts\":[{\"text\":\"partial\"}]}}]}\n\n"
	}
	return ""
}

func TestTerminalDeliveryDistinguishesCallerCancellationFromProviderWatchdog(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		rec := httptest.NewRecorder()
		ctx, cancel := context.WithCancel(context.Background())
		_, _, finish := beginTerminalDelivery(ctx, rec, translate.FormatAnthropic, true)
		if canceled {
			cancel()
		}
		returned := fmt.Errorf("%w: %w", providers.ErrUpstreamIdleTimeout, context.Canceled)
		finish(&returned)
		if canceled {
			require.Empty(t, rec.Body.String())
		} else {
			require.Contains(t, rec.Body.String(), `"error":`)
		}
		cancel()
	}
}

func TestTerminalDeliveryReplacesBufferedBodyBeforeCostFlush(t *testing.T) {
	rec := httptest.NewRecorder()
	ctx, w, finish := beginTerminalDelivery(context.Background(), rec, translate.FormatOpenAI, false)
	buffer := newResponseCostBuffer(w)
	require.True(t, registerResponseCostBuffer(ctx, buffer))
	_, err := buffer.Write([]byte(`{"partial":"invalid success"}`))
	require.NoError(t, err)
	returned := policy.ErrContextWindowExceeded
	finish(&returned)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.NotContains(t, rec.Body.String(), "invalid success")
	require.Contains(t, rec.Body.String(), openAIContextOverflowCode)
}

func TestTerminalDeliveryRetainsAlreadyDeliveredNativeHTTPFailure(t *testing.T) {
	rec := httptest.NewRecorder()
	_, writer, finish := beginTerminalDelivery(context.Background(), rec, translate.FormatOpenAI, true)
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusBadRequest)
	const body = `{"error":{"message":"synthetic native failure"}}`
	_, err := writer.Write([]byte(body))
	require.NoError(t, err)
	cause := error(&providers.UpstreamStatusError{Status: http.StatusBadRequest, Body: []byte(body)})
	finish(&cause)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, body, rec.Body.String())
}

func TestTerminalDeliveryCallerCancellationSupersedesMissingTerminal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	rec := httptest.NewRecorder()
	_, _, finish := beginTerminalDelivery(ctx, rec, translate.FormatAnthropic, true)
	cancel()
	cause := translate.ErrStreamIncomplete
	finish(&cause)
	require.ErrorIs(t, cause, context.Canceled)
	require.Empty(t, rec.Body.String())
}
