package translate

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"weave-os/router/internal/router"
	"weave-os/router/internal/sse"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ResponsesConversion is the typed ingress result for a Responses request.
// OriginalBody remains byte-for-byte client input so native OpenAI-family
// dispatch can preserve extensions that Chat Completions cannot represent.
type ResponsesConversion struct {
	Body         []byte
	OriginalBody []byte
	Stream       bool
	Model        string
	// CodexFeedbackSkill reports a user-invoked $rf/$router-feedback skill
	// whose emitted directive arrived in a later tool-result item.
	CodexFeedbackSkill bool
	Requirements       router.TranslationRequirements
	Report             []ResponseTransform
	ToolMappings       map[string]ResponsesToolMapping
	// TitleGeneration reports the native Codex title-generation shape (a
	// closed object containing only the required string title). It is consumed
	// only when the proxy has independently identified the Codex client.
	TitleGeneration bool
}

// ResponseTransform reports an ingress conversion outcome with a stable code.
// Details stay out of metric labels; callers may emit them in structured logs.
type ResponseTransform struct {
	Code   string
	Action string
	Path   string
}

// ResponsesToChatCompletions converts an OpenAI Responses API request into a
// Chat Completions request so the existing proxy path can dispatch it
// unchanged. Returns the rewritten body, whether streaming was requested, and
// the requested model (empty if absent). Handles only the subset of the
// Responses spec Codex actually emits: instructions, input items (message /
// function_call / function_call_output), tools, tool_choice,
// max_output_tokens, temperature, top_p, parallel_tool_calls, metadata.
//
// Codex's `reasoning` field is dropped intentionally: this runs before
// routing, so the served provider (and its native thinking knob) is unknown.
// Forwarding it as `reasoning_effort` broke every non-Gemini model — Codex
// sends invalid values (e.g. "none"), and gpt-5.x rejects `reasoning_effort`
// alongside tools, both causing an upstream 400 mid-stream. Per-provider
// reasoning is still driven downstream from the request's own signals.
func ResponsesToChatCompletions(body []byte) ([]byte, bool, string, error) {
	result, err := ConvertResponsesToChatCompletions(body)
	if err != nil {
		return nil, false, "", err
	}
	return result.Body, result.Stream, result.Model, nil
}

// ErrResponsesChatCompletionsBody is returned when a Chat Completions body
// (top-level "messages") is posted to /v1/responses; such a body routes on
// zero content and reaches the upstream Responses endpoint verbatim as a 400.
var ErrResponsesChatCompletionsBody = errors.New("unsupported parameter: 'messages'. In the Responses API, this parameter has moved to 'input'")

// validateResponsesRequest rejects bodies the Responses surface cannot project.
func validateResponsesRequest(body []byte) error {
	if err := validateJSONObject(body); err != nil {
		return err
	}
	if gjson.GetBytes(body, "messages").Exists() {
		return ErrResponsesChatCompletionsBody
	}
	return nil
}

// ConvertResponsesToChatCompletions projects a Responses request into Chat
// Completions for routing. Extensions that cannot be faithfully represented
// are marked native-only so proxy can dispatch the original bytes losslessly.
func ConvertResponsesToChatCompletions(body []byte) (ResponsesConversion, error) {
	if err := validateResponsesRequest(body); err != nil {
		return ResponsesConversion{}, err
	}

	root := gjson.ParseBytes(body)
	result := ResponsesConversion{
		OriginalBody: body,
		Requirements: router.TranslationRequirements{
			SourceFormat: router.WireFormatOpenAI,
			Endpoint:     router.EndpointOpenAIResponses,
		},
	}
	result.Requirements.Images = gjson.GetBytes(body, "input").Exists() && containsAnyKey(body, "image_url", "input_image")
	result.Requirements.Audio, result.Requirements.Files = openAIMediaRequirements(body)
	result.Requirements.CitationsOrSearch = len(nativeServerToolsFromBody(body, FormatOpenAI)) > 0
	result.Requirements.StructuredOutput = root.Get("text.format").Exists() || root.Get("response_format").Exists()
	result.TitleGeneration = requestRootRequestsCodexTitleSchema(root)
	out := map[string]any{}

	model := root.Get("model").Str
	result.Model = model
	if model != "" {
		out["model"] = model
	}

	stream := root.Get("stream").Bool()
	result.Stream = stream
	result.Requirements.UsageDetail = stream && root.Get("stream_options.include_usage").Bool()
	out["stream"] = stream
	if stream {
		out["stream_options"] = map[string]any{"include_usage": true}
	}

	messages := make([]map[string]any, 0, 8)
	if instructions := root.Get("instructions").Str; instructions != "" {
		messages = append(messages, map[string]any{
			"role":    "system",
			"content": instructions,
		})
	}

	input := root.Get("input")
	switch {
	case input.IsArray():
		for index, item := range input.Array() {
			itemType := item.Get("type").Str
			if itemType == "function_call" || itemType == "function_call_output" {
				result.Requirements.FunctionTools = true
			}
			if itemType == "reasoning" {
				result.Requirements.ReasoningReplay = true
				result.Requirements.NativeOnly = true
				result.Report = append(result.Report, ResponseTransform{Code: "responses_reasoning_native_only", Action: "preserved", Path: "input." + strconv.Itoa(index)})
				continue
			}
			msgs, err := responsesInputItemToMessages(item)
			if err != nil {
				result.Requirements.NativeOnly = true
				result.Report = append(result.Report, ResponseTransform{Code: "responses_unknown_input_native_only", Action: "preserved", Path: "input." + strconv.Itoa(index)})
				continue
			}
			messages = append(messages, msgs...)
		}
	case input.Type == gjson.String:
		messages = append(messages, map[string]any{
			"role":    "user",
			"content": input.Str,
		})
	}
	out["messages"] = messages

	hasTools := false
	if tools := root.Get("tools"); tools.IsArray() {
		converted := make([]map[string]any, 0, len(tools.Array()))
		for index, t := range tools.Array() {
			// Responses tools are flat: {type, name, description, parameters, strict}.
			// Chat Completions nest under {type:"function", function:{...}}.
			if t.Get("type").Str != "function" {
				result.Requirements.CustomTools = true
				result.Requirements.NativeOnly = true
				result.Report = append(result.Report, ResponseTransform{Code: "responses_non_function_tool_native_only", Action: "preserved", Path: "tools." + strconv.Itoa(index)})
				continue
			}
			result.Requirements.FunctionTools = true
			fn := map[string]any{}
			if name := t.Get("name").Str; name != "" {
				fn["name"] = name
			} else if name := t.Get("function.name").Str; name != "" {
				fn["name"] = name
			}
			if desc := t.Get("description").Str; desc != "" {
				fn["description"] = desc
			} else if desc := t.Get("function.description").Str; desc != "" {
				fn["description"] = desc
			}
			if params := t.Get("parameters"); params.Exists() {
				fn["parameters"] = json.RawMessage(params.Raw)
			} else if params := t.Get("function.parameters"); params.Exists() {
				fn["parameters"] = json.RawMessage(params.Raw)
			}
			if strict := t.Get("strict"); strict.Exists() {
				fn["strict"] = strict.Bool()
			}
			converted = append(converted, map[string]any{
				"type":     "function",
				"function": fn,
			})
		}
		if len(converted) > 0 {
			out["tools"] = converted
			hasTools = true
		}
	}

	if tc := root.Get("tool_choice"); tc.Exists() && hasTools {
		out["tool_choice"] = json.RawMessage(tc.Raw)
	}
	if pt := root.Get("parallel_tool_calls"); pt.Exists() {
		out["parallel_tool_calls"] = pt.Bool()
	}
	if temp := root.Get("temperature"); temp.Exists() {
		out["temperature"] = temp.Num
	}
	if topP := root.Get("top_p"); topP.Exists() {
		out["top_p"] = topP.Num
	}
	if max := root.Get("max_output_tokens"); max.Exists() {
		out["max_completion_tokens"] = max.Int()
	}
	if md := root.Get("metadata"); md.IsObject() {
		out["metadata"] = json.RawMessage(md.Raw)
	}

	bodyOut, err := json.Marshal(out)
	if err != nil {
		return ResponsesConversion{}, fmt.Errorf("marshal chat completions: %w", err)
	}
	result.Body = bodyOut
	return result, nil
}

// responsesInputItemToMessages flattens a single Responses input item into one
// or more Chat Completions messages. Returns ([], nil) for item types we don't
// recognize. Unknown shapes are rejected from the Chat projection; callers
// may still retain them through a native Responses route.
func responsesInputItemToMessages(item gjson.Result) ([]map[string]any, error) {
	itemType := item.Get("type").Str
	// Some Responses clients omit "type" and send a bare chat-style {role, content}.
	if itemType == "" {
		if role := item.Get("role").Str; role != "" {
			itemType = "message"
		}
	}

	switch itemType {
	case "message":
		role := item.Get("role").Str
		if role == "" {
			role = "user"
		}
		text, toolCalls := responsesContentToChatContent(item.Get("content"), role)
		// A badge-only assistant message strips to nothing; keeping the shell
		// puts a blank assistant turn ahead of the real function_call, which
		// some providers reject.
		if role == "assistant" && text == "" && len(toolCalls) == 0 {
			return nil, nil
		}
		msg := map[string]any{"role": role}
		if text != "" || len(toolCalls) == 0 {
			msg["content"] = text
		}
		if len(toolCalls) > 0 {
			msg["tool_calls"] = toolCalls
		}
		return []map[string]any{msg}, nil

	case "function_call":
		call := map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":      item.Get("name").Str,
				"arguments": item.Get("arguments").Str,
			},
		}
		if id := item.Get("call_id").Str; id != "" {
			call["id"] = id
		} else if id := item.Get("id").Str; id != "" {
			call["id"] = id
		}
		return []map[string]any{{
			"role":       "assistant",
			"content":    "",
			"tool_calls": []map[string]any{call},
		}}, nil

	case "function_call_output":
		out := map[string]any{
			"role":    "tool",
			"content": item.Get("output").Str,
		}
		if id := item.Get("call_id").Str; id != "" {
			out["tool_call_id"] = id
		}
		return []map[string]any{out}, nil

	case "reasoning":
		return nil, fmt.Errorf("responses reasoning item requires native Responses routing")
	}
	return nil, fmt.Errorf("unsupported Responses input item type %q", itemType)
}

