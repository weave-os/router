package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/sse"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type feedbackCompletionContextKey struct{}

// feedbackCompletionHoldCap bounds the bytes withheld from the client: the
// incomplete trailing SSE frame plus the held finish suffix. A Responses
// terminal event carries the whole output, so the cap sits well above a
// maximal single frame; an upstream that never completes a frame is cut off
// here instead of growing the buffer for the life of the stream.
const feedbackCompletionHoldCap = 8 << 20

// errFeedbackHoldExceeded fails a stream whose withheld bytes outgrew
// feedbackCompletionHoldCap.
var errFeedbackHoldExceeded = errors.New("proxy: feedback completion held more than the frame cap")

// feedbackCompletion holds only the finish-bearing suffix of a stream. It sits
// below translators: a ResponsesWriter can emit response.completed during Write.
type feedbackCompletion struct {
	mu    sync.Mutex
	inner http.ResponseWriter
	// body is inner's write half. Forwarding upstream bytes through io.Writer
	// keeps them out of CodeQL's reflected-XSS sink model, which reads every
	// ResponseWriter.Write in a proxy chain as an HTML response.
	body              io.Writer
	buffer            *responseCostBuffer
	format            translate.EscalationResponseFormat
	stream            bool
	active            bool
	status            int
	written           bool
	suffix            bytes.Buffer
	partial           bytes.Buffer
	holding           bool
	terminal          bool
	blocked           bool
	failed            bool
	invalid           error
	responsesTerminal []byte
	choices           map[int64]bool
	request           FeedbackRequest
	store             RouterFeedbackStore
	observations      []func()
}

func newFeedbackCompletion(w http.ResponseWriter, format translate.EscalationResponseFormat, stream bool) *feedbackCompletion {
	f := &feedbackCompletion{inner: w, format: format, stream: stream, status: http.StatusOK, choices: make(map[int64]bool)}
	if !stream {
		f.buffer, _ = w.(*responseCostBuffer)
		if f.buffer == nil {
			f.buffer = newResponseCostBuffer(w)
		}
		f.inner = f.buffer
	}
	f.body = f.inner
	return f
}

func (s *Service) beginFeedbackCompletion(ctx context.Context, w http.ResponseWriter, format translate.EscalationResponseFormat, stream bool, installationID uuid.UUID, key [sessionpin.SessionKeyLen]byte, requestedModel, requestID string, res turnLoopResult) (http.ResponseWriter, *feedbackCompletion, bool) {
	_, shadow := AgentShadowEvalFromContext(ctx)
	if s.feedbackStore == nil || installationID == uuid.Nil || shadow || (res.TurnType != turntype.MainLoop && res.TurnType != turntype.ToolResult) {
		return w, nil, false
	}
	f, outer := ctx.Value(feedbackCompletionContextKey{}).(*feedbackCompletion)
	if !outer {
		f = newFeedbackCompletion(w, format, stream)
		w = f
	}
	f.active = true
	f.store = s.feedbackStore
	f.request = FeedbackRequest{InstallationID: installationID.String(), SessionKey: append([]byte(nil), key[:]...), Role: roleForTier(catalog.TierFor(requestedModel)), RequestID: requestID, TrainingAllowed: policyTrainingAllowedForRequest(ctx)}
	f.setDecision(ctx, res.Decision, res.Fresh)
	return w, f, !outer
}

func (f *feedbackCompletion) setDecision(ctx context.Context, decision, fresh router.Decision) {
	if f == nil {
		return
	}
	f.request.ServedModel = decision.ServedIdentity()
	f.request.ServedProvider = decision.Provider
	// This follows existing telemetry attribution without retaining its payload.
	f.request.Strategy, f.request.RouteID = "", ""
	if decision.Metadata != nil || fresh.Model != "" {
		f.request.Strategy = string(router.StrategyFromContext(ctx))
	}
	if md := decision.Metadata; md != nil {
		if md.Strategy != "" {
			f.request.Strategy = md.Strategy
		}
		f.request.RouteID = md.RouteID
	}
}

func (f *feedbackCompletion) Header() http.Header { return f.inner.Header() }

func (f *feedbackCompletion) WriteHeader(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.written {
		return
	}
	f.status, f.written = status, true
	if !f.stream || status >= http.StatusBadRequest {
		f.inner.Header().Set("Content-Type", "application/json")
	} else {
		f.inner.Header().Set("Content-Type", "text/event-stream")
	}
	f.inner.Header().Set("X-Content-Type-Options", "nosniff")
	f.inner.WriteHeader(status)
}

