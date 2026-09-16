package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/require"
)

type completionTestStore struct {
	records  []FeedbackRequest
	complete func() error
}

func (s *completionTestStore) CompleteFeedbackRequest(_ context.Context, r FeedbackRequest) error {
	if s.complete != nil {
		if err := s.complete(); err != nil {
			return err
		}
	}
	s.records = append(s.records, r)
	return nil
}
func (*completionTestStore) AcceptRouterFeedback(_ context.Context, event RouterFeedbackEvent) (RouterFeedbackEvent, error) {
	return event, nil
}

var completionStreams = []struct {
	name                 string
	format               translate.EscalationResponseFormat
	prefix, suffix, body string
}{
	{"anthropic", translate.EscalationResponseAnthropic,
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		`{"type":"message","stop_reason":"end_turn","content":[{"type":"text","text":"hello"}]}`},
	{"chat", translate.EscalationResponseChat,
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"completion_tokens\":1}}\n\ndata: [DONE]\n\n",
		`{"choices":[{"index":0,"message":{"content":"hello"},"finish_reason":"stop"}]}`},
	{"responses", translate.EscalationResponseResponses,
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n",
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"status\":\"completed\",\"output\":[]}}\n\n",
		`{"id":"resp_test","status":"completed","output":[]}`},
	{"gemini", translate.EscalationResponseGemini,
		"data: {\"candidates\":[{\"index\":0,\"content\":{\"parts\":[{\"text\":\"hello\"}]}}]}\n\n",
		"data: {\"candidates\":[{\"index\":0,\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"candidatesTokenCount\":1}}\n\n",
		`{"candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}]}`},
}

func TestFeedbackCompletionCommitsBeforeTerminalVisibility(t *testing.T) {
	for _, tc := range completionStreams {
		for _, stream := range []bool{false, true} {
			for _, fail := range []bool{false, true} {
				t.Run(tc.name+"/"+map[bool]string{true: "stream", false: "body"}[stream]+"/"+map[bool]string{true: "failure", false: "success"}[fail], func(t *testing.T) {
					rec := httptest.NewRecorder()
					store := &completionTestStore{complete: func() error {
						if stream {
							require.Equal(t, tc.prefix, rec.Body.String())
						} else {
							require.Empty(t, rec.Body.String())
							require.False(t, rec.Flushed)
						}
						if fail {
							return errors.New("history unavailable")
						}
						return nil
					}}
					gate := newFeedbackCompletion(rec, tc.format, stream)
					gate.active, gate.store = true, store
					input := tc.body
					if stream {
						input = tc.prefix + tc.suffix
					}
					// Every byte boundary can split event names, JSON, CRLF and terminals.
					for _, b := range []byte(input) {
						_, err := gate.Write([]byte{b})
						require.NoError(t, err)
						gate.Flush()
					}
					err := gate.finish(context.Background(), nil)
					if fail {
						require.Error(t, err)
						require.Empty(t, store.records)
						if stream {
							require.NotContains(t, rec.Body.String(), tc.suffix)
							require.Contains(t, rec.Body.String(), "error")
						} else {
							require.Empty(t, rec.Body.String())
						}
					} else {
						require.NoError(t, err)
						require.Len(t, store.records, 1)
						require.Equal(t, input, rec.Body.String())
					}
				})
			}
		}
	}
}

func TestFeedbackCompletionRejectsIncompleteAndTrailingOutput(t *testing.T) {
	for _, tc := range completionStreams {
		for _, trailing := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{true: "/trailing", false: "/incomplete"}[trailing], func(t *testing.T) {
				rec := httptest.NewRecorder()
				store := &completionTestStore{}
				gate := newFeedbackCompletion(rec, tc.format, true)
				gate.active, gate.store = true, store
				input := tc.prefix
				if trailing {
					input += tc.suffix + tc.prefix
				}
				_, err := gate.Write([]byte(input))
				require.NoError(t, err)
				require.Error(t, gate.finish(context.Background(), nil))
				require.Empty(t, store.records)
				require.NotContains(t, rec.Body.String(), tc.suffix)
			})
		}
	}
}

