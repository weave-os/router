package translate

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/websearch"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// PrepareOpenAIResponses builds an OpenAI Responses API (`POST /v1/responses`)
// request from an Anthropic Messages envelope. Reasoning gpt-5.x models reject
// `reasoning_effort` + tools on /v1/chat/completions, so agentic clients must
// use Responses instead. We always stream: a non-streaming Responses call
// buffers all reasoning+output before headers, which at high effort routinely
// exceeds the header timeout ("http2: timeout awaiting response headers").

func (e *RequestEnvelope) PrepareOpenAIResponses(in http.Header, opts EmitOptions) (providers.PreparedRequest, error) {
	var body []byte
	var stats providers.RequestMutationStats
	var err error
	switch e.format {
	case FormatAnthropic:
		body, stats, err = e.buildResponsesFromAnthropic(opts)
	case FormatOpenAI:
		body, err = e.buildResponsesFromOpenAI(opts)
	default:
		return providers.PreparedRequest{}, fmt.Errorf("PrepareOpenAIResponses: unsupported source format: %d", e.format)
	}
	if err != nil {
		return providers.PreparedRequest{}, err
	}
	body, err = applyResponsesSessionAffinity(body, opts)
	if err != nil {
		return providers.PreparedRequest{}, err
	}
	body, err = ApplyOpenAIFastMode(body, opts)
	if err != nil {
		return providers.PreparedRequest{}, err
	}
	return providers.PreparedRequest{Body: body, Endpoint: providers.EndpointResponses, Stats: stats}, nil
}

// applyResponsesSessionAffinity mirrors applySessionAffinity for the Responses
// surface: prompt_cache_key is a spec Responses field (forwarded with the body),
// so reasoning-tool turns promoted to /v1/responses stay pinned to a warm replica.
func applyResponsesSessionAffinity(body []byte, opts EmitOptions) ([]byte, error) {
	switch opts.TargetProvider {
	case providers.ProviderOpenAI, providers.ProviderOpenAIGateway:
	default:
		return body, nil
	}
	if opts.StripPromptCacheKey {
		out, err := sjson.DeleteBytes(body, "prompt_cache_key")
		if err != nil {
			return nil, fmt.Errorf("delete prompt_cache_key: %w", err)
		}
		return out, nil
	}
	cacheKey := opts.SessionAffinity
	if cacheKey == "" {
		if gjson.GetBytes(body, "prompt_cache_key").Exists() {
			return body, nil
		}
		cacheKey = stableResponsesPromptCacheKey(body)
		if cacheKey == "" {
			return body, nil
		}
	}
	out, err := sjson.SetBytes(body, "prompt_cache_key", cacheKey)
	if err != nil {
		return nil, fmt.Errorf("set prompt_cache_key: %w", err)
	}
	return out, nil
}

// stableResponsesPromptCacheKey hashes the cacheable prefix (instructions + tools)
// for Responses bodies and returns "" when there is none.
func stableResponsesPromptCacheKey(body []byte) string {
	h := sha1.New()
	var hasPrefix bool
	if instructions := gjson.GetBytes(body, "instructions"); instructions.Raw != "" && instructions.Type != gjson.Null {
		h.Write([]byte(instructions.Raw))
		hasPrefix = true
	}
	h.Write([]byte{0x00})
	if tools := gjson.GetBytes(body, "tools"); tools.IsArray() && len(tools.Array()) > 0 {
		h.Write([]byte(tools.Raw))
		hasPrefix = true
	}
	if !hasPrefix {
		return ""
	}
	return "wv_" + hex.EncodeToString(h.Sum(nil))
}

// ResponseTranslator is the common surface the proxy's cross-format OpenAI
// dispatch drives. AnthropicSSETranslator (chat/completions) and
// ResponsesToAnthropicWriter (Responses API) both implement it.
type ResponseTranslator interface {
	http.ResponseWriter
	Prelude(streaming bool) error
	Finalize() error
	Summary() ResponseSummary
}