// responsesBadgePattern matches the routing badge ResponsesWriter prepends to
// the first assistant text delta. Stripped on ingress so it doesn't accumulate
// in history (defeats prompt-cache reuse) or leak router-injected content
// upstream.
var responsesBadgePattern = regexp.MustCompile(`(?is)\A(?:\*\*WEAVE ROUTER\*\* — .*?\n\n|✦ \*\*WEAVE ROUTER\*\* → .*?\n\n)`)

// codexResponsesBadgeSentinel is an invisible router-owned prefix that
// distinguishes injected badge text from user-authored assistant prose.
const codexResponsesBadgeSentinel = "\u2063\u2060\u2063\u2060"

var codexResponsesBadgePattern = regexp.MustCompile(
	`(?is)\A` + regexp.QuoteMeta(codexResponsesBadgeSentinel) +
		`(?:\*\*WEAVE ROUTER\*\* — .*?\n\n|✦ \*\*WEAVE ROUTER\*\* → .*?\n\n)`,
)

// StripRoutingBadgeFromResponsesInput removes a provenance-marked router badge
// from assistant items. Call only for clients opted into the Codex badge.
func StripRoutingBadgeFromResponsesInput(body []byte) ([]byte, error) {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, nil
	}

	out := body
	changed := false
	var emptied []int
	for itemIndex, item := range input.Array() {
		itemType := item.Get("type").Str
		if itemType != "message" && !(itemType == "" && item.Get("role").Str != "") {
			continue
		}
		if item.Get("role").Str != "assistant" {
			continue
		}

		content := item.Get("content")
		if content.Type == gjson.String {
			stripped := codexResponsesBadgePattern.ReplaceAllString(content.Str, "")
			if stripped == content.Str {
				continue
			}
			if stripped == "" {
				emptied = append(emptied, itemIndex)
				changed = true
				continue
			}
			var err error
			out, err = sjson.SetBytes(out, "input."+strconv.Itoa(itemIndex)+".content", stripped)
			if err != nil {
				return nil, fmt.Errorf("strip Responses routing badge from string content: %w", err)
			}
			changed = true
			continue
		}
		if !content.IsArray() {
			continue
		}
	contentParts:
		for partIndex, part := range content.Array() {
			switch part.Get("type").Str {
			case "input_text", "output_text", "text":
				text := part.Get("text").Str
				stripped := codexResponsesBadgePattern.ReplaceAllString(text, "")
				if stripped != text {
					var err error
					path := "input." + strconv.Itoa(itemIndex) + ".content." + strconv.Itoa(partIndex) + ".text"
					out, err = sjson.SetBytes(out, path, stripped)
					if err != nil {
						return nil, fmt.Errorf("strip Responses routing badge from content part: %w", err)
					}
					changed = true
				}
				if stripped == "" && !responsesContentHasBody(content, partIndex) {
					emptied = append(emptied, itemIndex)
					changed = true
				}
				// The egress marker is only ever prepended to the first text part.
				// Do not strip a marker-like string from later assistant content.
				break contentParts
			}
		}
	}
	if !changed {
		return body, nil
	}
	// Descending: an earlier delete would shift the indices still pending.
	for i := len(emptied) - 1; i >= 0; i-- {
		var err error
		out, err = sjson.DeleteBytes(out, "input."+strconv.Itoa(emptied[i]))
		if err != nil {
			return nil, fmt.Errorf("drop badge-only Responses input item: %w", err)
		}
	}
	return out, nil
}

// StripFeedbackFooterFromResponsesInput removes the rating hint from assistant
// text items so a subsequent native Codex turn does not echo it upstream.
func StripFeedbackFooterFromResponsesInput(body []byte) ([]byte, error) {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, nil
	}
	out := body
	changed := false
	for itemIndex, item := range input.Array() {
		itemType := item.Get("type").Str
		if itemType != "message" && !(itemType == "" && item.Get("role").Str != "") {
			continue
		}
		if item.Get("role").Str != "assistant" {
			continue
		}
		content := item.Get("content")
		if content.Type == gjson.String {
			stripped := feedbackFooterPattern.ReplaceAllString(content.Str, "")
			if stripped == content.Str {
				continue
			}
			var err error
			out, err = sjson.SetBytes(out, "input."+strconv.Itoa(itemIndex)+".content", stripped)
			if err != nil {
				return nil, fmt.Errorf("strip Responses feedback footer from string content: %w", err)
			}
			changed = true
			continue
		}
		if !content.IsArray() {
			continue
		}
		for partIndex := len(content.Array()) - 1; partIndex >= 0; partIndex-- {
			part := content.Array()[partIndex]
			switch part.Get("type").Str {
			case "input_text", "output_text", "text":
				text := part.Get("text").Str
				stripped := feedbackFooterPattern.ReplaceAllString(text, "")
				if stripped == text {
					continue
				}
				path := "input." + strconv.Itoa(itemIndex) + ".content." + strconv.Itoa(partIndex) + ".text"
				var err error
				out, err = sjson.SetBytes(out, path, stripped)
				if err != nil {
					return nil, fmt.Errorf("strip Responses feedback footer from content part: %w", err)
				}
				changed = true
			}
		}
	}
	if !changed {
		return body, nil
	}
	return out, nil
}

// StripRouterCommandsFromResponsesInput removes router directives from
// function-call output items so the upstream doesn't repeat them verbatim
// instead of continuing the agent turn.
func StripRouterCommandsFromResponsesInput(body []byte) ([]byte, error) {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, nil
	}
	out := body
	changed := false
	for itemIndex, item := range input.Array() {
		typ := item.Get("type").Str
		if typ != "function_call_output" && typ != "custom_tool_call_output" {
			continue
		}
		output := item.Get("output")
		if output.Type == gjson.String {
			stripped, found := stripRouterCommandText(output.Str)
			if !found {
				continue
			}
			var err error
			out, err = sjson.SetBytes(out, "input."+strconv.Itoa(itemIndex)+".output", stripped)
			if err != nil {
				return nil, fmt.Errorf("strip router command from Responses tool output: %w", err)
			}
			changed = true
			continue
		}
		if !output.IsArray() {
			continue
		}
		for partIndex, part := range output.Array() {
			if part.Get("type").Str != "input_text" {
				continue
			}
			stripped, found := stripRouterCommandText(part.Get("text").Str)
			if !found {
				continue
			}
			var err error
			out, err = sjson.SetBytes(out, "input."+strconv.Itoa(itemIndex)+".output."+strconv.Itoa(partIndex)+".text", stripped)
			if err != nil {
				return nil, fmt.Errorf("strip router command from Responses tool output part: %w", err)
			}
			changed = true
		}
	}
	if !changed {
		return body, nil
	}
	return out, nil
}

func stripRouterCommandText(text string) (string, bool) {
	if _, found, stripped := parseForceModelCommand(text); found {
		return stripped, true
	}
	if _, found, stripped := parseRouterFeedbackCommand(text); found {
		return stripped, true
	}
	return text, false
}

// responsesContentHasBody reports whether a content array carries anything
// beyond the (now stripped) part at skipIndex. Non-text parts always count.
func responsesContentHasBody(content gjson.Result, skipIndex int) bool {
	for i, part := range content.Array() {
		if i == skipIndex {
			continue
		}
		switch part.Get("type").Str {
		case "input_text", "output_text", "text":
			if part.Get("text").Str != "" {
				return true
			}
		default:
			return true
		}
	}
	return false
}

// responsesContentToChatContent flattens a content array. For assistant
// messages we may also extract tool-call shells if a client embeds them.
func responsesContentToChatContent(content gjson.Result, role string) (string, []map[string]any) {
	if content.Type == gjson.String {
		s := content.Str
		if role == "assistant" {
			s = responsesBadgePattern.ReplaceAllString(s, "")
		}
		return s, nil
	}
	if !content.IsArray() {
		return "", nil
	}
	var text strings.Builder
	var toolCalls []map[string]any
	firstAssistantTextStripped := false
	for _, part := range content.Array() {
		switch part.Get("type").Str {
		case "input_text", "output_text", "text":
			s := part.Get("text").Str
			// Strip only the assistant's first output_text part, so user/system
			// text is never touched even if it happens to start with marker bytes.
			if role == "assistant" && !firstAssistantTextStripped {
				s = responsesBadgePattern.ReplaceAllString(s, "")
				firstAssistantTextStripped = true
			}
			text.WriteString(s)
		case "refusal":
			text.WriteString(part.Get("refusal").Str)
		case "tool_use":
			if role == "assistant" {
				toolCalls = append(toolCalls, map[string]any{
					"id":   part.Get("id").Str,
					"type": "function",
					"function": map[string]any{
						"name":      part.Get("name").Str,
						"arguments": part.Get("arguments").Str,
					},
				})
			}
		}
	}
	return text.String(), toolCalls
}