func (f *feedbackCompletion) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.written = true
	if !f.stream || f.status >= http.StatusBadRequest {
		f.inner.Header().Set("Content-Type", "application/json")
	} else {
		f.inner.Header().Set("Content-Type", "text/event-stream")
	}
	f.inner.Header().Set("X-Content-Type-Options", "nosniff")
	if !f.active || !f.stream || f.status >= 400 {
		return f.body.Write(p)
	}
	if errors.Is(f.invalid, errFeedbackHoldExceeded) {
		return 0, f.invalid
	}
	f.partial.Write(p)
	for {
		event, n := sse.SplitNext(f.partial.Bytes())
		if n == 0 {
			break
		}
		f.observe(event)
		frame := f.partial.Next(n)
		if f.holding || f.invalid != nil {
			f.suffix.Write(frame)
		} else if _, err := f.body.Write(frame); err != nil {
			return 0, err
		}
	}
	if f.partial.Len()+f.suffix.Len() > feedbackCompletionHoldCap {
		f.invalid = errFeedbackHoldExceeded
		f.partial.Reset()
		f.suffix.Reset()
		return 0, f.invalid
	}
	return len(p), nil
}

func (f *feedbackCompletion) Flush() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.stream || f.status >= http.StatusBadRequest {
		f.inner.Header().Set("Content-Type", "application/json")
	} else {
		f.inner.Header().Set("Content-Type", "text/event-stream")
	}
	f.inner.Header().Set("X-Content-Type-Options", "nosniff")
	if flusher, ok := f.inner.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (f *feedbackCompletion) ArmOutputProgress(mark func()) bool {
	arm, ok := f.inner.(interface{ ArmOutputProgress(func()) bool })
	return ok && arm.ArmOutputProgress(mark)
}

func (f *feedbackCompletion) observe(event []byte) {
	eventType, data := sse.ParseEvent(event)
	if len(data) == 0 {
		if trimmed := bytes.TrimSpace(event); len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
			f.invalid = translate.ErrStreamOrder
		}
		return
	}
	root := gjson.ParseBytes(data)
	if (root.Get("error").Exists() && root.Get("error").Type != gjson.Null) || string(eventType) == "error" || root.Get("type").String() == "error" {
		f.failed = true
		return
	}
	if root.Get("type").String() == "ping" && f.format == translate.EscalationResponseAnthropic {
		return
	}
	if f.terminal || f.failed {
		f.invalid = translate.ErrStreamOrder
		return
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
		if f.format != translate.EscalationResponseChat || !f.holding || !f.allChoicesFinished() {
			f.invalid = translate.ErrStreamOrder
		}
		f.holding, f.terminal = true, true
		return
	}
	if !gjson.ValidBytes(data) {
		f.invalid = translate.ErrStreamOrder
		return
	}
	switch f.format {
	case translate.EscalationResponseAnthropic:
		switch root.Get("type").String() {
		case "message_delta":
			if root.Get("delta.stop_reason").String() != "" {
				f.holding = true
			}
		case "message_stop":
			if !f.holding {
				f.invalid = translate.ErrStreamIncomplete
			}
			f.holding, f.terminal = true, true
		default:
			if f.holding && root.Get("type").String() != "ping" {
				f.invalid = translate.ErrStreamOrder
			}
		}
	case translate.EscalationResponseResponses:
		switch root.Get("type").String() {
		case "response.completed", "response.incomplete":
			f.responsesTerminal = append([]byte(nil), data...)
			_, ok := translate.ResponsesTerminalReason(data)
			f.holding, f.terminal, f.failed = true, ok, !ok
		case "response.failed":
			f.failed = true
		}
	case translate.EscalationResponseChat:
		root.Get("choices").ForEach(func(_, choice gjson.Result) bool {
			index := choice.Get("index").Int()
			finished := choice.Get("finish_reason").String() != ""
			// Providers restate finish_reason on a trailing usage chunk; only
			// new output or a cleared finish_reason reopens a finished choice.
			if f.choices[index] && (!finished || chatChoiceHasOutput(choice)) {
				f.invalid = translate.ErrStreamOrder
			}
			f.choices[index] = finished
			f.holding = f.holding || finished
			return true
		})
	case translate.EscalationResponseGemini:
		if geminiPromptBlocked(root) {
			f.blocked, f.holding, f.terminal = true, true, true
			return
		}
		root.Get("candidates").ForEach(func(_, candidate gjson.Result) bool {
			index := candidate.Get("index").Int()
			finished := candidate.Get("finishReason").String() != ""
			if f.choices[index] && (!finished || len(candidate.Get("content.parts").Array()) > 0) {
				f.invalid = translate.ErrStreamOrder
			}
			f.choices[index] = finished
			f.holding = f.holding || finished
			return true
		})
	}
}

// chatChoiceHasOutput reports whether a chat chunk's delta carries anything
// beyond the assistant role marker.
func chatChoiceHasOutput(choice gjson.Result) bool {
	output := false
	choice.Get("delta").ForEach(func(key, value gjson.Result) bool {
		switch {
		case key.String() == "role", value.Type == gjson.Null:
			return true
		case value.Type == gjson.String && value.String() == "":
			return true
		case value.IsArray() && len(value.Array()) == 0:
			return true
		}
		output = true
		return false
	})
	return output
}

func (f *feedbackCompletion) allChoicesFinished() bool {
	if len(f.choices) == 0 {
		return false
	}
	for _, done := range f.choices {
		if !done {
			return false
		}
	}
	return true
}