var (
	_ ResponseTranslator = (*AnthropicSSETranslator)(nil)
	_ ResponseTranslator = (*ResponsesToAnthropicWriter)(nil)
	_ ResponseTranslator = (*ResponsesToOpenAIChatWriter)(nil)
)

// ReasoningRequested reports whether the inbound Anthropic request asks the
// model to reason (a `thinking` budget, or an explicit reasoning_effort). Used
// by the proxy to gate the Responses-API dispatch for reasoning OpenAI models.
func (e *RequestEnvelope) ReasoningRequested() bool {
	intent := e.ReasoningIntent()
	return intent.Kind != "" && intent.Kind != ReasoningDisabled
}

// ResponsesRoute carries the inputs to the OpenAI endpoint choice.
type ResponsesRoute struct {
	// Provider is the routed decision's provider.
	Provider string
	// Capabilities is the target model's spec.
	Capabilities router.ModelSpec
	// HasTools reports whether the turn carries function tools.
	HasTools bool
	// ChatOnlyParams reports whether the request uses a parameter only
	// chat/completions can express; see RequiresChatCompletionsParams.
	ChatOnlyParams bool
	// Broad is the rollout flag (ROUTER_OPENAI_RESPONSES_BROAD). Off, only
	// the reasoning tool turn chat/completions outright rejects is promoted.
	Broad bool
}

// UseOpenAIResponsesAPI reports whether a dispatch should use POST
// /v1/responses instead of /v1/chat/completions. Direct OpenAI uses Responses
// for every turn it can express (chat/completions 400s reasoning + tools from
// gpt-5.4 on; only Responses carries encrypted reasoning across turns).
// Gateways keep the narrow rule — reasoning tool turns only — because most
// mount no Responses surface; one without it is downgraded by the caller.
func UseOpenAIResponsesAPI(rt ResponsesRoute) bool {
	narrow := rt.Capabilities.Supports(router.CapReasoning) && rt.HasTools
	switch rt.Provider {
	case providers.ProviderOpenAI:
		if rt.ChatOnlyParams {
			return false
		}
		return rt.Broad || narrow
	case providers.ProviderOpenAIGateway:
		return narrow
	default:
		return false
	}
}

// RequiresChatCompletionsParams reports whether the request asks for something
// /v1/responses cannot express (stop sequences, chat-only sampling knobs,
// inline audio content), keeping the turn on chat/completions rather than
// silently dropping it. Reasoning targets are exempt: they reject those same
// knobs on chat/completions too, so staying there would preserve nothing.
func (e *RequestEnvelope) RequiresChatCompletionsParams(caps router.ModelSpec) bool {
	if e.format == FormatOpenAI && (openAIContentNeedsChatCompletions(e.body) || openAIParamsNeedChatCompletions(e.body)) {
		return true
	}
	if caps.Supports(router.CapReasoning) {
		return false
	}
	key := "stop"
	if e.format == FormatAnthropic {
		key = "stop_sequences"
	}
	stop := gjson.GetBytes(e.body, key)
	switch {
	case stop.Type == gjson.String:
		if stop.String() != "" {
			return true
		}
	case stop.IsArray() && stop.Get("#").Int() > 0:
		return true
	}
	return e.format == FormatOpenAI && openAIParamsNeedChatCompletions(e.body)
}

// openAIParamsNeedChatCompletions reports whether a chat/completions body sets
// a knob the Responses API has no field for; zero/false values are not blockers —
// SDKs send them as defaults, and blocking would keep most turns off Responses.
func openAIParamsNeedChatCompletions(body []byte) bool {
	if n := gjson.GetBytes(body, "n"); n.Type == gjson.Number && n.Int() > 1 {
		return true
	}
	for _, key := range []string{"frequency_penalty", "presence_penalty"} {
		if p := gjson.GetBytes(body, key); p.Type == gjson.Number && p.Float() != 0 {
			return true
		}
	}
	if gjson.GetBytes(body, "logprobs").Type == gjson.True {
		return true
	}
	if bias := gjson.GetBytes(body, "logit_bias"); bias.IsObject() && len(bias.Map()) > 0 {
		return true
	}
	return gjson.GetBytes(body, "seed").Type == gjson.Number
}