// ResponsesWriter wraps an http.ResponseWriter and translates a downstream
// Chat Completions response (streaming SSE or buffered JSON) into the
// Responses API shape on the fly.
type ResponsesWriter struct {
	inner   http.ResponseWriter
	flusher http.Flusher
	bw      *bufio.Writer

	model      string // routed model, set from x-router-model when known
	responseID string
	createdAt  int64

	statusCode       int
	streaming        bool
	httpHeadersSent  bool
	passthrough      bool
	passthroughBadge bool
	buf              bytes.Buffer

	seq int64

	// Streaming state.
	headersEmitted              bool
	completedEmitted            bool
	badgePrepended              bool
	badgeText                   string
	codexBadgeProvenance        bool
	nativeBadgeTargetSelected   bool
	nativeBadgeDeltaPrepended   bool
	nativeBadgeItemID           string
	nativeBadgeOutputIndex      int64
	nativeBadgeHasOutputIndex   bool
	nativeBadgeContentIndex     int64
	nativeBadgeHasContentIndex  bool
	nativeSyntheticBadgeEmitted bool
	nativeOutputIndexShift      int64
	nativeSequenceShift         int64
	footerText                  string
	footerEmitted               bool
	sawToolCall                 bool
	nativeHeldEvents            [][]byte
	nativeFooterCommit          bool
	textItem                    *responsesTextItem
	toolItems                   map[int]*responsesToolItem
	finishReason                string
	usage                       *responsesUsage
	lifecycle                   *StreamLifecycle
	toolLedger                  *ToolCallLedger
	toolMappings                map[string]ResponsesToolMapping
}

type responsesTextItem struct {
	itemID      string
	outputIndex int
	openedPart  bool
	text        strings.Builder
	closed      bool
}

type responsesToolItem struct {
	itemID      string
	callID      string
	name        string
	mapping     ResponsesToolMapping
	outputIndex int
	arguments   strings.Builder
	opened      bool
	closed      bool
}

type responsesUsage struct {
	prompt     int64
	completion int64
	total      int64
}

var responsesIDCounter atomic.Uint64

// newResponsesID returns a short opaque ID. Codex doesn't read these for
// correctness; they exist to give the SSE stream stable handles per item.
func newResponsesID(prefix string) string {
	return prefix + "_" + strconv.FormatUint(responsesIDCounter.Add(1), 36) + strconv.FormatInt(time.Now().UnixNano()&0xffffff, 36)
}

// toolCallItemIDPrefix returns ctc_ for custom_tool_call, fc_ for function_call;
// wrong prefix causes a 400 on the next turn when the client replays the id.
func toolCallItemIDPrefix(custom bool) string {
	if custom {
		return "ctc"
	}
	return "fc"
}

// NewResponsesWriter wraps w so chat-completions output is re-emitted as
// Responses API events.
func NewResponsesWriter(w http.ResponseWriter, model string) *ResponsesWriter {
	flusher, _ := w.(http.Flusher)
	return &ResponsesWriter{
		inner:      w,
		flusher:    flusher,
		bw:         bufio.NewWriterSize(w, 8192),
		model:      model,
		responseID: newResponsesID("resp"),
		createdAt:  time.Now().Unix(),
		toolItems:  map[int]*responsesToolItem{},
		lifecycle:  NewStreamLifecycle(),
		toolLedger: NewToolCallLedger(),
	}
}

// SetToolMappings teaches the Responses writer how synthetic Chat function
// aliases map back to the function/custom tool contract Codex originally sent.
// A nil map preserves the ordinary function-call behavior for every other
// Responses client.
func (t *ResponsesWriter) SetToolMappings(mappings map[string]ResponsesToolMapping) {
	if len(mappings) == 0 {
		return
	}
	t.toolMappings = mappings
}

// WrapInner splices fn between this writer and the client writer, rebinding
// inner and bw so every byte (prelude, SSE events, final envelope) flows
// through fn — used for content-capture telemetry. Call before any writes.
func (t *ResponsesWriter) WrapInner(fn func(http.ResponseWriter) http.ResponseWriter) {
	wrapped := fn(t.inner)
	t.inner = wrapped
	t.flusher, _ = wrapped.(http.Flusher)
	t.bw = bufio.NewWriterSize(wrapped, 8192)
}

// SetBadgeText overrides the default routed-model badge prepended to the first
// assistant text delta. Empty text keeps the model-derived default.
func (t *ResponsesWriter) SetBadgeText(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	t.badgeText = text + "\n\n"
}

// EnableCodexBadgeProvenance prefixes in-band badges with the invisible
// sentinel so ingress stripping only removes router-injected text.
func (t *ResponsesWriter) EnableCodexBadgeProvenance() {
	t.codexBadgeProvenance = true
}

// SetFooterText appends the rating hint to the last assistant text of a
// naturally finished, tool-free turn. Empty text is a no-op.
func (t *ResponsesWriter) SetFooterText(text string) {
	t.footerText = text
}

func (t *ResponsesWriter) Header() http.Header { return t.inner.Header() }

// SetPassthrough switches to verbatim mode: bytes forwarded unchanged, no
// chat->Responses translation, no response.created prelude. Use when upstream
// already speaks Responses natively (Codex backend) — re-translating would
// corrupt the stream. Must be called before the first write (right after
// routing, before Prelude).
func (t *ResponsesWriter) SetPassthrough() { t.passthrough = true }

// ClearPassthrough returns the writer to translation mode, reporting false
// when bytes already committed it. Used for pre-commit fallback to chat/completions.
func (t *ResponsesWriter) ClearPassthrough() bool {
	if t.httpHeadersSent || t.headersEmitted || t.buf.Len() > 0 {
		return false
	}
	t.passthrough = false
	t.passthroughBadge = false
	return true
}

// SetPassthroughBadge switches to native Responses passthrough while opting
// into a Codex-visible badge; text-free turns get a synthetic assistant item
// so Codex has a visible surface for the badge.
func (t *ResponsesWriter) SetPassthroughBadge() {
	t.EnableCodexBadgeProvenance()
	t.passthrough = true
	t.passthroughBadge = true
}

func (t *ResponsesWriter) WriteHeader(code int) {
	// Prelude can run before routing completes, so learn the served model from
	// the proxy header at the last safe point before either response mode writes.
	if routed := t.inner.Header().Get("x-router-model"); routed != "" {
		t.model = routed
	}
	if t.passthrough {
		if t.httpHeadersSent {
			return
		}
		t.statusCode = code
		t.streaming = strings.Contains(t.inner.Header().Get("Content-Type"), "text/event-stream") && code < 400
		// Codex backend already sets text/event-stream; only drop length/encoding.
		t.inner.Header().Del("Content-Length")
		t.inner.Header().Del("Content-Encoding")
		t.inner.WriteHeader(code)
		t.httpHeadersSent = true
		return
	}
	if t.httpHeadersSent {
		return
	}
	t.statusCode = code
	ct := t.inner.Header().Get("Content-Type")
	t.streaming = strings.Contains(ct, "text/event-stream") && code < 400

	t.inner.Header().Del("Content-Length")
	t.inner.Header().Del("Content-Encoding")

	if t.streaming {
		t.inner.Header().Set("Content-Type", "text/event-stream")
		t.inner.WriteHeader(code)
		t.httpHeadersSent = true
	}
}

func (t *ResponsesWriter) Write(data []byte) (int, error) {
	n := len(data)
	if t.passthrough {
		if !t.httpHeadersSent {
			t.streaming = strings.Contains(t.inner.Header().Get("Content-Type"), "text/event-stream")
			t.statusCode = http.StatusOK
			t.inner.WriteHeader(http.StatusOK)
			t.httpHeadersSent = true
		}
		// Generic native Responses callers remain byte-for-byte passthrough. Only
		// the explicitly enabled Codex display path parses native SSE.
		if t.passthroughBadge && t.streaming {
			t.buf.Write(data)
			err := t.processPassthroughSSEBuffer()
			if err == nil {
				err = t.bw.Flush()
				if t.flusher != nil {
					t.flusher.Flush()
				}
			}
			return n, err
		}
		if t.passthroughBadge {
			t.buf.Write(data)
			// Some upstreams omit Content-Type; detect SSE from the first buffered event.
			if nativeResponsesSSEBuffer(t.buf.Bytes()) {
				t.streaming = true
				if err := t.processPassthroughSSEBuffer(); err != nil {
					return n, err
				}
				if err := t.bw.Flush(); err != nil {
					return n, err
				}
				if t.flusher != nil {
					t.flusher.Flush()
				}
			}
			return n, nil
		}
		// Forward verbatim. The upstream emits Responses natively, so there is
		// nothing to translate for clients that did not opt into the display badge.
		written, err := t.bw.Write(data)
		if err == nil {
			err = t.bw.Flush()
			if t.flusher != nil {
				t.flusher.Flush()
			}
		}
		return written, err
	}
	t.buf.Write(data)
	if !t.streaming {
		return n, nil
	}
	if !t.headersEmitted {
		if err := t.lifecycle.Start(); err != nil {
			return n, err
		}
		if err := t.emitCreated(); err != nil {
			return n, err
		}
		t.headersEmitted = true
	}
	return n, t.processSSEBuffer()
}

func nativeResponsesSSEBuffer(data []byte) bool {
	event, n := sse.SplitNext(data)
	if n == 0 {
		return false
	}
	_, payload := sse.ParseEvent(event)
	return gjson.ValidBytes(payload) && gjson.GetBytes(payload, "type").Str != ""
}