func TestFeedbackCompletionHoldsAllMultiChoiceEndings(t *testing.T) {
	for _, format := range []translate.EscalationResponseFormat{translate.EscalationResponseChat, translate.EscalationResponseGemini} {
		rec := httptest.NewRecorder()
		store := &completionTestStore{}
		gate := newFeedbackCompletion(rec, format, true)
		gate.active, gate.store = true, store
		var suffix string
		if format == translate.EscalationResponseChat {
			suffix = "data: {\"choices\":[{\"index\":0,\"finish_reason\":\"stop\"},{\"index\":1,\"delta\":{\"content\":\"ongoing\"}}]}\n\ndata: {\"choices\":[{\"index\":1,\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n"
		} else {
			suffix = "data: {\"candidates\":[{\"index\":0,\"finishReason\":\"STOP\"},{\"index\":1,\"content\":{\"parts\":[{\"text\":\"ongoing\"}]}}]}\n\ndata: {\"candidates\":[{\"index\":1,\"finishReason\":\"MAX_TOKENS\"}]}\n\n"
		}
		_, err := gate.Write([]byte(suffix))
		require.NoError(t, err)
		require.Empty(t, rec.Body.String())
		require.NoError(t, gate.finish(context.Background(), nil))
		require.Len(t, store.records, 1)
		require.Equal(t, suffix, rec.Body.String())
	}
}

func TestFeedbackCompletionPreservesLargeLengthLimitedResponses(t *testing.T) {
	rec := httptest.NewRecorder()
	store := &completionTestStore{}
	gate := newFeedbackCompletion(rec, translate.EscalationResponseResponses, true)
	gate.active, gate.store = true, store
	event := "event: response.incomplete\r\ndata: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"},\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"" + strings.Repeat("x", 2<<20) + "\"}]}]}}\r\n\r\n"
	_, err := gate.Write([]byte(event))
	require.NoError(t, err)
	require.Empty(t, rec.Body.String())
	require.NoError(t, gate.finish(context.Background(), nil))
	require.Equal(t, event, rec.Body.String())
	require.Len(t, store.records, 1)
}

// An upstream that never completes an SSE frame must not grow the router's
// memory for the life of the stream; the hold is capped and the stream fails.
func TestFeedbackCompletionBoundsIncompleteFrames(t *testing.T) {
	rec := httptest.NewRecorder()
	store := &completionTestStore{}
	gate := newFeedbackCompletion(rec, translate.EscalationResponseAnthropic, true)
	gate.active, gate.store = true, store
	chunk := []byte("data: " + strings.Repeat("x", 64<<10))
	var written int
	var err error
	for written <= feedbackCompletionHoldCap+len(chunk) {
		_, err = gate.Write(chunk)
		if err != nil {
			break
		}
		written += len(chunk)
	}
	require.ErrorIs(t, err, errFeedbackHoldExceeded)
	require.LessOrEqual(t, gate.partial.Len()+gate.suffix.Len(), feedbackCompletionHoldCap, "retained bytes must stay within the cap")
	require.Zero(t, gate.partial.Len(), "an over-cap hold is released, not retained")
	_, err = gate.Write([]byte("more"))
	require.ErrorIs(t, err, errFeedbackHoldExceeded, "a capped stream stays failed")
	require.ErrorIs(t, gate.finish(context.Background(), nil), errFeedbackHoldExceeded)
	require.Empty(t, store.records)
}

// A frame within the cap is held whole: the Responses terminal event carries
// the full output, and it must still reach the client intact.
func TestFeedbackCompletionHoldsLargeFrameWithinCap(t *testing.T) {
	rec := httptest.NewRecorder()
	store := &completionTestStore{}
	gate := newFeedbackCompletion(rec, translate.EscalationResponseAnthropic, true)
	gate.active, gate.store = true, store
	prefix := completionStreams[0].prefix
	suffix := completionStreams[0].suffix
	large := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"" + strings.Repeat("y", feedbackCompletionHoldCap/2) + "\"}}\n\n"
	for _, part := range []string{prefix, large[:len(large)/2], large[len(large)/2:], suffix} {
		_, err := gate.Write([]byte(part))
		require.NoError(t, err)
	}
	require.NoError(t, gate.finish(context.Background(), nil))
	require.Equal(t, prefix+large+suffix, rec.Body.String())
	require.Len(t, store.records, 1)
}