// openAIContentNeedsChatCompletions reports whether a message carries a content
// part the Responses input has no equivalent for (chat's inline `input_audio`).
// Dropping it would silently change what the model is asked.
func openAIContentNeedsChatCompletions(body []byte) bool {
	unsupported := false
	gjson.GetBytes(body, "messages").ForEach(func(_, msg gjson.Result) bool {
		content := msg.Get("content")
		if content.IsArray() {
			content.ForEach(func(_, part gjson.Result) bool {
				switch part.Get("type").String() {
				case "text", "image_url", "file", "":
					return true
				default:
					unsupported = true
					return false
				}
			})
		}
		return !unsupported
	})
	return unsupported
}

// reasoningEffortFromAnthropic is retained for callers that only need the
// legacy effort projection. Target validation belongs to ApplyReasoningIntent.
func reasoningEffortFromAnthropic(body []byte) string {
	intent := ParseReasoningIntent(FormatAnthropic, body)
	switch intent.Kind {
	case ReasoningLevel:
		return intent.Level
	case ReasoningBudget:
		return effortForBudget(intent.BudgetTokens)
	}
	return ""
}

// responsesReasoningEffort preserves client-selected effort. Routing policy may
// select a model, but translation must not silently promote a valid level.
func responsesReasoningEffort(eff, model string) string {
	return eff
}

// minResponsesOutputTokens floors max_output_tokens for reasoning targets:
// hidden reasoning exhausts a tiny budget (1 for a quota probe, 64 for title
// generation) before a visible token is emitted. max_output_tokens is a
// ceiling, not an allocation.
const minResponsesOutputTokens = 16000

func (e *RequestEnvelope) buildResponsesFromAnthropic(opts EmitOptions) ([]byte, providers.RequestMutationStats, error) {
	var stats providers.RequestMutationStats
	body, removed, err := filterClaudeCodeOnlyToolsFromAnthropicBody(e.body, opts.KeepCrossVendorOrchestrationTools)
	if err != nil {
		return nil, stats, fmt.Errorf("strip claude-code-only tools: %w", err)
	}
	stats.CCOnlyToolsStripped = removed
	// See buildOpenAIFromAnthropic: native server tools cannot cross to a
	// non-Anthropic upstream without becoming phantom client tools.
	body, stats.ServerToolsStripped = websearch.StripServerTools(body)

	jw := newJSONWriter()
	jw.Obj()
	jw.Key("model")
	jw.Str(opts.TargetModel)
	jw.Key("stream")
	jw.Bool(true)
	// Stateless: prior reasoning items round-trip through signed Anthropic
	// thinking blocks and are replayed below when the client echoes them back.
	jw.Key("store")
	jw.Bool(false)

	if sys := flattenAnthropicSystemGJSON(gjson.GetBytes(body, "system")); sys != "" {
		jw.Key("instructions")
		jw.Str(sys)
	}

	reasoningEnabled := false
	if opts.Capabilities.Supports(router.CapReasoning) {
		intent, err := ApplyReasoningIntent(ParseReasoningIntent(FormatAnthropic, body), opts.Capabilities, opts.ForceReasoningEffort)
		if err != nil {
			return nil, stats, err
		}
		eff := ""
		switch intent.Kind {
		case ReasoningLevel:
			eff = intent.Level
		case ReasoningBudget:
			eff = effortForBudget(intent.BudgetTokens)
		}
		if eff != "" {
			eff = responsesReasoningEffort(eff, opts.TargetModel)
			reasoningEnabled = true
			jw.Key("reasoning")
			jw.Obj()
			jw.Key("effort")
			jw.Str(eff)
			jw.Key("summary")
			jw.Str("detailed")
			jw.EndObj()
		}
	}
	if reasoningEnabled {
		jw.Key("include")
		jw.Arr()
		jw.Str("reasoning.encrypted_content")
		jw.EndArr()
	}

	writeResponsesToolsFromAnthropic(jw, body)
	writeResponsesToolChoiceFromAnthropic(jw, body)

	if mt := gjson.GetBytes(body, "max_tokens"); mt.Exists() && mt.Type == gjson.Number {
		want := mt.Int()
		// Gated on the target: OpenAI applies its own default effort when we send
		// none, so a reasoning model burns the budget on hidden reasoning either way.
		if opts.Capabilities.Supports(router.CapReasoning) {
			want = max(want, minResponsesOutputTokens)
		}
		jw.Key("max_output_tokens")
		jw.Int(clampToModelOutputCap(want, opts.TargetModel))
	}
	// Reasoning models reject temperature != 1 on the Responses API; every other
	// target samples normally, so a non-reasoning turn keeps the client's knobs.
	if samplersAccepted(opts) {
		for _, key := range []string{"temperature", "top_p"} {
			if r := gjson.GetBytes(body, key); r.Exists() {
				jw.Key(key)
				jw.Raw(r.Raw)
			}
		}
	}

	writeResponsesInputFromAnthropic(jw, body)

	jw.EndObj()
	return jw.Bytes(), stats, nil
}