// Prelude commits headers and emits response.created immediately so Codex
// stops waiting on upstream prefill. Call right after routing when streaming
// was requested; the headersEmitted guard makes it safe to call once, with
// upstream Write emitting created later if this hasn't run yet.
func (t *ResponsesWriter) Prelude(streaming bool) error {
	// Upstream emits its own response.created in passthrough mode.
	if t.passthrough {
		return nil
	}
	if !streaming || t.headersEmitted {
		return nil
	}
	t.inner.Header().Set("Content-Type", "text/event-stream")
	t.streaming = true
	t.statusCode = http.StatusOK
	if !t.httpHeadersSent {
		t.inner.WriteHeader(http.StatusOK)
		t.httpHeadersSent = true
	}
	t.headersEmitted = true
	if err := t.lifecycle.Start(); err != nil {
		return err
	}
	return t.emitCreated()
}

func (t *ResponsesWriter) Flush() {
	if !t.streaming {
		return
	}
	_ = t.bw.Flush()
	if t.flusher != nil {
		t.flusher.Flush()
	}
}

// Finalize handles non-streaming bodies and end-of-stream completion events.
func (t *ResponsesWriter) Finalize() error {
	if t.passthrough {
		if t.passthroughBadge && t.streaming {
			if err := t.processFinalPassthroughSSETail(); err != nil {
				return err
			}
		}
		if t.passthroughBadge && !t.streaming {
			body := t.buf.Bytes()
			if rewritten, changed := t.rewriteNativeNonStreamingBody(body); changed {
				body = rewritten
			}
			if !t.httpHeadersSent {
				t.inner.Header().Set("Content-Type", "application/json")
				t.inner.WriteHeader(t.statusCode)
				t.httpHeadersSent = true
			}
			_, err := t.inner.Write(body)
			return err
		}
		// Nothing is synthesized; the upstream remains the event authority.
		return t.bw.Flush()
	}
	if t.streaming {
		if err := t.processFinalSSETail(); err != nil {
			return err
		}
		if err := t.lifecycle.EOF(); err != nil {
			if t.lifecycle.State() == StreamStarted {
				if emitErr := t.emitIncompleteFailure(); emitErr != nil {
					return emitErr
				}
			}
			return err
		}
		return nil
	}

	body := t.buf.Bytes()
	if t.statusCode >= 400 {
		t.inner.Header().Set("Content-Type", "application/json")
		t.inner.WriteHeader(t.statusCode)
		_, err := t.inner.Write(body)
		return err
	}

	translated, err := chatCompletionToResponse(body, t.responseID, t.model, t.createdAt, t.toolMappings, t.computeBadgeText(), t.footerText)
	if err != nil {
		t.inner.Header().Set("Content-Type", "application/json")
		t.inner.WriteHeader(http.StatusBadGateway)
		_, _ = t.inner.Write([]byte(`{"error":{"message":"translation failed","type":"api_error"}}`))
		return err
	}
	t.inner.Header().Set("Content-Type", "application/json")
	t.inner.WriteHeader(t.statusCode)
	_, err = t.inner.Write(translated)
	return err
}

type nativeResponsesBadgeRef struct {
	itemID          string
	outputIndex     int64
	hasOutputIndex  bool
	contentIndex    int64
	hasContentIndex bool
}

func nativeResponsesIndex(root gjson.Result, path string) (int64, bool) {
	value := root.Get(path)
	return value.Int(), value.Exists()
}

func nativeResponsesEventRef(root gjson.Result, itemIDPath string) nativeResponsesBadgeRef {
	outputIndex, hasOutputIndex := nativeResponsesIndex(root, "output_index")
	contentIndex, hasContentIndex := nativeResponsesIndex(root, "content_index")
	return nativeResponsesBadgeRef{
		itemID:          root.Get(itemIDPath).Str,
		outputIndex:     outputIndex,
		hasOutputIndex:  hasOutputIndex,
		contentIndex:    contentIndex,
		hasContentIndex: hasContentIndex,
	}
}

func nativeResponsesAssistantMessage(item gjson.Result) bool {
	if item.Get("type").Str != "message" {
		return false
	}
	role := item.Get("role").Str
	return role == "" || role == "assistant"
}

func firstNativeOutputTextPart(item gjson.Result) (int, bool) {
	content := item.Get("content")
	if !content.IsArray() {
		return 0, false
	}
	for index, part := range content.Array() {
		if part.Get("type").Str == "output_text" && part.Get("text").Type == gjson.String {
			return index, true
		}
	}
	return 0, false
}

func (t *ResponsesWriter) nativeOutputTextPart(item gjson.Result) (int, bool) {
	content := item.Get("content")
	if !content.IsArray() {
		return 0, false
	}
	if !t.nativeBadgeHasContentIndex {
		return firstNativeOutputTextPart(item)
	}
	index := int(t.nativeBadgeContentIndex)
	parts := content.Array()
	if index < 0 || index >= len(parts) {
		return 0, false
	}
	part := parts[index]
	if part.Get("type").Str != "output_text" || part.Get("text").Type != gjson.String {
		return 0, false
	}
	return index, true
}

func (t *ResponsesWriter) nativeBadgeTargetMatches(ref nativeResponsesBadgeRef, includeContent bool) bool {
	if !t.nativeBadgeTargetSelected {
		return false
	}
	matched := false
	if t.nativeBadgeItemID != "" && ref.itemID != "" {
		if t.nativeBadgeItemID != ref.itemID {
			return false
		}
		matched = true
	}
	if t.nativeBadgeHasOutputIndex && ref.hasOutputIndex {
		if t.nativeBadgeOutputIndex != ref.outputIndex {
			return false
		}
		matched = true
	}
	if includeContent && t.nativeBadgeHasContentIndex && ref.hasContentIndex {
		if t.nativeBadgeContentIndex != ref.contentIndex {
			return false
		}
		matched = true
	}
	return matched
}

func (t *ResponsesWriter) observeNativeBadgeTarget(ref nativeResponsesBadgeRef) bool {
	if !t.nativeBadgeTargetSelected {
		t.nativeBadgeTargetSelected = true
		t.nativeBadgeItemID = ref.itemID
		t.nativeBadgeOutputIndex = ref.outputIndex
		t.nativeBadgeHasOutputIndex = ref.hasOutputIndex
		t.nativeBadgeContentIndex = ref.contentIndex
		t.nativeBadgeHasContentIndex = ref.hasContentIndex
		return true
	}
	if !t.nativeBadgeTargetMatches(ref, false) {
		return false
	}
	if t.nativeBadgeItemID == "" {
		t.nativeBadgeItemID = ref.itemID
	}
	if !t.nativeBadgeHasOutputIndex && ref.hasOutputIndex {
		t.nativeBadgeOutputIndex = ref.outputIndex
		t.nativeBadgeHasOutputIndex = true
	}
	if !t.nativeBadgeHasContentIndex && ref.hasContentIndex {
		t.nativeBadgeContentIndex = ref.contentIndex
		t.nativeBadgeHasContentIndex = true
	}
	return true
}

func (t *ResponsesWriter) prefixNativeBadge(data []byte, path string) ([]byte, bool) {
	text := gjson.GetBytes(data, path)
	if text.Type != gjson.String || text.Str == "" || codexResponsesBadgePattern.MatchString(text.Str) {
		return data, false
	}
	badge := t.computeBadgeText()
	if badge == "" {
		return data, false
	}
	rewritten, err := sjson.SetBytes(append([]byte(nil), data...), path, badge+text.Str)
	if err != nil {
		// The badge is cosmetic. A malformed or unexpected event must retain the
		// upstream bytes instead of terminating an otherwise valid model stream.
		return data, false
	}
	return rewritten, true
}

func (t *ResponsesWriter) suffixNativeFooter(data []byte, path string) ([]byte, bool) {
	if t.footerText == "" || t.sawToolCall {
		return data, false
	}
	text := gjson.GetBytes(data, path)
	if text.Type != gjson.String {
		return data, false
	}
	if feedbackFooterPattern.MatchString(text.Str) {
		return data, false
	}
	rewritten, err := sjson.SetBytes(append([]byte(nil), data...), path, text.Str+t.footerText)
	if err != nil {
		return data, false
	}
	return rewritten, true
}

func applyNativeRewrites(data []byte, fns ...func([]byte) ([]byte, bool)) ([]byte, bool) {
	changed := false
	for _, fn := range fns {
		next, ok := fn(data)
		if ok {
			data = next
			changed = true
		}
	}
	return data, changed
}