// Chat and Gemini providers restate the finish reason on a trailing usage or
// bookkeeping chunk. That repeat is not new output and must not fail a
// response the client already received in full.
func TestFeedbackCompletionAcceptsRepeatedFinishReason(t *testing.T) {
	for _, tc := range []struct {
		name   string
		format translate.EscalationResponseFormat
		wire   string
	}{
		{"chat usage chunk restates finish_reason", translate.EscalationResponseChat,
			"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n" +
				"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
				"data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\",\"tool_calls\":[]},\"finish_reason\":\"stop\"}],\"usage\":{\"completion_tokens\":1}}\n\n" +
				"data: [DONE]\n\n"},
		{"gemini usage chunk restates finishReason", translate.EscalationResponseGemini,
			"data: {\"candidates\":[{\"index\":0,\"content\":{\"parts\":[{\"text\":\"hello\"}]}}]}\n\n" +
				"data: {\"candidates\":[{\"index\":0,\"finishReason\":\"STOP\"}]}\n\n" +
				"data: {\"candidates\":[{\"index\":0,\"content\":{\"parts\":[]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"candidatesTokenCount\":1}}\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			store := &completionTestStore{}
			gate := newFeedbackCompletion(rec, tc.format, true)
			gate.active, gate.store = true, store
			_, err := gate.Write([]byte(tc.wire))
			require.NoError(t, err)
			require.NoError(t, gate.finish(context.Background(), nil))
			require.Len(t, store.records, 1)
			require.Equal(t, tc.wire, rec.Body.String())
		})
	}
}

// Output after a finished choice, or a finish reason that is cleared again,
// is still a lifecycle violation.
func TestFeedbackCompletionRejectsOutputAfterFinishedChoice(t *testing.T) {
	for _, tc := range []struct {
		name   string
		format translate.EscalationResponseFormat
		wire   string
	}{
		{"chat content after finish", translate.EscalationResponseChat,
			"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
				"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"more\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"},
		{"chat finish cleared", translate.EscalationResponseChat,
			"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
				"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n"},
		{"gemini parts after finish", translate.EscalationResponseGemini,
			"data: {\"candidates\":[{\"index\":0,\"finishReason\":\"STOP\"}]}\n\n" +
				"data: {\"candidates\":[{\"index\":0,\"content\":{\"parts\":[{\"text\":\"more\"}]},\"finishReason\":\"STOP\"}]}\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			store := &completionTestStore{}
			gate := newFeedbackCompletion(rec, tc.format, true)
			gate.active, gate.store = true, store
			_, err := gate.Write([]byte(tc.wire))
			require.NoError(t, err)
			require.ErrorIs(t, gate.finish(context.Background(), nil), translate.ErrStreamOrder)
			require.Empty(t, store.records)
		})
	}
}

// A client that disconnects after the answer is on the wire cancels the
// request context; the history commit must survive that cancellation.
func TestFeedbackCompletionCommitsHistoryAfterClientCancellation(t *testing.T) {
	for _, tc := range completionStreams {
		for _, stream := range []bool{false, true} {
			t.Run(tc.name+"/"+map[bool]string{true: "stream", false: "body"}[stream], func(t *testing.T) {
				rec := httptest.NewRecorder()
				var commitErr error
				var commitBounded bool
				store := &contextCapturingStore{capture: func(ctx context.Context) {
					commitErr = ctx.Err()
					_, commitBounded = ctx.Deadline()
				}}
				gate := newFeedbackCompletion(rec, tc.format, stream)
				gate.active, gate.store = true, store
				input := tc.body
				if stream {
					input = tc.prefix + tc.suffix
				}
				_, err := gate.Write([]byte(input))
				require.NoError(t, err)
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				require.NoError(t, gate.finish(ctx, nil))
				require.Len(t, store.records, 1)
				require.NoError(t, commitErr, "the commit context must not inherit the request's cancellation")
				require.True(t, commitBounded, "the detached commit is still bounded")
				require.Equal(t, input, rec.Body.String())
			})
		}
	}
}

type contextCapturingStore struct {
	records []FeedbackRequest
	capture func(context.Context)
}