// writeResponsesInputFromAnthropic converts Anthropic messages into Responses
// input items (text/image messages, reasoning, function_call, function_call_output).
func writeResponsesInputFromAnthropic(jw *jsonWriter, body []byte) {
	jw.Key("input")
	jw.Arr()
	gjson.GetBytes(body, "messages").ForEach(func(_, msg gjson.Result) bool {
		role := msg.Get("role").String()
		content := msg.Get("content")
		if content.Type == gjson.String {
			writeResponsesTextMessage(jw, role, content.String())
			return true
		}
		if content.Type != gjson.JSON || !content.IsArray() {
			return true
		}
		var textParts []string
		var imagePartRaws []string
		// Buffered content flushes as one message before every typed item, so
		// turn order (text → reasoning → tool_use) is preserved.
		flushContent := func() {
			if len(textParts) == 0 && len(imagePartRaws) == 0 {
				return
			}
			writeResponsesContentMessage(jw, role, joinNonEmpty(textParts), imagePartRaws)
			textParts, imagePartRaws = nil, nil
		}
		emittedReasoningSignatures := map[string]struct{}{}
		content.ForEach(func(_, block gjson.Result) bool {
			switch block.Get("type").String() {
			case "text":
				if t := block.Get("text").String(); t != "" {
					textParts = append(textParts, t)
				}
			case "image":
				if part := buildResponsesImagePart(block); part != "" {
					imagePartRaws = append(imagePartRaws, part)
				}
			case "thinking":
				sig := block.Get("signature").String()
				if _, emitted := emittedReasoningSignatures[sig]; emitted || !decodeOpenAIReasoningSignatureValid(sig) {
					return true
				}
				flushContent()
				emitResponsesReasoningItem(jw, sig)
				emittedReasoningSignatures[sig] = struct{}{}
			case "tool_use":
				callID, sig := extractOpenAIReasoningSignatureFromID(block.Get("id").String())
				flushContent()
				// Claude Code's round-trip drops the thinking block but keeps
				// the tool_use id, so replay the reasoning item carried on it.
				if sig != "" {
					if _, emitted := emittedReasoningSignatures[sig]; !emitted && emitResponsesReasoningItem(jw, sig) {
						emittedReasoningSignatures[sig] = struct{}{}
					}
				}
				jw.Obj()
				jw.Key("type")
				jw.Str("function_call")
				jw.Key("call_id")
				jw.Str(clampOpenAIToolCallID(callID))
				jw.Key("name")
				jw.Str(sanitizeResponsesToolAlias(block.Get("name").String()))
				inputRaw := block.Get("input").Raw
				if inputRaw == "" {
					inputRaw = "{}"
				}
				jw.Key("arguments")
				jw.Str(inputRaw)
				jw.EndObj()
			case "tool_result":
				flushContent()
				// function_call_output is text-only, so nested images are hoisted
				// into the message that flushes after this item.
				block.Get("content").ForEach(func(_, inner gjson.Result) bool {
					if inner.Get("type").String() == "image" {
						if part := buildResponsesImagePart(inner); part != "" {
							imagePartRaws = append(imagePartRaws, part)
						}
					}
					return true
				})
				jw.Obj()
				jw.Key("type")
				jw.Str("function_call_output")
				jw.Key("call_id")
				callID, _ := extractOpenAIReasoningSignatureFromID(block.Get("tool_use_id").String())
				jw.Str(clampOpenAIToolCallID(callID))
				jw.Key("output")
				jw.Str(flattenAnthropicToolResultContent(block.Get("content")))
				jw.EndObj()
			}
			return true
		})
		flushContent()
		return true
	})
	jw.EndArr()
}