func (t *ResponsesWriter) rewriteNativeResponsesPayload(data []byte) ([]byte, bool) {
	if !gjson.ValidBytes(data) {
		return data, false
	}
	root := gjson.ParseBytes(data)
	switch root.Get("type").Str {
	case "response.content_part.added":
		if root.Get("part.type").Str != "output_text" {
			return data, false
		}
		t.observeNativeBadgeTarget(nativeResponsesEventRef(root, "item_id"))
		return data, false

	case "response.output_text.delta":
		ref := nativeResponsesEventRef(root, "item_id")
		if !t.observeNativeBadgeTarget(ref) || !t.nativeBadgeTargetMatches(ref, true) {
			return data, false
		}
		delta := root.Get("delta")
		if t.nativeBadgeDeltaPrepended || delta.Type != gjson.String || delta.Str == "" {
			return data, false
		}
		// Each delta carries only newly generated text. Prefix exactly the first
		// non-empty one; the cumulative done snapshots below are rewritten
		// independently so Codex history and the live stream agree.
		t.nativeBadgeDeltaPrepended = true
		return t.prefixNativeBadge(data, "delta")

	case "response.output_text.done":
		ref := nativeResponsesEventRef(root, "item_id")
		if !t.observeNativeBadgeTarget(ref) || !t.nativeBadgeTargetMatches(ref, true) {
			if t.nativeFooterCommit {
				return t.suffixNativeFooter(data, "text")
			}
			return data, false
		}
		if t.nativeFooterCommit {
			return applyNativeRewrites(data,
				func(d []byte) ([]byte, bool) { return t.prefixNativeBadge(d, "text") },
				func(d []byte) ([]byte, bool) { return t.suffixNativeFooter(d, "text") },
			)
		}
		return t.prefixNativeBadge(data, "text")

	case "response.content_part.done":
		if root.Get("part.type").Str != "output_text" {
			return data, false
		}
		ref := nativeResponsesEventRef(root, "item_id")
		if !t.observeNativeBadgeTarget(ref) || !t.nativeBadgeTargetMatches(ref, true) {
			if t.nativeFooterCommit {
				return t.suffixNativeFooter(data, "part.text")
			}
			return data, false
		}
		if t.nativeFooterCommit {
			return applyNativeRewrites(data,
				func(d []byte) ([]byte, bool) { return t.prefixNativeBadge(d, "part.text") },
				func(d []byte) ([]byte, bool) { return t.suffixNativeFooter(d, "part.text") },
			)
		}
		return t.prefixNativeBadge(data, "part.text")

	case "response.output_item.done":
		item := root.Get("item")
		if !nativeResponsesAssistantMessage(item) {
			return data, false
		}
		ref := nativeResponsesEventRef(root, "item.id")
		if t.nativeBadgeTargetSelected && !t.nativeBadgeTargetMatches(ref, false) {
			return data, false
		}
		partIndex, ok := t.nativeOutputTextPart(item)
		if !ok {
			return data, false
		}
		ref.contentIndex = int64(partIndex)
		ref.hasContentIndex = true
		if !t.observeNativeBadgeTarget(ref) || !t.nativeBadgeTargetMatches(ref, true) {
			if t.nativeFooterCommit {
				return t.suffixNativeFooter(data, "item.content."+strconv.Itoa(partIndex)+".text")
			}
			return data, false
		}
		path := "item.content." + strconv.Itoa(partIndex) + ".text"
		if t.nativeFooterCommit {
			return applyNativeRewrites(data,
				func(d []byte) ([]byte, bool) { return t.prefixNativeBadge(d, path) },
				func(d []byte) ([]byte, bool) { return t.suffixNativeFooter(d, path) },
			)
		}
		return t.prefixNativeBadge(data, path)

	case "response.completed", "response.incomplete":
		output := root.Get("response.output")
		if !output.IsArray() {
			return data, false
		}
		for outputIndex, item := range output.Array() {
			if !nativeResponsesAssistantMessage(item) {
				continue
			}
			ref := nativeResponsesBadgeRef{
				itemID:         item.Get("id").Str,
				outputIndex:    int64(outputIndex),
				hasOutputIndex: true,
			}
			if t.nativeBadgeTargetSelected && !t.nativeBadgeTargetMatches(ref, false) {
				// The badge rides its own synthetic item, so this message only
				// needs the footer.
				textPart, ok := firstNativeOutputTextPart(item)
				if !ok {
					continue
				}
				footerPath := "response.output." + strconv.Itoa(outputIndex) + ".content." + strconv.Itoa(textPart) + ".text"
				if rewritten, changed := t.suffixNativeFooter(data, footerPath); changed {
					return rewritten, true
				}
				continue
			}
			partIndex, ok := t.nativeOutputTextPart(item)
			if !ok {
				continue
			}
			path := "response.output." + strconv.Itoa(outputIndex) + ".content." + strconv.Itoa(partIndex) + ".text"
			ref.contentIndex = int64(partIndex)
			ref.hasContentIndex = true
			if !t.observeNativeBadgeTarget(ref) || !t.nativeBadgeTargetMatches(ref, true) {
				continue
			}
			return applyNativeRewrites(data,
				func(d []byte) ([]byte, bool) { return t.prefixNativeBadge(d, path) },
				func(d []byte) ([]byte, bool) { return t.suffixNativeFooter(d, path) },
			)
		}
	}
	return data, false
}

// rewriteNativePassthroughFields shifts sequence numbers (monotonicity) and
// output indices (item identity) after a synthetic badge item is prepended.
func (t *ResponsesWriter) rewriteNativePassthroughFields(data []byte) ([]byte, bool) {
	if !gjson.ValidBytes(data) {
		return data, false
	}
	root := gjson.ParseBytes(data)
	changed := false
	if t.nativeSequenceShift != 0 && root.Get("sequence_number").Type == gjson.Number {
		if rewritten, err := sjson.SetBytes(data, "sequence_number", root.Get("sequence_number").Int()+t.nativeSequenceShift); err == nil {
			data = rewritten
			root = gjson.ParseBytes(data)
			changed = true
		}
	}
	if t.nativeOutputIndexShift != 0 && root.Get("output_index").Type == gjson.Number {
		if rewritten, err := sjson.SetBytes(data, "output_index", root.Get("output_index").Int()+t.nativeOutputIndexShift); err == nil {
			data = rewritten
			root = gjson.ParseBytes(data)
			changed = true
		}
	}
	if !t.nativeSyntheticBadgeEmitted || (root.Get("type").Str != "response.completed" && root.Get("type").Str != "response.incomplete") {
		return data, changed
	}
	output := root.Get("response.output")
	if !output.IsArray() {
		return data, changed
	}
	for _, item := range output.Array() {
		if item.Get("id").Str == t.nativeBadgeItemID {
			return data, changed
		}
	}
	badgeItem, err := json.Marshal(map[string]any{
		"id": t.nativeBadgeItemID, "type": "message", "status": "completed", "role": "assistant",
		"content": []any{map[string]any{"type": "output_text", "text": t.computeBadgeText(), "annotations": []any{}}},
	})
	if err != nil {
		return data, changed
	}
	items := make([]string, 0, len(output.Array())+1)
	items = append(items, string(badgeItem))
	for _, item := range output.Array() {
		items = append(items, item.Raw)
	}
	if rewritten, err := sjson.SetRawBytes(data, "response.output", []byte("["+strings.Join(items, ",")+"]")); err == nil {
		return rewritten, true
	}
	return data, changed
}

func (t *ResponsesWriter) rewriteNativeNonStreamingBody(data []byte) ([]byte, bool) {
	if !gjson.ValidBytes(data) {
		return data, false
	}
	root := gjson.ParseBytes(data)
	output := root.Get("output")
	if !output.IsArray() || t.computeBadgeText() == "" {
		return data, false
	}
	for index, item := range output.Array() {
		if !nativeResponsesAssistantMessage(item) {
			continue
		}
		partIndex, ok := firstNativeOutputTextPart(item)
		if !ok {
			continue
		}
		path := "output." + strconv.Itoa(index) + ".content." + strconv.Itoa(partIndex) + ".text"
		return t.prefixNativeBadge(data, path)
	}
	for _, item := range output.Array() {
		if item.Get("id").Str != "" && codexResponsesBadgePattern.MatchString(item.Get("content.0.text").Str) {
			return data, false
		}
	}
	itemID := newResponsesID("msg")
	badgeItem, err := json.Marshal(map[string]any{
		"id": itemID, "type": "message", "status": "completed", "role": "assistant",
		"content": []any{map[string]any{"type": "output_text", "text": t.computeBadgeText(), "annotations": []any{}}},
	})
	if err != nil {
		return data, false
	}
	items := []string{string(badgeItem)}
	for _, item := range output.Array() {
		items = append(items, item.Raw)
	}
	rewritten, err := sjson.SetRawBytes(data, "output", []byte("["+strings.Join(items, ",")+"]"))
	if err != nil {
		return data, false
	}
	return rewritten, true
}

func (t *ResponsesWriter) rewriteNativeResponsesEvent(raw []byte) []byte {
	return t.rewriteNativeEventWith(raw, true)
}

// rewriteNativeHeldEvent re-applies badge/footer to a held event without
// re-shifting coordinates; the shift is applied once on first sight and is not idempotent.
func (t *ResponsesWriter) rewriteNativeHeldEvent(raw []byte) []byte {
	return t.rewriteNativeEventWith(raw, false)
}

func (t *ResponsesWriter) rewriteNativeEventWith(raw []byte, shiftFields bool) []byte {
	_, data := sse.ParseEvent(raw)
	if len(data) == 0 {
		return raw
	}
	rewrittenData, changed := t.rewriteNativeResponsesPayload(data)
	if shiftFields {
		if fields, fieldsChanged := t.rewriteNativePassthroughFields(rewrittenData); fieldsChanged {
			rewrittenData = fields
			changed = true
		}
	}
	if !changed {
		return raw
	}
	offset := bytes.Index(raw, data)
	if offset < 0 {
		return raw
	}
	// Preallocate only len(raw); badge growth is handled safely by append.
	rewritten := make([]byte, 0, len(raw))
	rewritten = append(rewritten, raw[:offset]...)
	rewritten = append(rewritten, rewrittenData...)
	rewritten = append(rewritten, raw[offset+len(data):]...)
	return rewritten
}

func (t *ResponsesWriter) writeNativeEvent(eventType string, sequence int64, payload map[string]any) error {
	payload["type"] = eventType
	payload["sequence_number"] = sequence
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := t.bw.WriteString("event: " + eventType + "\ndata: "); err != nil {
		return err
	}
	if _, err := t.bw.Write(body); err != nil {
		return err
	}
	_, err = t.bw.WriteString("\n\n")
	return err
}