func (s *contextCapturingStore) CompleteFeedbackRequest(ctx context.Context, r FeedbackRequest) error {
	s.capture(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	s.records = append(s.records, r)
	return nil
}
func (*contextCapturingStore) AcceptRouterFeedback(_ context.Context, event RouterFeedbackEvent) (RouterFeedbackEvent, error) {
	return event, nil
}

func TestFeedbackCompletionTransportFailureNeverCommits(t *testing.T) {
	for _, tc := range completionStreams {
		rec := httptest.NewRecorder()
		store := &completionTestStore{}
		gate := newFeedbackCompletion(rec, tc.format, true)
		gate.active, gate.store = true, store
		_, err := gate.Write([]byte(tc.prefix + tc.suffix))
		require.NoError(t, err)
		failure := errors.New("socket failed")
		require.ErrorIs(t, gate.finish(context.Background(), failure), failure)
		require.Empty(t, store.records)
		require.NotContains(t, rec.Body.String(), tc.suffix)
	}
}

func TestResponseCostBufferAbortsUnconfirmedSuccess(t *testing.T) {
	rec := httptest.NewRecorder()
	b := newResponseCostBuffer(rec)
	b.Header().Set("Content-Length", "100")
	_, err := b.Write([]byte("success"))
	require.NoError(t, err)
	failure := errors.New("commit failed")
	require.ErrorIs(t, b.finish(failure), failure)
	require.Empty(t, rec.Body.String())
	require.Empty(t, rec.Header().Get("Content-Length"))
	require.False(t, rec.Flushed)
}

var _ http.ResponseWriter = (*feedbackCompletion)(nil)

func TestFeedbackCompletionPopulationAndLogicalScope(t *testing.T) {
	const requestedModel = "claude-sonnet-4-6"
	for _, tt := range []turntype.TurnType{turntype.MainLoop, turntype.ToolResult, turntype.Probe, turntype.SubAgentDispatch, turntype.Compaction} {
		store := &completionTestStore{}
		svc := &Service{feedbackStore: store}
		rec := httptest.NewRecorder()
		key := [sessionpin.SessionKeyLen]byte{7}
		installation := uuid.New()
		w, gate, owns := svc.beginFeedbackCompletion(context.Background(), rec, translate.EscalationResponseAnthropic, false, installation, key, requestedModel, "completion-request", turnLoopResult{TurnType: tt, HardPinned: true, PinRole: "physical_hmm_history", Decision: router.Decision{Provider: providers.ProviderAnthropic, Model: requestedModel}})
		_, err := w.Write([]byte(completionStreams[0].body))
		require.NoError(t, err)
		if tt != turntype.MainLoop && tt != turntype.ToolResult {
			require.False(t, owns)
			require.Nil(t, gate)
			require.NotEmpty(t, rec.Body.String())
			continue
		}
		require.True(t, owns)
		require.Empty(t, rec.Body.String())
		require.NoError(t, gate.finish(context.Background(), nil))
		require.Len(t, store.records, 1)
		require.Equal(t, key[:], store.records[0].SessionKey)
		require.Equal(t, installation.String(), store.records[0].InstallationID)
		require.Equal(t, "default_mid", store.records[0].Role)
		require.Equal(t, "completion-request", store.records[0].RequestID)
	}
}

type completionSocketFailure struct{ *httptest.ResponseRecorder }

func (w completionSocketFailure) Write([]byte) (int, error) {
	return 0, errors.New("client disconnected")
}

func TestFeedbackCompletionKeepsHistoryAfterConfirmedCommitAndSocketFailure(t *testing.T) {
	store := &completionTestStore{}
	gate := newFeedbackCompletion(completionSocketFailure{httptest.NewRecorder()}, translate.EscalationResponseAnthropic, false)
	gate.active, gate.store = true, store
	_, err := gate.Write([]byte(completionStreams[0].body))
	require.NoError(t, err)
	require.ErrorContains(t, gate.finish(context.Background(), nil), "client disconnected")
	require.Len(t, store.records, 1)
}

func TestDurableFeedbackUnavailableClassification(t *testing.T) {
	cls, ok := ClassifyDispatchError(ErrFeedbackUnavailable)
	require.True(t, ok)
	require.Equal(t, http.StatusServiceUnavailable, cls.Status)
	require.Contains(t, cls.Message, "feedback")
}

func TestFeedbackCompletionRejectsInvalidNonstreamSuffix(t *testing.T) {
	for _, tc := range completionStreams {
		rec := httptest.NewRecorder()
		store := &completionTestStore{}
		gate := newFeedbackCompletion(rec, tc.format, false)
		gate.active, gate.store = true, store
		_, err := gate.Write([]byte(tc.body + ` {"extra":"body"}`))
		require.NoError(t, err)
		require.Error(t, gate.finish(context.Background(), nil))
		require.Empty(t, rec.Body.String())
		require.Empty(t, store.records)
	}
}

func TestInactiveFeedbackCompletionDoesNotDuplicateStreamFailure(t *testing.T) {
	rec := httptest.NewRecorder()
	gate := newFeedbackCompletion(rec, translate.EscalationResponseResponses, true)
	failure := errors.New("auxiliary request failed")
	wire := "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\"}}\n\n"
	_, err := gate.Write([]byte(wire))
	require.NoError(t, err)
	require.ErrorIs(t, gate.finish(context.Background(), failure), failure)
	require.Equal(t, wire, rec.Body.String())
}

func TestFeedbackCompletionPreservesBufferedHTTPFailure(t *testing.T) {
	for _, active := range []bool{false, true} {
		rec := httptest.NewRecorder()
		gate := newFeedbackCompletion(rec, translate.EscalationResponseResponses, false)
		gate.active = active
		store := &completionTestStore{}
		gate.store = store
		gate.WriteHeader(http.StatusBadRequest)
		body := `{"error":{"message":"unsupported parameter"}}`
		_, err := gate.Write([]byte(body))
		require.NoError(t, err)
		failure := errors.New("provider rejected request")
		require.ErrorIs(t, gate.finish(context.Background(), failure), failure)
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Equal(t, body, rec.Body.String())
		require.Empty(t, store.records)
	}
}

func TestFeedbackCompletionPreservesProtocolContentTypes(t *testing.T) {
	for _, tc := range completionStreams {
		for _, stream := range []bool{false, true} {
			for _, active := range []bool{false, true} {
				for _, writeHeader := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/stream=%v/active=%v/header=%v", tc.name, stream, active, writeHeader), func(t *testing.T) {
						rec := httptest.NewRecorder()
						gate := newFeedbackCompletion(rec, tc.format, stream)
						gate.active, gate.store = active, &completionTestStore{}
						gate.Header().Set("Content-Type", "text/html")
						if writeHeader {
							gate.WriteHeader(http.StatusOK)
						}
						body, contentType := tc.body, "application/json"
						if stream {
							body, contentType = tc.prefix+tc.suffix, "text/event-stream"
						}
						body = strings.ReplaceAll(body, "hello", "<script>alert(1)</script>")
						_, err := gate.Write([]byte(body))
						require.NoError(t, err)
						require.NoError(t, gate.finish(context.Background(), nil))
						require.Equal(t, contentType, rec.Result().Header.Get("Content-Type"))
						require.Equal(t, "nosniff", rec.Result().Header.Get("X-Content-Type-Options"))
						require.Equal(t, body, rec.Body.String(), "do not HTML-escape protocol payloads")
					})
				}
			}
		}
	}
}