func decodeOpenAIReasoningSignatureValid(sig string) bool {
	_, _, ok := decodeOpenAIReasoningSignature(sig)
	return ok
}

func emitResponsesReasoningItem(jw *jsonWriter, sig string) bool {
	id, enc, ok := decodeOpenAIReasoningSignature(sig)
	if !ok {
		return false
	}
	jw.Obj()
	jw.Key("type")
	jw.Str("reasoning")
	jw.Key("id")
	jw.Str(id)
	jw.Key("encrypted_content")
	jw.Str(enc)
	jw.Key("summary")
	jw.Arr()
	jw.EndArr()
	jw.EndObj()
	return true
}

// writeResponsesTextMessage emits one Responses input message with a single
// typed text part (input_text for user, output_text for assistant).
func writeResponsesTextMessage(jw *jsonWriter, role, text string) {
	writeResponsesContentMessage(jw, role, text, nil)
}

// writeResponsesContentMessage emits one Responses message with a text part
// followed by typed non-text parts (input_image / input_file); those are
// dropped on assistant-role messages, which take output_text only.
func writeResponsesContentMessage(jw *jsonWriter, role, text string, extraPartRaws []string) {
	partType := "input_text"
	if role == "assistant" {
		partType = "output_text"
		extraPartRaws = nil
	}
	if text == "" && len(extraPartRaws) == 0 {
		return
	}
	jw.Obj()
	jw.Key("role")
	jw.Str(role)
	jw.Key("content")
	jw.Arr()
	if text != "" {
		jw.Obj()
		jw.Key("type")
		jw.Str(partType)
		jw.Key("text")
		jw.Str(text)
		jw.EndObj()
	}
	for _, raw := range extraPartRaws {
		jw.Raw(raw)
	}
	jw.EndArr()
	jw.EndObj()
}

// buildResponsesImagePart converts an Anthropic image block to a Responses
// input_image content-part JSON string. Returns "" if the block is malformed.
func buildResponsesImagePart(block gjson.Result) string {
	src := block.Get("source")
	if !src.Exists() {
		return ""
	}
	var url string
	switch src.Get("type").String() {
	case "base64":
		data := src.Get("data").String()
		if data == "" {
			return ""
		}
		mediaType := src.Get("media_type").String()
		if mediaType == "" {
			mediaType = "image/jpeg"
		}
		url = "data:" + mediaType + ";base64," + data
	case "url":
		url = src.Get("url").String()
		if url == "" {
			return ""
		}
	default:
		return ""
	}
	inner := newJSONWriter()
	inner.Obj()
	inner.Key("type")
	inner.Str("input_image")
	inner.Key("image_url")
	inner.Str(url)
	inner.EndObj()
	return string(inner.Bytes())
}