// emitNativeBadgeBeforeOutput prepends a synthetic badge message item, emitting
// six native events and shifting sequence numbers and output indices accordingly.
func (t *ResponsesWriter) emitNativeBadgeBeforeOutput(event []byte) error {
	if t.nativeSyntheticBadgeEmitted || t.nativeBadgeTargetSelected || t.computeBadgeText() == "" {
		return nil
	}
	_, data := sse.ParseEvent(event)
	root := gjson.ParseBytes(data)
	sequence := root.Get("sequence_number")
	base := int64(0)
	if sequence.Type == gjson.Number {
		base = sequence.Int() + t.nativeSequenceShift
	}
	itemID := newResponsesID("msg")
	text := t.computeBadgeText()
	item := map[string]any{
		"id": itemID, "type": "message", "status": "in_progress", "role": "assistant", "content": []any{},
	}
	if err := t.writeNativeEvent("response.output_item.added", base, map[string]any{"output_index": 0, "item": item}); err != nil {
		return err
	}
	if err := t.writeNativeEvent("response.content_part.added", base+1, map[string]any{
		"item_id": itemID, "output_index": 0, "content_index": 0,
		"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
	}); err != nil {
		return err
	}
	if err := t.writeNativeEvent("response.output_text.delta", base+2, map[string]any{
		"item_id": itemID, "output_index": 0, "content_index": 0, "delta": text,
	}); err != nil {
		return err
	}
	if err := t.writeNativeEvent("response.output_text.done", base+3, map[string]any{
		"item_id": itemID, "output_index": 0, "content_index": 0, "text": text,
	}); err != nil {
		return err
	}
	if err := t.writeNativeEvent("response.content_part.done", base+4, map[string]any{
		"item_id": itemID, "output_index": 0, "content_index": 0,
		"part": map[string]any{"type": "output_text", "text": text, "annotations": []any{}},
	}); err != nil {
		return err
	}
	if err := t.writeNativeEvent("response.output_item.done", base+5, map[string]any{
		"output_index": 0,
		"item": map[string]any{"id": itemID, "type": "message", "status": "completed", "role": "assistant", "content": []any{
			map[string]any{"type": "output_text", "text": text, "annotations": []any{}},
		}},
	}); err != nil {
		return err
	}
	t.nativeSyntheticBadgeEmitted = true
	t.nativeBadgeTargetSelected = true
	t.nativeBadgeItemID = itemID
	t.nativeBadgeOutputIndex = 0
	t.nativeBadgeHasOutputIndex = true
	t.nativeBadgeContentIndex = 0
	t.nativeBadgeHasContentIndex = true
	t.nativeOutputIndexShift++
	t.nativeSequenceShift += 6
	return nil
}

func (t *ResponsesWriter) writeNativeResponsesEvent(event, delimiter []byte) error {
	_, data := sse.ParseEvent(event)
	eventType := ""
	itemType := ""
	if gjson.ValidBytes(data) {
		eventType = gjson.GetBytes(data, "type").Str
		itemType = gjson.GetBytes(data, "item.type").Str
	}
	toolCall := eventType == "response.function_call_arguments.delta" || eventType == "response.custom_tool_call_input.delta" || (eventType == "response.output_item.added" && (itemType == "function_call" || itemType == "custom_tool_call")) || (eventType == "response.output_item.done" && (itemType == "function_call" || itemType == "custom_tool_call"))
	reasoningItem := eventType == "response.output_item.added" && itemType == "reasoning"
	if toolCall || reasoningItem {
		if toolCall {
			t.sawToolCall = true
		}
		if err := t.emitNativeBadgeBeforeOutput(event); err != nil {
			return err
		}
		if toolCall {
			if err := t.flushNativeHeldEvents(false); err != nil {
				return err
			}
		}
	}
	if (eventType == "response.completed" || eventType == "response.incomplete") && !t.nativeBadgeTargetSelected && !t.nativeSyntheticBadgeEmitted {
		if err := t.emitNativeBadgeBeforeOutput(event); err != nil {
			return err
		}
	}
	rewritten := t.rewriteNativeResponsesEvent(event)
	if t.shouldHoldNativeEvent(eventType, itemType) {
		held := append([]byte(nil), rewritten...)
		if len(delimiter) > 0 {
			held = append(held, delimiter...)
		}
		t.nativeHeldEvents = append(t.nativeHeldEvents, held)
		return nil
	}
	if eventType == "response.completed" || eventType == "response.incomplete" {
		if err := t.flushNativeHeldEvents(true); err != nil {
			return err
		}
	}
	if _, err := t.bw.Write(rewritten); err != nil {
		return err
	}
	if len(delimiter) > 0 {
		_, err := t.bw.Write(delimiter)
		return err
	}
	return nil
}

func (t *ResponsesWriter) shouldHoldNativeEvent(eventType, itemType string) bool {
	if t.footerText == "" || t.sawToolCall {
		return false
	}
	switch eventType {
	case "response.output_text.done", "response.content_part.done":
		return true
	case "response.output_item.done":
		return itemType == "message" || itemType == ""
	}
	return false
}

func (t *ResponsesWriter) flushNativeHeldEvents(commitFooter bool) error {
	if len(t.nativeHeldEvents) == 0 {
		return nil
	}
	if commitFooter {
		t.nativeFooterCommit = true
	}
	held := t.nativeHeldEvents
	t.nativeHeldEvents = nil
	for _, event := range held {
		rewritten := t.rewriteNativeHeldEvent(event)
		if _, err := t.bw.Write(rewritten); err != nil {
			return err
		}
	}
	return nil
}

func (t *ResponsesWriter) processPassthroughSSEBuffer() error {
	for {
		buffered := t.buf.Bytes()
		event, n := sse.SplitNext(buffered)
		if n == 0 {
			return nil
		}
		err := t.writeNativeResponsesEvent(event, buffered[len(event):n])
		t.buf.Next(n)
		if err != nil {
			return err
		}
	}
}

func (t *ResponsesWriter) processFinalPassthroughSSETail() error {
	if t.buf.Len() > 0 {
		event := append([]byte(nil), t.buf.Bytes()...)
		t.buf.Reset()
		if err := t.writeNativeResponsesEvent(event, nil); err != nil {
			return err
		}
	}
	return t.flushNativeHeldEvents(t.footerText != "" && !t.sawToolCall)
}

// FinalizeError emits a response.failed terminal event when upstream fails
// mid-stream (after response.created), so Codex sees a clean failure instead
// of a truncated stream. No-op if nothing streamed yet (caller writes a JSON
// error instead), in passthrough mode, or after a terminal event already fired.
func (t *ResponsesWriter) FinalizeError(_ error) error {
	if t.passthrough || !t.streaming || !t.headersEmitted || t.completedEmitted {
		return nil
	}
	if t.lifecycle.State() == StreamStarted {
		if err := t.lifecycle.Fail(); err != nil {
			return err
		}
	}
	closeErr := t.closeOpenItems()
	if err := t.emitFailed(); err != nil {
		return err
	}
	t.completedEmitted = true
	if err := t.bw.Flush(); err != nil {
		return err
	}
	return closeErr
}

// processSSEBuffer drains complete chat.completion.chunk events.
func (t *ResponsesWriter) processSSEBuffer() error {
	for {
		event, n := sse.SplitNext(t.buf.Bytes())
		if n == 0 {
			return nil
		}
		err := t.translateChunk(event)
		t.buf.Next(n)
		if err != nil {
			return err
		}
	}
}

func (t *ResponsesWriter) processFinalSSETail() error {
	if t.buf.Len() == 0 {
		return nil
	}
	event := append([]byte(nil), t.buf.Bytes()...)
	t.buf.Reset()
	return t.translateChunk(event)
}

func (t *ResponsesWriter) translateChunk(raw []byte) error {
	_, data := sse.ParseEvent(raw)
	if len(data) == 0 {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
		if !t.lifecycle.OutputStarted() {
			return nil
		}
		if err := t.closeOpenItems(); err != nil {
			return err
		}
		if t.completedEmitted {
			return nil
		}
		if err := t.lifecycle.Terminal(); err != nil {
			return err
		}
		t.completedEmitted = true
		return t.emitCompleted()
	}
	if !gjson.ValidBytes(data) {
		return nil
	}

	root := gjson.ParseBytes(data)
	if m := root.Get("model").Str; m != "" && t.model == "" {
		t.model = strings.Clone(m)
	}
	if usage := root.Get("usage"); usage.Exists() {
		t.usage = &responsesUsage{
			prompt:     usage.Get("prompt_tokens").Int(),
			completion: usage.Get("completion_tokens").Int(),
			total:      usage.Get("total_tokens").Int(),
		}
	}

	choice := root.Get("choices.0")
	if !choice.Exists() {
		return nil
	}
	delta := choice.Get("delta")

	if content := delta.Get("content"); content.Type == gjson.String && content.Str != "" {
		if err := t.appendText(content.Str); err != nil {
			return err
		}
	}

	if tcs := delta.Get("tool_calls"); tcs.IsArray() {
		if tcs.Get("#").Int() > 0 {
			t.sawToolCall = true
		}
		for _, tc := range tcs.Array() {
			idx := int(tc.Get("index").Int())
			if err := t.appendToolCall(idx, tc); err != nil {
				return err
			}
		}
	}

	if fr := choice.Get("finish_reason"); fr.Type == gjson.String && fr.Str != "" {
		t.finishReason = fr.Str
		// Reasoning-only turns emit no delta this writer translates, so the
		// badge would never be reached through appendText/appendToolCall.
		if err := t.ensureBadgeItem(); err != nil {
			return err
		}
		if err := t.closeOpenItems(); err != nil {
			return err
		}
		if !t.completedEmitted {
			if err := t.lifecycle.Terminal(); err != nil {
				return err
			}
			t.completedEmitted = true
			return t.emitCompleted()
		}
	}
	return nil
}