func TestFeedbackCompletionSetsContentTypeBeforeEmptyFlush(t *testing.T) {
	rec := httptest.NewRecorder()
	gate := newFeedbackCompletion(rec, translate.EscalationResponseResponses, true)
	gate.Header().Set("Content-Type", "text/html")
	gate.Flush()
	require.Equal(t, "text/event-stream", rec.Result().Header.Get("Content-Type"))
	require.Equal(t, "nosniff", rec.Result().Header.Get("X-Content-Type-Options"))
}

func TestFeedbackCompletionRejectsMalformedGeminiBlocks(t *testing.T) {
	for _, body := range []string{
		`{"promptFeedback":{}}`,
		`{"promptFeedback":{"blockReason":""}}`,
		`{"promptFeedback":{"blockReason":"BLOCK_REASON_UNSPECIFIED"}}`,
		`{"promptFeedback":{"blockReason":5}}`,
		`{"candidates":{},"promptFeedback":{"blockReason":"SAFETY"}}`,
		`{"candidates":[{"index":0}],"promptFeedback":{"blockReason":"SAFETY"}}`,
		`{"promptFeedback":{"blockReason":"SAFETY"}`, // truncated JSON
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", body, stream), func(t *testing.T) {
				rec := httptest.NewRecorder()
				store := &completionTestStore{}
				gate := newFeedbackCompletion(rec, translate.EscalationResponseGemini, stream)
				gate.active, gate.store = true, store
				wire := body
				if stream {
					wire = "data: " + wire + "\n\n"
				}
				_, err := gate.Write([]byte(wire))
				require.NoError(t, err)
				require.Error(t, gate.finish(context.Background(), nil))
				require.Empty(t, store.records)
				if !stream {
					require.Empty(t, rec.Body.String())
				}
			})
		}
	}
}

func TestFeedbackCompletionGeminiBlockTerminatesWithoutSuccessHistory(t *testing.T) {
	const block = "data: {\"promptFeedback\":{\"blockReason\":\"SAFETY\"}}\n\n"
	const output = "data: {\"candidates\":[{\"index\":0,\"content\":{\"parts\":[{\"text\":\"hello\"}]}}]}\n\n"
	for _, trailing := range []bool{false, true} {
		wire := output + block
		if trailing {
			wire = block + output
		}
		rec := httptest.NewRecorder()
		store := &completionTestStore{}
		gate := newFeedbackCompletion(rec, translate.EscalationResponseGemini, true)
		gate.active, gate.store = true, store
		_, err := gate.Write([]byte(wire))
		require.NoError(t, err)
		err = gate.finish(context.Background(), nil)
		require.Empty(t, store.records)
		if trailing {
			require.Error(t, err)
			require.Contains(t, rec.Body.String(), "error")
		} else {
			require.NoError(t, err)
			require.Equal(t, wire, rec.Body.String(), "a routing marker may precede the prompt block")
		}
	}
}