// flattenAnthropicToolResultContent flattens an Anthropic tool_result `content`
// (string or array of text blocks) into a single string for function_call_output.
func flattenAnthropicToolResultContent(content gjson.Result) string {
	switch content.Type {
	case gjson.String:
		return content.String()
	case gjson.JSON:
		if !content.IsArray() {
			return content.Raw
		}
		var parts []string
		content.ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() == "text" {
				if t := block.Get("text").String(); t != "" {
					parts = append(parts, t)
				}
			}
			return true
		})
		return joinNonEmpty(parts)
	default:
		return ""
	}
}

// writeResponsesToolsFromAnthropic emits the Responses flat function-tool
// shape (`{type:"function", name, description, parameters}`, no wrapper).
//
// Tools whose schema survives strictifyOpenAISchema get `strict:true`, which
// turns on grammar-constrained decoding so gpt-5.x can't emit out-of-schema
// args. Schemas that can't be strictified fall back to non-strict emission
// rather than failing the request. toolcheck still validates against the
// ORIGINAL schema downstream — strict mode's nullable optionals produce
// explicit nulls that toolcheck's normalize pass strips before the client sees them.
func writeResponsesToolsFromAnthropic(jw *jsonWriter, body []byte) {
	var collected []responsesFunctionTool
	gjson.GetBytes(body, "tools").ForEach(func(_, tool gjson.Result) bool {
		collected = append(collected, responsesFunctionTool{
			name:        tool.Get("name").String(),
			description: tool.Get("description"),
			schema:      tool.Get("input_schema"),
		})
		return true
	})
	writeResponsesFunctionTools(jw, collected)
}

// responsesFunctionTool is one source-format-neutral function tool.
type responsesFunctionTool struct {
	name        string
	description gjson.Result
	schema      gjson.Result
}

// writeResponsesFunctionTools emits the `tools` key in the flat Responses shape.
func writeResponsesFunctionTools(jw *jsonWriter, tools []responsesFunctionTool) {
	if len(tools) == 0 {
		return
	}
	jw.Key("tools")
	jw.Arr()
	for i, tool := range tools {
		if i >= openAIMaxTools {
			break
		}
		var params any
		strict := false
		if tool.schema.Exists() {
			_ = json.Unmarshal([]byte(tool.schema.Raw), &params)
			params = inlineSchemaDefs(params)
			sanitizeOpenAIToolSchema(params)
			if strictParams, ok := strictifyOpenAISchema(params); ok {
				params = strictParams
				strict = true
			} else {
				observability.Get().Info("Responses strictify fallback — emitting non-strict tool",
					"tool_name", tool.name)
			}
		}
		jw.Obj()
		jw.Key("type")
		jw.Str("function")
		jw.Key("name")
		jw.Str(sanitizeResponsesToolAlias(tool.name))
		if tool.description.Exists() {
			jw.Key("description")
			jw.Raw(tool.description.Raw)
		}
		if params != nil {
			if paramBytes, err := json.Marshal(params); err == nil {
				jw.Key("parameters")
				jw.RawBytes(paramBytes)
				jw.Key("strict")
				jw.Bool(strict)
			}
		}
		jw.EndObj()
	}
	jw.EndArr()
}

// writeResponsesToolChoiceFromAnthropic maps the Anthropic tool_choice to the
// Responses tool_choice shape.
func writeResponsesToolChoiceFromAnthropic(jw *jsonWriter, body []byte) {
	kind, name := anthropicToolChoice(body)
	switch kind {
	case toolChoiceAuto:
		jw.Key("tool_choice")
		jw.Str("auto")
	case toolChoiceRequired:
		jw.Key("tool_choice")
		jw.Str("required")
	case toolChoiceNamed:
		jw.Key("tool_choice")
		jw.Obj()
		jw.Key("type")
		jw.Str("function")
		jw.Key("name")
		jw.Str(sanitizeResponsesToolAlias(name))
		jw.EndObj()
	}
}

func joinNonEmpty(parts []string) string {
	out := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if out != "" {
			out += "\n"
		}
		out += p
	}
	return out
}