func (t *ResponsesWriter) appendText(s string) error {
	if err := t.ensureBadgeItem(); err != nil {
		return err
	}
	if err := t.openTextItem(); err != nil {
		return err
	}
	if err := t.lifecycle.Output(t.textItem.outputIndex); err != nil {
		return err
	}

	t.textItem.text.WriteString(s)
	return t.emitTextDelta(t.textItem, s)
}

// openTextItem lazily opens the assistant text item the badge and every text
// delta share. Idempotent.
func (t *ResponsesWriter) openTextItem() error {
	if t.textItem != nil {
		return nil
	}
	t.textItem = &responsesTextItem{
		itemID: newResponsesID("msg"),
	}
	// Must assign after t.textItem is reachable, else nextOutputIndex
	// undercounts it (Go evaluates struct-literal RHS before assignment).
	t.textItem.outputIndex = t.nextOutputIndex()
	if err := t.emitMessageItemAdded(t.textItem); err != nil {
		return err
	}
	if err := t.emitContentPartAdded(t.textItem); err != nil {
		return err
	}
	t.textItem.openedPart = true
	return nil
}

// ensureBadgeItem prepends the routing badge before the first text delta, tool call, or finish.
// Codex drops reasoning-summary items from custom providers (text is the only guaranteed surface),
// and tool-call-only turns produce no text of their own without this.
func (t *ResponsesWriter) ensureBadgeItem() error {
	if t.badgePrepended {
		return nil
	}
	t.badgePrepended = true
	line := t.computeBadgeText()
	if line == "" {
		return nil
	}
	if err := t.openTextItem(); err != nil {
		return err
	}
	if err := t.lifecycle.Output(t.textItem.outputIndex); err != nil {
		return err
	}
	t.textItem.text.WriteString(line)
	return t.emitTextDelta(t.textItem, line)
}

func (t *ResponsesWriter) appendToolCall(idx int, tc gjson.Result) error {
	if err := t.ensureBadgeItem(); err != nil {
		return err
	}
	entry := t.toolLedger.Upsert(idx, tc.Get("id").Str, tc.Get("function.name").Str)
	item, ok := t.toolItems[idx]
	justOpened := false
	if !ok {
		mapping := ResponsesToolMapping{Name: entry.Name}
		if mapped, found := t.toolMappings[entry.Name]; found {
			mapping = mapped
		}
		item = &responsesToolItem{
			mapping: mapping,
		}
		t.toolItems[idx] = item
		item.outputIndex = t.nextOutputIndex()
		item.callID = entry.ExternalID
		item.name = mapping.Name
		if item.name == "" {
			item.name = entry.Name
		}
		// Buffer nameless chunks; the mapping decides function_call vs
		// custom_tool_call only once the name arrives from a later delta.
		if len(t.toolMappings) == 0 || item.name != "" {
			item.itemID = newResponsesID(toolCallItemIDPrefix(item.mapping.Custom))
			if err := t.emitFunctionCallItemAdded(item); err != nil {
				return err
			}
			item.opened = true
			justOpened = true
		}
	}
	// Later chunks may carry name/id only on first delta; pick up any later
	// arrivals defensively.
	if item.name == "" && entry.Name != "" {
		if mapped, found := t.toolMappings[entry.Name]; found {
			item.mapping = mapped
			item.name = mapped.Name
		} else {
			item.name = entry.Name
		}
	}
	if !item.opened && item.name != "" {
		item.itemID = newResponsesID(toolCallItemIDPrefix(item.mapping.Custom))
		if err := t.emitFunctionCallItemAdded(item); err != nil {
			return err
		}
		item.opened = true
		justOpened = true
	}
	if err := t.lifecycle.Output(item.outputIndex); err != nil {
		return err
	}
	args := tc.Get("function.arguments").Str
	if args != "" {
		t.toolLedger.AppendArguments(idx, tc.Get("id").Str, tc.Get("function.name").Str, args)
		item.arguments.WriteString(args)
	}
	if !item.opened {
		return nil
	}
	if justOpened && item.arguments.Len() > 0 {
		return t.emitFunctionArgsDelta(item, item.arguments.String())
	}
	if args != "" {
		return t.emitFunctionArgsDelta(item, args)
	}
	return nil
}

func (t *ResponsesWriter) nextOutputIndex() int {
	count := 0
	if t.textItem != nil {
		count++
	}
	count += len(t.toolItems)
	return count - 1
}

// computeBadgeText returns the routing badge to surface for this turn, with the
// Codex provenance sentinel applied when enabled. Empty when the proxy supplied
// no marker — suppression is decided there, not here.
func (t *ResponsesWriter) computeBadgeText() string {
	if t.badgeText == "" {
		return ""
	}
	badge := t.badgeText
	if t.codexBadgeProvenance && !strings.HasPrefix(badge, codexResponsesBadgeSentinel) {
		badge = codexResponsesBadgeSentinel + badge
	}
	return badge
}

func (t *ResponsesWriter) closeOpenItems() error {
	if t.textItem != nil && !t.textItem.closed {
		if t.footerText != "" && !t.sawToolCall && t.finishReason == "stop" && !feedbackFooterPattern.MatchString(t.textItem.text.String()) {
			t.textItem.text.WriteString(t.footerText)
			if err := t.emitTextDelta(t.textItem, t.footerText); err != nil {
				return err
			}
		}
		if err := t.emitTextDone(t.textItem); err != nil && len(t.toolMappings) > 0 {
			return err
		}
		if err := t.emitContentPartDone(t.textItem); err != nil && len(t.toolMappings) > 0 {
			return err
		}
		if err := t.emitMessageItemDone(t.textItem); err != nil && len(t.toolMappings) > 0 {
			return err
		}
		t.textItem.closed = true
	}
	for _, item := range t.toolItems {
		if item.closed {
			continue
		}
		if len(t.toolMappings) > 0 && !item.opened {
			return fmt.Errorf("portable Codex tool call is missing a function name")
		}
		if err := t.emitFunctionArgsDone(item); err != nil && len(t.toolMappings) > 0 {
			return err
		}
		if err := t.emitFunctionCallItemDone(item); err != nil && len(t.toolMappings) > 0 {
			return err
		}
		item.closed = true
	}
	return nil
}

// emitIncompleteFailure terminates a committed translated stream without
// fabricating a response.completed event after upstream EOF.
func (t *ResponsesWriter) emitIncompleteFailure() error {
	if err := t.lifecycle.Fail(); err != nil {
		return err
	}
	closeErr := t.closeOpenItems()
	if err := t.emitFailed(); err != nil {
		return err
	}
	t.completedEmitted = true
	if err := t.bw.Flush(); err != nil {
		return err
	}
	return closeErr
}

// ---------- event emitters ----------

func (t *ResponsesWriter) nextSeq() int64 {
	s := t.seq
	t.seq++
	return s
}

func (t *ResponsesWriter) writeEvent(eventType string, payload map[string]any) error {
	payload["type"] = eventType
	payload["sequence_number"] = t.nextSeq()
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := t.bw.WriteString("event: "); err != nil {
		return err
	}
	if _, err := t.bw.WriteString(eventType); err != nil {
		return err
	}
	if _, err := t.bw.WriteString("\ndata: "); err != nil {
		return err
	}
	if _, err := t.bw.Write(body); err != nil {
		return err
	}
	if _, err := t.bw.WriteString("\n\n"); err != nil {
		return err
	}
	if err := t.bw.Flush(); err != nil {
		return err
	}
	if t.flusher != nil {
		t.flusher.Flush()
	}
	return nil
}

func (t *ResponsesWriter) responseEnvelope(status string) map[string]any {
	env := map[string]any{
		"id":         t.responseID,
		"object":     "response",
		"created_at": t.createdAt,
		"status":     status,
		"model":      t.model,
	}
	return env
}

func (t *ResponsesWriter) emitCreated() error {
	return t.writeEvent("response.created", map[string]any{
		"response": t.responseEnvelope("in_progress"),
	})
}

func (t *ResponsesWriter) emitMessageItemAdded(item *responsesTextItem) error {
	return t.writeEvent("response.output_item.added", map[string]any{
		"output_index": item.outputIndex,
		"item": map[string]any{
			"id":      item.itemID,
			"type":    "message",
			"status":  "in_progress",
			"role":    "assistant",
			"content": []any{},
		},
	})
}

func (t *ResponsesWriter) emitContentPartAdded(item *responsesTextItem) error {
	return t.writeEvent("response.content_part.added", map[string]any{
		"item_id":       item.itemID,
		"output_index":  item.outputIndex,
		"content_index": 0,
		"part": map[string]any{
			"type":        "output_text",
			"text":        "",
			"annotations": []any{},
		},
	})
}

func (t *ResponsesWriter) emitTextDelta(item *responsesTextItem, delta string) error {
	return t.writeEvent("response.output_text.delta", map[string]any{
		"item_id":       item.itemID,
		"output_index":  item.outputIndex,
		"content_index": 0,
		"delta":         delta,
	})
}

func (t *ResponsesWriter) emitTextDone(item *responsesTextItem) error {
	return t.writeEvent("response.output_text.done", map[string]any{
		"item_id":       item.itemID,
		"output_index":  item.outputIndex,
		"content_index": 0,
		"text":          item.text.String(),
	})
}