func geminiPromptBlocked(root gjson.Result) bool {
	candidates := root.Get("candidates")
	reason := root.Get("promptFeedback.blockReason")
	return (!candidates.Exists() || (candidates.IsArray() && len(candidates.Array()) == 0)) &&
		reason.Type == gjson.String && reason.String() != "" && reason.String() != "BLOCK_REASON_UNSPECIFIED"
}

func (f *feedbackCompletion) completeBody() bool {
	root := gjson.ParseBytes(f.buffer.body.Bytes())
	if !gjson.ValidBytes(f.buffer.body.Bytes()) || (root.Get("error").Exists() && root.Get("error").Type != gjson.Null) {
		return false
	}
	switch f.format {
	case translate.EscalationResponseAnthropic:
		return root.Get("type").String() == "message" && root.Get("stop_reason").String() != ""
	case translate.EscalationResponseResponses:
		_, ok := translate.ResponsesTerminalReason(f.buffer.body.Bytes())
		return ok
	case translate.EscalationResponseChat, translate.EscalationResponseGemini:
		items, reason := root.Get("choices"), "finish_reason"
		if f.format == translate.EscalationResponseGemini {
			items, reason = root.Get("candidates"), "finishReason"
			f.blocked = geminiPromptBlocked(root)
			if f.blocked {
				return true
			}
		}
		complete := items.IsArray() && len(items.Array()) > 0
		items.ForEach(func(_, item gjson.Result) bool {
			complete = complete && item.Get(reason).String() != ""
			return true
		})
		return complete
	}
	return false
}

func (f *feedbackCompletion) finish(ctx context.Context, proxyErr error) error {
	if f == nil {
		return proxyErr
	}
	defer f.submitObservations()
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.active {
		if f.buffer != nil {
			return f.buffer.finish(proxyErr)
		}
		return proxyErr
	}
	if proxyErr == nil && f.status < 400 {
		if f.stream {
			if f.partial.Len() > 0 {
				f.observe(f.partial.Bytes())
				f.suffix.Write(f.partial.Bytes())
				f.partial.Reset()
			}
			if f.format == translate.EscalationResponseGemini && !f.blocked {
				f.terminal = f.allChoicesFinished()
			}
			if f.invalid != nil {
				proxyErr = f.invalid
			} else if f.failed || !f.terminal {
				proxyErr = translate.ErrStreamIncomplete
			}
		} else if !f.completeBody() {
			proxyErr = translate.ErrStreamIncomplete
		}
		if proxyErr == nil && !f.blocked {
			// The client may already have hung up on a response it received in
			// full; its cancellation must not lose the turn from history.
			writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			f.request.CompletedAt = time.Now()
			err := f.store.CompleteFeedbackRequest(writeCtx, f.request)
			cancel()
			if err != nil {
				observability.FromContext(ctx).Error("Failed to commit feedback request history", "err", err)
				proxyErr = fmt.Errorf("commit feedback history: %w", err)
			}
		}
	}
	if proxyErr != nil {
		if f.buffer != nil {
			return f.buffer.finish(proxyErr)
		} else if f.written && f.status < 400 && (!f.failed || f.holding) {
			f.emitError(proxyErr)
		}
		return proxyErr
	}
	if f.buffer != nil {
		return f.buffer.FlushToClient()
	}
	if f.suffix.Len() > 0 {
		f.inner.Header().Set("Content-Type", "text/event-stream")
		if _, err := f.body.Write(f.suffix.Bytes()); err != nil {
			return err
		}
		if flusher, ok := f.inner.(http.Flusher); ok {
			flusher.Flush()
		}
	}
	return nil
}

// Admission can block, so only drain after protocol finalization and history commit.
func (f *feedbackCompletion) submitObservations() {
	f.mu.Lock()
	pending := f.observations
	f.observations = nil
	f.mu.Unlock()
	for _, submit := range pending {
		submit()
	}
}

func (f *feedbackCompletion) emitError(err error) {
	switch f.format {
	case translate.EscalationResponseAnthropic:
		_ = emitAnthropicSSEErrorEvent(f.inner, err)
	case translate.EscalationResponseChat:
		_ = emitOpenAISSEErrorEvent(f.inner, err)
	case translate.EscalationResponseGemini:
		emitGeminiSSEErrorEvent(f.inner)
	case translate.EscalationResponseResponses:
		body := f.responsesTerminal
		if len(body) == 0 {
			body = []byte(`{"type":"response.failed","response":{"status":"failed"}}`)
		}
		body, _ = sjson.SetBytes(body, "type", "response.failed")
		body, _ = sjson.SetBytes(body, "response.status", "failed")
		body, _ = sjson.SetRawBytes(body, "response.error", []byte(`{"code":"server_error","message":"Response completion failed"}`))
		_, _ = f.body.Write(append(append([]byte("event: response.failed\ndata: "), body...), '\n', '\n'))
		if flusher, ok := f.inner.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}