func (t *ResponsesWriter) emitContentPartDone(item *responsesTextItem) error {
	return t.writeEvent("response.content_part.done", map[string]any{
		"item_id":       item.itemID,
		"output_index":  item.outputIndex,
		"content_index": 0,
		"part": map[string]any{
			"type":        "output_text",
			"text":        item.text.String(),
			"annotations": []any{},
		},
	})
}

func (t *ResponsesWriter) emitMessageItemDone(item *responsesTextItem) error {
	return t.writeEvent("response.output_item.done", map[string]any{
		"output_index": item.outputIndex,
		"item": map[string]any{
			"id":     item.itemID,
			"type":   "message",
			"status": "completed",
			"role":   "assistant",
			"content": []any{map[string]any{
				"type":        "output_text",
				"text":        item.text.String(),
				"annotations": []any{},
			}},
		},
	})
}

func (t *ResponsesWriter) emitFunctionCallItemAdded(item *responsesToolItem) error {
	if item.mapping.Custom {
		call := map[string]any{
			"id":      item.itemID,
			"type":    "custom_tool_call",
			"status":  "in_progress",
			"call_id": item.callID,
			"name":    item.name,
			"input":   "",
		}
		if item.mapping.Namespace != "" {
			call["namespace"] = item.mapping.Namespace
		}
		return t.writeEvent("response.output_item.added", map[string]any{
			"output_index": item.outputIndex,
			"item":         call,
		})
	}
	call := map[string]any{
		"id":        item.itemID,
		"type":      "function_call",
		"status":    "in_progress",
		"call_id":   item.callID,
		"name":      item.name,
		"arguments": "",
	}
	if item.mapping.Namespace != "" {
		call["namespace"] = item.mapping.Namespace
	}
	return t.writeEvent("response.output_item.added", map[string]any{
		"output_index": item.outputIndex,
		"item":         call,
	})
}

func (t *ResponsesWriter) emitFunctionArgsDelta(item *responsesToolItem, delta string) error {
	if item.mapping.Custom {
		// Chat providers stream fragments of the JSON wrapper, not fragments of
		// the raw freeform input. Emitting them as custom deltas would expose
		// invalid patch/JavaScript text to Codex, so the validated raw input is
		// emitted atomically when the call closes.
		return nil
	}
	return t.writeEvent("response.function_call_arguments.delta", map[string]any{
		"item_id":      item.itemID,
		"output_index": item.outputIndex,
		"delta":        delta,
	})
}

func (t *ResponsesWriter) emitFunctionArgsDone(item *responsesToolItem) error {
	if item.mapping.Custom {
		input, err := customToolInput(item.arguments.String())
		if err != nil {
			return err
		}
		if input != "" {
			if err := t.writeEvent("response.custom_tool_call_input.delta", map[string]any{
				"item_id":      item.itemID,
				"call_id":      item.callID,
				"output_index": item.outputIndex,
				"delta":        input,
			}); err != nil {
				return err
			}
		}
		return nil
	}
	return t.writeEvent("response.function_call_arguments.done", map[string]any{
		"item_id":      item.itemID,
		"output_index": item.outputIndex,
		"arguments":    item.arguments.String(),
	})
}

func (t *ResponsesWriter) emitFunctionCallItemDone(item *responsesToolItem) error {
	if item.mapping.Custom {
		input, err := customToolInput(item.arguments.String())
		if err != nil {
			return err
		}
		call := map[string]any{
			"id": item.itemID, "type": "custom_tool_call", "status": "completed",
			"call_id": item.callID, "name": item.name, "input": input,
		}
		if item.mapping.Namespace != "" {
			call["namespace"] = item.mapping.Namespace
		}
		return t.writeEvent("response.output_item.done", map[string]any{"output_index": item.outputIndex, "item": call})
	}
	call := map[string]any{
		"id": item.itemID, "type": "function_call", "status": "completed",
		"call_id": item.callID, "name": item.name, "arguments": item.arguments.String(),
	}
	if item.mapping.Namespace != "" {
		call["namespace"] = item.mapping.Namespace
	}
	return t.writeEvent("response.output_item.done", map[string]any{"output_index": item.outputIndex, "item": call})
}

func (t *ResponsesWriter) emitCompleted() error {
	env := t.responseEnvelope("completed")
	env["output"] = t.assembleOutput()
	if t.usage != nil {
		env["usage"] = map[string]any{
			"input_tokens":  t.usage.prompt,
			"output_tokens": t.usage.completion,
			"total_tokens":  t.usage.total,
		}
	}
	return t.writeEvent("response.completed", map[string]any{
		"response": env,
	})
}

// emitFailed writes response.failed with whatever output was assembled so far
// plus a generic error (no upstream internals leak). Usage omitted: no
// trustworthy accounting for a failed turn.
func (t *ResponsesWriter) emitFailed() error {
	env := t.responseEnvelope("failed")
	env["output"] = t.assembleOutput()
	env["error"] = map[string]any{
		"code":    "upstream_error",
		"message": "Upstream call failed.",
	}
	return t.writeEvent("response.failed", map[string]any{
		"response": env,
	})
}

func (t *ResponsesWriter) assembleOutput() []any {
	out := make([]any, 0, len(t.toolItems))
	if t.textItem != nil {
		out = append(out, map[string]any{
			"id":     t.textItem.itemID,
			"type":   "message",
			"status": "completed",
			"role":   "assistant",
			"content": []any{map[string]any{
				"type":        "output_text",
				"text":        t.textItem.text.String(),
				"annotations": []any{},
			}},
		})
	}
	// Tool items in upstream index order. Upstream indices may be
	// non-contiguous (e.g. {0, 2}), so iterate the sorted keys rather than
	// counting up to len.
	indices := make([]int, 0, len(t.toolItems))
	for idx := range t.toolItems {
		indices = append(indices, idx)
	}
	sort.Ints(indices)
	for _, idx := range indices {
		item := t.toolItems[idx]
		if len(t.toolMappings) > 0 && !item.opened {
			continue
		}
		if item.mapping.Custom {
			input, err := customToolInput(item.arguments.String())
			if err != nil {
				continue
			}
			call := map[string]any{
				"id":      item.itemID,
				"type":    "custom_tool_call",
				"status":  "completed",
				"call_id": item.callID,
				"name":    item.name,
				"input":   input,
			}
			if item.mapping.Namespace != "" {
				call["namespace"] = item.mapping.Namespace
			}
			out = append(out, call)
			continue
		}
		call := map[string]any{
			"id":        item.itemID,
			"type":      "function_call",
			"status":    "completed",
			"call_id":   item.callID,
			"name":      item.name,
			"arguments": item.arguments.String(),
		}
		if item.mapping.Namespace != "" {
			call["namespace"] = item.mapping.Namespace
		}
		out = append(out, call)
	}
	return out
}

// chatCompletionToResponse converts a buffered chat-completions JSON body into
// a Responses-shaped JSON body. Only used when the client requested
// stream:false; Codex always streams, but other clients may not. A non-empty
// badge leads the assistant text, synthesizing the message item when the turn
// produced only tool calls.
func chatCompletionToResponse(body []byte, responseID, model string, createdAt int64, mappings map[string]ResponsesToolMapping, badge, footer string) ([]byte, error) {
	if !gjson.ValidBytes(body) {
		return nil, fmt.Errorf("invalid JSON")
	}
	root := gjson.ParseBytes(body)
	if model == "" {
		model = root.Get("model").Str
	}

	out := map[string]any{
		"id":         responseID,
		"object":     "response",
		"created_at": createdAt,
		"status":     "completed",
		"model":      model,
	}

	choice := root.Get("choices.0.message")
	output := make([]any, 0, 2)
	text := badge
	if content := choice.Get("content"); content.Type == gjson.String {
		text += content.Str
	}
	if footer != "" && choice.Get("tool_calls.#").Int() == 0 && !feedbackFooterPattern.MatchString(text) {
		text += footer
	}
	if text != "" {
		output = append(output, map[string]any{
			"id":     newResponsesID("msg"),
			"type":   "message",
			"status": "completed",
			"role":   "assistant",
			"content": []any{map[string]any{
				"type":        "output_text",
				"text":        text,
				"annotations": []any{},
			}},
		})
	}
	if tcs := choice.Get("tool_calls"); tcs.IsArray() {
		for _, tc := range tcs.Array() {
			alias := tc.Get("function.name").Str
			mapping, mapped := mappings[alias]
			if !mapped {
				mapping = ResponsesToolMapping{Name: alias}
			}
			item := map[string]any{
				"id":      newResponsesID(toolCallItemIDPrefix(mapping.Custom)),
				"status":  "completed",
				"call_id": tc.Get("id").Str,
				"name":    mapping.Name,
			}
			if mapping.Namespace != "" {
				item["namespace"] = mapping.Namespace
			}
			if mapping.Custom {
				input, err := customToolInput(tc.Get("function.arguments").Str)
				if err != nil {
					return nil, err
				}
				item["type"] = "custom_tool_call"
				item["input"] = input
			} else {
				item["type"] = "function_call"
				item["arguments"] = tc.Get("function.arguments").Str
			}
			output = append(output, item)
		}
	}
	out["output"] = output

	if usage := root.Get("usage"); usage.Exists() {
		out["usage"] = map[string]any{
			"input_tokens":  usage.Get("prompt_tokens").Int(),
			"output_tokens": usage.Get("completion_tokens").Int(),
			"total_tokens":  usage.Get("total_tokens").Int(),
		}
	}

	return json.Marshal(out)
}
