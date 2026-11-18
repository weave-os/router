package translate

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type anthropicSystemOnlyContentBlock string

const (
	anthropicSystemOnlyContentToolAddition anthropicSystemOnlyContentBlock = "tool_addition"
	anthropicSystemOnlyContentToolRemoval  anthropicSystemOnlyContentBlock = "tool_removal"
)

func isAnthropicSystemOnlyContentBlock(blockType string) bool {
	switch anthropicSystemOnlyContentBlock(blockType) {
	case anthropicSystemOnlyContentToolAddition, anthropicSystemOnlyContentToolRemoval:
		return true
	default:
		return false
	}
}

func (e *RequestEnvelope) PrepareAnthropic(in http.Header, opts EmitOptions) (providers.PreparedRequest, error) {
	var body []byte
	var err error
	switch e.format {
	case FormatOpenAI:
		body = e.buildAnthropicFromOpenAI(opts)
	case FormatAnthropic:
		body, err = e.buildAnthropicFromAnthropic(opts)
		if err != nil {
			return providers.PreparedRequest{}, fmt.Errorf("marshal anthropic body: %w", err)
		}
	default:
		return providers.PreparedRequest{}, fmt.Errorf("unsupported source format for Anthropic emit: %d", e.format)
	}
	body, toolAliases, err := sanitizeAnthropicToolNamesBytes(body)
	if err != nil {
		return providers.PreparedRequest{}, fmt.Errorf("sanitize anthropic tool names: %w", err)
	}
	body, err = applyAnthropicCachePolicy(body, false)
	if err != nil {
		return providers.PreparedRequest{}, err
	}
	body, err = dropUnsupportedCacheControlFields(body, opts.TargetProvider == providers.ProviderAnthropic, in.Get("anthropic-beta"))
	if err != nil {
		return providers.PreparedRequest{}, err
	}
	body, err = prepareAnthropicMessageTail(body, opts.Capabilities.Supports(router.CapAssistantPrefill))
	if err != nil {
		return providers.PreparedRequest{}, err
	}
	body, err = applyAnthropicCachePolicy(body, true)
	if err != nil {
		return providers.PreparedRequest{}, err
	}
	if opts.AppendAutonomySystem {
		body, err = appendAutonomySystem(body)
		if err != nil {
			return providers.PreparedRequest{}, err
		}
	}
	body, err = applyServerSideFallback(body, opts)
	if err != nil {
		return providers.PreparedRequest{}, err
	}
	body, err = applyAnthropicSessionAffinity(body, opts)
	if err != nil {
		return providers.PreparedRequest{}, err
	}
	body, err = applyAnthropicFastMode(body, opts)
	if err != nil {
		return providers.PreparedRequest{}, err
	}
	modelID := router.StripDateSuffix(opts.TargetModel)
	maxOutputTokens := modelMaxOutputTokens[modelID]
	if model, ok := catalog.ByID(modelID); ok && model.MaxOutputTokens > 0 {
		maxOutputTokens = model.MaxOutputTokens
	}
	if maxOutputTokens > 0 {
		body = clampFieldBytes(body, "max_tokens", int64(maxOutputTokens))
	}
	return providers.PreparedRequest{Body: body, Headers: deriveAnthropicHeaders(in, opts, body), ResponseToolNames: toolAliases}, nil
}

func applyAnthropicSessionAffinity(body []byte, opts EmitOptions) ([]byte, error) {
	if opts.TargetProvider != providers.ProviderAnthropicGateway || opts.SessionAffinity == "" {
		return body, nil
	}
	if gjson.GetBytes(body, "metadata.user_id").String() != "" {
		return body, nil
	}
	out, err := sjson.SetBytes(body, "metadata.user_id", opts.SessionAffinity)
	if err != nil {
		return nil, fmt.Errorf("set metadata.user_id: %w", err)
	}
	return out, nil
}

func deriveAnthropicHeaders(in http.Header, opts EmitOptions, body []byte) http.Header {
	h := make(http.Header)
	if v := in.Get("anthropic-version"); v != "" {
		h.Set("anthropic-version", v)
	} else {
		h.Set("anthropic-version", "2023-06-01")
	}
	beta := filterBetaHeader(in.Get("anthropic-beta"), opts.TargetModel)
	if opts.EnableExtendedContext && router.Lookup(opts.TargetModel).Supports(router.CapExtendedContext) {
		beta = ensureBetaToken(beta, context1MBeta)
	}
	if gjson.GetBytes(body, "context_management").Exists() {
		beta = ensureBetaToken(beta, contextManagementBeta)
	}
	if gjson.GetBytes(body, "fallbacks").Exists() {
		beta = ensureBetaToken(beta, serverSideFallbackBeta)
	}
	if gjson.GetBytes(body, "speed").Exists() {
		beta = ensureBetaToken(beta, fastModeBeta)
	}
	if opts.TargetProvider == providers.ProviderAnthropic && containsAnthropicSystemOnlyContentBlocks(body) {
		beta = ensureBetaToken(beta, anthropicMidConversationToolChangesBeta)
	}
	if beta != "" {
		h.Set("anthropic-beta", beta)
	}
	return h
}

const context1MBeta = "context-1m-2025-08-07"

func HasContext1MBeta(headers http.Header) bool {
	for _, value := range headers.Values("Anthropic-Beta") {
		if joinKept(value, func(token string) bool { return token == context1MBeta }) != "" {
			return true
		}
	}
	return false
}

const contextManagementBeta = "context-management-2025-06-27"

const serverSideFallbackBeta = "server-side-fallback-2026-07-01"

const anthropicMidConversationToolChangesBeta = "mid-conversation-tool-changes-2026-07-01"

func applyServerSideFallback(body []byte, opts EmitOptions) ([]byte, error) {
	if opts.TargetProvider != providers.ProviderAnthropic ||
		!opts.Capabilities.Supports(router.CapServerSideFallback) {
		if !gjson.GetBytes(body, "fallbacks").Exists() {
			return body, nil
		}
		return sjson.DeleteBytes(body, "fallbacks")
	}
	if !opts.EnableServerSideFallback || gjson.GetBytes(body, "fallbacks").Exists() {
		return body, nil
	}
	return sjson.SetBytes(body, "fallbacks", "default")
}

func ensureBetaToken(beta, token string) string {
	if beta == "" {
		return token
	}
	for _, p := range strings.Split(beta, ",") {
		if strings.TrimSpace(p) == token {
			return beta
		}
	}
	return beta + "," + token
}

func filterBetaHeader(beta, targetModel string) string {
	if beta == "" {
		return ""
	}
	spec := router.Lookup(targetModel)
	return joinKept(beta, func(token string) bool {
		return betaCompatible(token, spec)
	})
}

func betaCompatible(token string, spec router.ModelSpec) bool {
	if strings.Contains(token, "server-side-fallback") {
		return spec.Supports(router.CapServerSideFallback)
	}
	if strings.Contains(token, "context-1m") {
		return spec.Supports(router.CapExtendedContext)
	}
	if strings.Contains(token, "interleaved-thinking") ||
		strings.Contains(token, "adaptive-thinking") {
		return spec.Supports(router.CapAdaptiveThinking)
	}
	if strings.Contains(token, "thinking") {
		return spec.Supports(router.CapAdaptiveThinking) ||
			spec.Supports(router.CapExtendedThinking)
	}
	return true
}

func joinKept(beta string, keep func(string) bool) string {
	if beta == "" {
		return ""
	}
	parts := strings.Split(beta, ",")
	kept := parts[:0]
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t == "" || !keep(t) {
			continue
		}
		kept = append(kept, t)
	}
	return strings.Join(kept, ",")
}

func (e *RequestEnvelope) buildAnthropicFromOpenAI(opts EmitOptions) []byte {
	jw := newJSONWriter()
	jw.Obj()
	jw.Key("model")
	jw.Str(opts.TargetModel)

	if r := gjson.GetBytes(e.body, "stream"); r.Exists() {
		jw.Key("stream")
		jw.Raw(r.Raw)
	}

	writeAnthropicSystemAndMessages(jw, e.body)
	writeAnthropicMaxTokens(jw, e.body, opts.TargetModel, opts.Capabilities)
	writeAnthropicStopSequences(jw, e.body)
	writeAnthropicSamplingParams(jw, e.body, opts.Capabilities)

	kind, _ := openAIToolChoice(e.body)
	suppressTools := kind == toolChoiceNone
	if !suppressTools {
		writeAnthropicTools(jw, e.body)
		writeAnthropicToolChoice(jw, e.body, opts.Capabilities.Supports(router.CapAutoToolChoiceOnly))
	}
	jw.EndObj()
	return jw.Bytes()
}

func writeAnthropicSystemAndMessages(jw *jsonWriter, body []byte) {
	messages := viewJSONPath(body, "messages")
	if !messages.Exists() {
		return
	}

	type pendingToolBatch struct {
		active bool
		blocks []string
	}

	var systemBlocks []string
	var msgParts []string
	var pending pendingToolBatch
	leading := true

	flushToolBatch := func() {
		if !pending.active {
			return
		}
		tw := newJSONWriter()
		tw.Obj()
		tw.Key("role")
		tw.Str("user")
		tw.Key("content")
		tw.Arr()
		for _, b := range pending.blocks {
			tw.Raw(b)
		}
		tw.EndArr()
		tw.EndObj()
		msgParts = append(msgParts, string(tw.Bytes()))
		pending.active = false
		pending.blocks = pending.blocks[:0]
	}

	messages.ForEach(func(_, msg gjson.Result) bool {
		role := msg.Get("role").String()
		content := msg.Get("content")

		switch {
		case role == "system" && !leading:
			flushToolBatch()
			msgParts = append(msgParts, cacheControlOnLastBlockWithPolicy(buildAnthropicUserMessage("user", content), sourceMessageCacheControl(msg)))

		case role == "system":
			policy := sourceMessageCacheControl(msg)
			if content.Type == gjson.String {
				if s := content.String(); s != "" {
					sb := newJSONWriter()
					sb.Obj()
					sb.Key("type")
					sb.Str("text")
					sb.Key("text")
					sb.Str(s)
					sb.EndObj()
					systemBlocks = append(systemBlocks, appendCacheControlRaw(string(sb.Bytes()), policy))
				}
			} else if content.IsArray() {
				content.ForEach(func(_, part gjson.Result) bool {
					if part.Get("type").String() == "text" {
						if t := part.Get("text").String(); t != "" {
							sb := newJSONWriter()
							sb.Obj()
							sb.Key("type")
							sb.Str("text")
							sb.Key("text")
							sb.Str(t)
							sb.EndObj()
							systemBlocks = append(systemBlocks, appendCacheControlRaw(string(sb.Bytes()), policy))
						}
					}
					return true
				})
			}

		case role == "tool":
			leading = false
			toolCallID := msg.Get("tool_call_id").String()
			blockRaw := buildToolResultBlock(toolCallID, content)
			pending.active = true
			pending.blocks = append(pending.blocks, blockRaw)

		case role == "assistant":
			leading = false
			flushToolBatch()
			msgParts = append(msgParts, cacheControlOnLastBlockWithPolicy(buildAnthropicAssistantMessage(msg), sourceMessageCacheControl(msg)))

		default:
			leading = false
			flushToolBatch()
			msgParts = append(msgParts, cacheControlOnLastBlockWithPolicy(buildAnthropicUserMessage(role, content), sourceMessageCacheControl(msg)))
		}
		return true
	})
	flushToolBatch()

	if len(systemBlocks) > 0 {
		jw.Key("system")
		jw.Arr()
		for _, b := range systemBlocks {
			jw.Raw(b)
		}
		jw.EndArr()
	}

	if len(msgParts) > 0 {
		jw.Key("messages")
		jw.Arr()
		for _, m := range msgParts {
			jw.Raw(m)
		}
		jw.EndArr()
	}
}

func appendCacheControlRaw(block, policy string) string {
	if len(block) < 2 || block[len(block)-1] != '}' || !strings.Contains(block, ":") {
		return block
	}
	if policy == "" {
		return block
	}
	return block[:len(block)-1] + `,"cache_control":` + policy + "}"
}

func cacheControlOnLastBlockWithPolicy(msg, policy string) string {
	if policy == "" {
		return msg
	}
	content := gjson.Get(msg, "content")
	role := gjson.Get(msg, "role").String()

	if content.IsArray() {
		blocks := content.Array()
		if len(blocks) == 0 {
			return msg
		}
		jw := newJSONWriter()
		jw.Obj()
		jw.Key("role")
		jw.Str(role)
		jw.Key("content")
		jw.Arr()
		for i, b := range blocks {
			if i == len(blocks)-1 {
				jw.Raw(appendCacheControlRaw(b.Raw, policy))
			} else {
				jw.Raw(b.Raw)
			}
		}
		jw.EndArr()
		jw.EndObj()
		return string(jw.Bytes())
	}

	if content.Type == gjson.String {
		jw := newJSONWriter()
		jw.Obj()
		jw.Key("role")
		jw.Str(role)
		jw.Key("content")
		jw.Arr()
		jw.Obj()
		jw.Key("type")
		jw.Str("text")
		jw.Key("text")
		jw.Str(content.String())
		jw.Key("cache_control")
		jw.Raw(policy)
		jw.EndObj()
		jw.EndArr()
		jw.EndObj()
		return string(jw.Bytes())
	}

	return msg
}

func sourceMessageCacheControl(msg gjson.Result) string {
	policy := msg.Get("cache_control")
	if !policy.Exists() {
		return ""
	}
	return policy.Raw
}

func buildToolResultBlock(toolUseID string, content gjson.Result) string {
	jw := newJSONWriter()
	jw.Obj()
	jw.Key("type")
	jw.Str("tool_result")
	jw.Key("tool_use_id")
	jw.Str(sanitizeToolUseID(toolUseID))

	switch content.Type {
	case gjson.String:
		jw.Key("content")
		jw.Str(content.String())
	case gjson.JSON:
		if content.IsArray() {
			var parts []string
			content.ForEach(func(_, part gjson.Result) bool {
				switch part.Get("type").String() {
				case "text":
					if t := part.Get("text").String(); t != "" {
						pb := newJSONWriter()
						pb.Obj()
						pb.Key("type")
						pb.Str("text")
						pb.Key("text")
						pb.Str(t)
						pb.EndObj()
						parts = append(parts, string(pb.Bytes()))
					}
				case "image_url":
					urlStr := part.Get("image_url.url").String()
					if urlStr != "" {
						parts = append(parts, buildAnthropicImageBlock(urlStr))
					}
				}
				return true
			})
			if len(parts) > 0 {
				jw.Key("content")
				jw.Arr()
				for _, p := range parts {
					jw.Raw(p)
				}
				jw.EndArr()
			} else {
				jw.Key("content")
				jw.Str("")
			}
		} else {
			jw.Key("content")
			jw.Str("")
		}
	default:
		jw.Key("content")
		jw.Str("")
	}

	jw.EndObj()
	return string(jw.Bytes())
}

func buildAnthropicAssistantMessage(msg gjson.Result) string {
	jw := newJSONWriter()
	jw.Obj()
	jw.Key("role")
	jw.Str("assistant")

	toolCalls := msg.Get("tool_calls")
	if !toolCalls.Exists() || !toolCalls.IsArray() || toolCalls.Get("#").Int() == 0 {
		content := msg.Get("content")
		jw.Key("content")
		writeAnthropicContentValue(jw, content)
		jw.EndObj()
		return string(jw.Bytes())
	}

	jw.Key("content")
	jw.Arr()
	if text := openAIContentTextGJSON(msg.Get("content")); text != "" {
		jw.Obj()
		jw.Key("type")
		jw.Str("text")
		jw.Key("text")
		jw.Str(text)
		jw.EndObj()
	}
	toolCalls.ForEach(func(_, tc gjson.Result) bool {
		id := sanitizeToolUseID(tc.Get("id").String())
		name := tc.Get("function.name").String()
		argsStr := tc.Get("function.arguments").String()

		jw.Obj()
		jw.Key("type")
		jw.Str("tool_use")
		jw.Key("id")
		jw.Str(id)
		jw.Key("name")
		jw.Str(name)
		jw.Key("input")
		if gjson.Valid(argsStr) {
			jw.Raw(argsStr)
		} else {
			jw.Raw("{}")
		}
		jw.EndObj()
		return true
	})
	jw.EndArr()

	jw.EndObj()
	return string(jw.Bytes())
}

func buildAnthropicUserMessage(role string, content gjson.Result) string {
	jw := newJSONWriter()
	jw.Obj()
	jw.Key("role")
	jw.Str(role)
	jw.Key("content")
	writeAnthropicContentValue(jw, content)
	jw.EndObj()
	return string(jw.Bytes())
}

func writeAnthropicContentValue(jw *jsonWriter, content gjson.Result) {
	if content.Type == gjson.String {
		jw.Str(content.String())
		return
	}
	if !content.IsArray() {
		if content.Raw != "" {
			jw.Raw(content.Raw)
		} else {
			jw.Null()
		}
		return
	}
	jw.Arr()
	content.ForEach(func(_, part gjson.Result) bool {
		switch part.Get("type").String() {
		case "text":
			jw.Obj()
			jw.Key("type")
			jw.Str("text")
			jw.Key("text")
			jw.Str(part.Get("text").String())
			jw.EndObj()
		case "image_url":
			urlStr := part.Get("image_url.url").String()
			if urlStr != "" {
				jw.Raw(buildAnthropicImageBlock(urlStr))
			}
		}
		return true
	})
	jw.EndArr()
}

func buildAnthropicImageBlock(urlStr string) string {
	jw := newJSONWriter()
	jw.Obj()
	jw.Key("type")
	jw.Str("image")
	jw.Key("source")
	jw.Obj()
	if mime, data, ok := parseDataURL(urlStr); ok {
		jw.Key("type")
		jw.Str("base64")
		jw.Key("media_type")
		jw.Str(mime)
		jw.Key("data")
		jw.Str(data)
	} else {
		jw.Key("type")
		jw.Str("url")
		jw.Key("url")
		jw.Str(urlStr)
	}
	jw.EndObj()
	jw.EndObj()
	return string(jw.Bytes())
}

func writeAnthropicMaxTokens(jw *jsonWriter, body []byte, targetModel string, capabilities router.ModelSpec) {
	if r := gjson.GetBytes(body, "max_tokens"); r.Exists() {
		jw.Key("max_tokens")
		jw.Raw(r.Raw)
		return
	}
	if r := gjson.GetBytes(body, "max_completion_tokens"); r.Exists() {
		jw.Key("max_tokens")
		jw.Raw(r.Raw)
		return
	}
	jw.Key("max_tokens")
	jw.Int(defaultAnthropicOutputTokens(targetModel, capabilities))
}

func writeAnthropicStopSequences(jw *jsonWriter, body []byte) {
	r := gjson.GetBytes(body, "stop")
	if !r.Exists() {
		return
	}
	if r.Type == gjson.String {
		jw.Key("stop_sequences")
		jw.Arr()
		jw.Str(r.String())
		jw.EndArr()
		return
	}
	if r.IsArray() {
		jw.Key("stop_sequences")
		jw.Raw(r.Raw)
	}
}

func writeAnthropicTools(jw *jsonWriter, body []byte) {
	tools := gjson.GetBytes(body, "tools")
	if !tools.Exists() || !tools.IsArray() || tools.Get("#").Int() == 0 {
		return
	}
	jw.Key("tools")
	jw.Arr()
	tools.ForEach(func(_, tool gjson.Result) bool {
		fn := tool.Get("function")
		if !fn.Exists() {
			return true
		}
		jw.Obj()
		jw.Key("name")
		jw.Str(fn.Get("name").String())
		if desc := fn.Get("description"); desc.Exists() {
			jw.Key("description")
			jw.Raw(desc.Raw)
		}
		if params := fn.Get("parameters"); params.Exists() {
			jw.Key("input_schema")
			jw.Raw(anthropicToolSchemaRaw(params))
		}
		jw.EndObj()
		return true
	})
	jw.EndArr()
}

func hasAnthropicFunctionTools(body []byte) bool {
	found := false
	gjson.GetBytes(body, "tools").ForEach(func(_, tool gjson.Result) bool {
		found = tool.Get("function").Exists()
		return !found
	})
	return found
}

func writeAnthropicToolChoice(jw *jsonWriter, body []byte, autoOnly bool) {
	kind, name := openAIToolChoice(body)
	if autoOnly && (kind == toolChoiceRequired || kind == toolChoiceNamed) {
		kind = toolChoiceAuto
	}
	parallel := gjson.GetBytes(body, "parallel_tool_calls")
	disableParallel := isJSONBool(parallel) && !parallel.Bool() && hasAnthropicFunctionTools(body)
	if disableParallel && kind == toolChoiceAbsent {
		kind = toolChoiceAuto
	}
	var choiceType string
	switch kind {
	case toolChoiceAuto:
		choiceType = "auto"
	case toolChoiceRequired:
		choiceType = "any"
	case toolChoiceNamed:
		choiceType = "tool"
	default:
		return
	}
	tw := newJSONWriter()
	tw.Obj()
	tw.Key("type")
	tw.Str(choiceType)
	if kind == toolChoiceNamed {
		tw.Key("name")
		tw.Str(name)
	}
	if disableParallel {
		tw.Key("disable_parallel_tool_use")
		tw.Bool(true)
	}
	tw.EndObj()
	jw.Key("tool_choice")
	jw.Raw(string(tw.Bytes()))
}

var anthropicSamplingParams = []string{"temperature", "top_p", "top_k"}

func writeAnthropicSamplingParams(jw *jsonWriter, body []byte, capabilities router.ModelSpec) {
	if capabilities.Supports(router.CapSamplingParamsUnsupported) {
		return
	}
	for _, key := range anthropicSamplingParams {
		if value := gjson.GetBytes(body, key); value.Exists() {
			jw.Key(key)
			jw.Raw(value.Raw)
		}
	}
}

func (e *RequestEnvelope) buildAnthropicFromAnthropic(opts EmitOptions) ([]byte, error) {
	body, err := normalizeAnthropicSystemOnlyContentBlocks(e.body)
	if err != nil {
		return nil, fmt.Errorf("normalize system-only content blocks: %w", err)
	}
	body, err = hoistAnthropicSystemMessages(body)
	if err != nil {
		return nil, fmt.Errorf("hoist system messages: %w", err)
	}
	ov := resolveAnthropicOverrides(body, opts)
	return applyOverrides(body, ov)
}

func normalizeAnthropicSystemOnlyContentBlocks(body []byte) ([]byte, error) {
	msgs := gjson.GetBytes(body, "messages")
	if !msgs.IsArray() {
		return body, nil
	}
	var kept []string
	changed := false
	for _, msg := range msgs.Array() {
		if msg.Get("role").String() == "system" {
			kept = append(kept, msg.Raw)
			continue
		}
		content := msg.Get("content")
		if !content.IsArray() {
			kept = append(kept, msg.Raw)
			continue
		}
		var remaining []string
		var systemOnly []string
		msgChanged := false
		for _, part := range content.Array() {
			if isAnthropicSystemOnlyContentBlock(part.Get("type").String()) {
				systemOnly = append(systemOnly, part.Raw)
				msgChanged = true
				continue
			}
			remaining = append(remaining, part.Raw)
		}
		if !msgChanged {
			kept = append(kept, msg.Raw)
			continue
		}
		changed = true
		role := msg.Get("role").String()
		systemMessage := newJSONWriter()
		systemMessage.Obj()
		systemMessage.Key("role")
		systemMessage.Str("system")
		systemMessage.Key("content")
		systemMessage.Arr()
		for _, block := range systemOnly {
			systemMessage.Raw(block)
		}
		systemMessage.EndArr()
		systemMessage.EndObj()
		if role == "assistant" {
			kept = append(kept, string(systemMessage.Bytes()))
		}
		if len(remaining) > 0 {
			rewritten, err := sjson.SetRawBytes([]byte(msg.Raw), "content", []byte("["+strings.Join(remaining, ",")+"]"))
			if err != nil {
				return nil, fmt.Errorf("strip system-only content blocks: %w", err)
			}
			kept = append(kept, string(rewritten))
		}
		if role != "assistant" {
			kept = append(kept, string(systemMessage.Bytes()))
		}
	}
	if !changed {
		return body, nil
	}
	out, err := sjson.SetRawBytes(body, "messages", []byte("["+strings.Join(kept, ",")+"]"))
	if err != nil {
		return nil, fmt.Errorf("rebuild messages after system-only normalization: %w", err)
	}
	return out, nil
}

func containsAnthropicSystemOnlyContentBlock(content gjson.Result) bool {
	if !content.IsArray() {
		return false
	}
	for _, part := range content.Array() {
		if isAnthropicSystemOnlyContentBlock(part.Get("type").String()) {
			return true
		}
	}
	return false
}

func containsAnthropicSystemOnlyContentBlocks(body []byte) bool {
	for _, msg := range viewJSONPath(body, "messages").Array() {
		if containsAnthropicSystemOnlyContentBlock(msg.Get("content")) {
			return true
		}
	}
	return false
}

func hoistAnthropicSystemMessages(body []byte) ([]byte, error) {
	msgs := gjson.GetBytes(body, "messages")
	if !msgs.IsArray() {
		return body, nil
	}

	var hoisted []string
	var kept []string
	var hoistedOutputConfig string
	leading := true
	leadingSystemFound := false
	rewritten := false
	for _, msg := range msgs.Array() {
		isSystem := msg.Get("role").String() == "system"
		preserveSystem := isSystem && containsAnthropicSystemOnlyContentBlock(msg.Get("content"))
		switch {
		case preserveSystem:
			kept = append(kept, msg.Raw)
		case isSystem && leading:
			leadingSystemFound = true
			hoisted = append(hoisted, anthropicSystemTexts(msg.Get("content"))...)
			if config := msg.Get("output_config"); config.Exists() && config.Type != gjson.Null && hoistedOutputConfig == "" {
				hoistedOutputConfig = config.Raw
			}
		case isSystem:
			demoted := []byte(msg.Raw)
			if config := msg.Get("output_config"); config.Exists() {
				var err error
				demoted, err = sjson.DeleteBytes(demoted, "output_config")
				if err != nil {
					return nil, fmt.Errorf("remove demoted system output config: %w", err)
				}
				if config.Type != gjson.Null && hoistedOutputConfig == "" {
					hoistedOutputConfig = config.Raw
				}
			}
			if gjson.GetBytes(demoted, "clear_at").Exists() {
				var err error
				demoted, err = sjson.DeleteBytes(demoted, "clear_at")
				if err != nil {
					return nil, fmt.Errorf("remove demoted system clear_at: %w", err)
				}
			}
			demoted, err := sjson.SetBytes(demoted, "role", "user")
			if err != nil {
				return nil, fmt.Errorf("demote system message: %w", err)
			}
			kept = append(kept, string(demoted))
			rewritten = true
		default:
			leading = false
			kept = append(kept, msg.Raw)
		}
	}
	requestOutputConfig := gjson.GetBytes(body, "output_config")
	if hoistedOutputConfig != "" && (!requestOutputConfig.Exists() || requestOutputConfig.Type == gjson.Null) {
		var err error
		body, err = sjson.SetRawBytes(body, "output_config", []byte(hoistedOutputConfig))
		if err != nil {
			return nil, fmt.Errorf("hoist system output config: %w", err)
		}
	}
	if !leadingSystemFound && !rewritten {
		return body, nil
	}
	if len(hoisted) == 0 {
		out, err := sjson.SetRawBytes(body, "messages", []byte("["+strings.Join(kept, ",")+"]"))
		if err != nil {
			return nil, fmt.Errorf("rebuild messages: %w", err)
		}
		return out, nil
	}

	sw := newJSONWriter()
	sw.Arr()
	switch existing := gjson.GetBytes(body, "system"); {
	case existing.Type == gjson.String:
		if s := existing.String(); s != "" {
			writeAnthropicTextBlock(sw, s)
		}
	case existing.IsArray():
		existing.ForEach(func(_, b gjson.Result) bool {
			sw.Raw(b.Raw)
			return true
		})
	}
	for _, t := range hoisted {
		writeAnthropicTextBlock(sw, t)
	}
	sw.EndArr()

	out, err := sjson.SetRawBytes(body, "messages", []byte("["+strings.Join(kept, ",")+"]"))
	if err != nil {
		return nil, fmt.Errorf("rebuild messages: %w", err)
	}
	out, err = sjson.SetRawBytes(out, "system", sw.Bytes())
	if err != nil {
		return nil, fmt.Errorf("set system: %w", err)
	}
	return out, nil
}

func sanitizeAnthropicToolSchemasBytes(body []byte) ([]byte, error) {
	tools := gjson.GetBytes(body, "tools")
	if !tools.Exists() || !tools.IsArray() || tools.Get("#").Int() == 0 {
		return body, nil
	}
	raw, err := json.Marshal(sanitizeAnthropicTools(tools.Value()))
	if err != nil {
		return nil, err
	}
	return sjson.SetRawBytes(body, "tools", raw)
}

const maxAnthropicHistoricalToolNameChars = 200

var anthropicDeclaredToolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

func sanitizeAnthropicToolNamesBytes(body []byte) ([]byte, map[string]string, error) {
	tools := gjson.GetBytes(body, "tools").Array()
	reservedNames := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		name := tool.Get("name").String()
		if anthropicDeclaredToolNamePattern.MatchString(name) {
			reservedNames[name] = struct{}{}
		}
	}

	aliases := make(map[string]string)
	for _, tool := range tools {
		name := tool.Get("name").String()
		if !anthropicDeclaredToolNamePattern.MatchString(name) {
			if _, exists := aliases[name]; !exists {
				aliases[name] = uniqueSanitizedAnthropicToolName(name, reservedNames)
			}
		}
	}
	choice := gjson.GetBytes(body, "tool_choice")
	choiceName := choice.Get("name").String()
	if choice.Get("type").String() == "tool" && !anthropicDeclaredToolNamePattern.MatchString(choiceName) {
		if _, exists := aliases[choiceName]; !exists {
			aliases[choiceName] = uniqueSanitizedAnthropicToolName(choiceName, reservedNames)
		}
	}

	out, err := rewriteMessageBlocks(
		body,
		func(block gjson.Result) bool {
			if block.Get("type").String() != "tool_use" {
				return false
			}
			name := block.Get("name").String()
			_, hasAlias := aliases[name]
			return hasAlias || utf8.RuneCountInString(name) > maxAnthropicHistoricalToolNameChars
		},
		func(raw string) (string, error) {
			name := gjson.Get(raw, "name").String()
			alias, ok := aliases[name]
			if !ok {
				alias = sanitizedAnthropicToolName(name)
			}
			rewritten, rewriteErr := sjson.Set(raw, "name", alias)
			if rewriteErr != nil {
				return "", fmt.Errorf("rewrite tool_use name: %w", rewriteErr)
			}
			return rewritten, nil
		},
	)
	if err != nil {
		return nil, nil, err
	}
	for index, tool := range gjson.GetBytes(out, "tools").Array() {
		name := tool.Get("name").String()
		alias, ok := aliases[name]
		if !ok {
			continue
		}
		out, err = sjson.SetBytes(out, fmt.Sprintf("tools.%d.name", index), alias)
		if err != nil {
			return nil, nil, fmt.Errorf("rewrite declared tool name: %w", err)
		}
	}
	if choice.Get("type").String() == "tool" {
		if alias, ok := aliases[choiceName]; ok {
			out, err = sjson.SetBytes(out, "tool_choice.name", alias)
			if err != nil {
				return nil, nil, fmt.Errorf("rewrite tool_choice name: %w", err)
			}
		}
	}
	out, err = rewriteAnthropicToolReferences(out, aliases)
	if err != nil {
		return nil, nil, err
	}
	clientNames := make(map[string]string, len(aliases))
	for name, alias := range aliases {
		clientNames[alias] = name
	}
	return out, clientNames, nil
}

func sanitizedAnthropicToolName(name string) string {
	nameHash := sha256.Sum256([]byte(name))
	return fmt.Sprintf("invalid_tool_%x", nameHash[:20])
}

func uniqueSanitizedAnthropicToolName(name string, reservedNames map[string]struct{}) string {
	for attempt := 0; ; attempt++ {
		hashInput := name
		if attempt > 0 {
			hashInput = fmt.Sprintf("%s_%d", name, attempt)
		}
		alias := sanitizedAnthropicToolName(hashInput)
		if _, exists := reservedNames[alias]; exists {
			continue
		}
		reservedNames[alias] = struct{}{}
		return alias
	}
}

func anthropicToolSchemaRaw(schema gjson.Result) string {
	raw, err := json.Marshal(sanitizeAnthropicInputSchema(schema.Value()))
	if err != nil {
		return schema.Raw
	}
	return string(raw)
}

func sanitizeAnthropicTools(v any) any {
	tools, ok := v.([]any)
	if !ok {
		return v
	}
	out := make([]any, 0, len(tools))
	for _, item := range tools {
		tool, ok := item.(map[string]any)
		if !ok {
			out = append(out, item)
			continue
		}
		copied := make(map[string]any, len(tool))
		nativeDomainFilter := isAnthropicNativeDomainFilterTool(tool)
		for k, child := range tool {
			if k == "input_schema" {
				copied[k] = sanitizeAnthropicInputSchema(child)
				continue
			}
			if nativeDomainFilter && (k == "allowed_domains" || k == "blocked_domains") && isEmptyDomainList(child) {
				continue
			}
			copied[k] = child
		}
		out = append(out, copied)
	}
	return out
}

func isAnthropicNativeDomainFilterTool(tool map[string]any) bool {
	typ, _ := tool["type"].(string)
	return strings.HasPrefix(typ, "web_search_") || strings.HasPrefix(typ, "web_fetch_")
}

func isEmptyDomainList(v any) bool {
	switch arr := v.(type) {
	case []any:
		return len(arr) == 0
	case []string:
		return len(arr) == 0
	default:
		return false
	}
}

func sanitizeAnthropicInputSchema(v any) any {
	sanitized := sanitizeAnthropicSchema(v)
	root, ok := sanitized.(map[string]any)
	if !ok {
		return sanitized
	}
	return liftAnthropicTopLevelComposition(root)
}

func liftAnthropicTopLevelComposition(root map[string]any) map[string]any {
	keywords := []string{"allOf", "anyOf", "oneOf"}
	if !slices.ContainsFunc(keywords, func(k string) bool { _, ok := root[k]; return ok }) {
		return root
	}
	out := make(map[string]any, len(root))
	for k, v := range root {
		if !slices.Contains(keywords, k) {
			out[k] = v
		}
	}
	mergedProps, _ := out["properties"].(map[string]any)
	mergedProps = maps.Clone(mergedProps)
	if mergedProps == nil {
		mergedProps = map[string]any{}
	}
	required := schemaRequiredSet(out)
	for _, k := range keywords {
		branches, _ := root[k].([]any)
		var requiredInAll map[string]struct{}
		for _, b := range branches {
			branch, ok := resolveLocalSchemaRef(root, b).(map[string]any)
			if !ok {
				requiredInAll = map[string]struct{}{}
				continue
			}
			branch = liftAnthropicTopLevelComposition(branch)
			if branchProps, ok := branch["properties"].(map[string]any); ok {
				for name, schema := range branchProps {
					if _, exists := mergedProps[name]; !exists {
						mergedProps[name] = schema
					}
				}
			}
			branchRequired := schemaRequiredSet(branch)
			switch {
			case k == "allOf":
				maps.Copy(required, branchRequired)
			case requiredInAll == nil:
				requiredInAll = branchRequired
			default:
				maps.DeleteFunc(requiredInAll, func(name string, _ struct{}) bool {
					_, ok := branchRequired[name]
					return !ok
				})
			}
		}
		maps.Copy(required, requiredInAll)
	}
	out["type"] = "object"
	out["properties"] = mergedProps
	delete(out, "required")
	if len(required) > 0 {
		names := slices.Sorted(maps.Keys(required))
		requiredNames := make([]any, len(names))
		for i, name := range names {
			requiredNames[i] = name
		}
		out["required"] = requiredNames
	}
	return out
}

func resolveLocalSchemaRef(root map[string]any, branch any) any {
	node, ok := branch.(map[string]any)
	if !ok {
		return branch
	}
	ref, _ := node["$ref"].(string)
	for _, defsKey := range []string{"$defs", "definitions"} {
		name, found := strings.CutPrefix(ref, "#/"+defsKey+"/")
		if !found {
			continue
		}
		defs, _ := root[defsKey].(map[string]any)
		if target, exists := defs[name]; exists {
			return target
		}
	}
	return branch
}

func schemaRequiredSet(schema map[string]any) map[string]struct{} {
	set := map[string]struct{}{}
	requiredValues, _ := schema["required"].([]any)
	for _, v := range requiredValues {
		if name, ok := v.(string); ok {
			set[name] = struct{}{}
		}
	}
	return set
}

func sanitizeAnthropicSchema(v any) any {
	switch node := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(node))
		for k, child := range node {
			if k == "pattern" {
				continue
			}
			out[k] = sanitizeAnthropicSchema(child)
		}
		return out
	case []any:
		out := make([]any, len(node))
		for i, child := range node {
			out[i] = sanitizeAnthropicSchema(child)
		}
		return out
	default:
		return v
	}
}

func anthropicSystemTexts(content gjson.Result) []string {
	if content.Type == gjson.String {
		if s := content.String(); s != "" {
			return []string{s}
		}
		return nil
	}
	if !content.IsArray() {
		return nil
	}
	var out []string
	content.ForEach(func(_, part gjson.Result) bool {
		if part.Get("type").String() == "text" {
			if t := part.Get("text").String(); t != "" {
				out = append(out, t)
			}
		}
		return true
	})
	return out
}

func writeAnthropicTextBlock(jw *jsonWriter, text string) {
	jw.Obj()
	jw.Key("type")
	jw.Str("text")
	jw.Key("text")
	jw.Str(text)
	jw.EndObj()
}

func sanitizeToolUseID(id string) string {
	if id == "" {
		return id
	}
	b := []byte(id)
	changed := false
	for i, c := range b {
		if !isIdentifierChar(rune(c)) {
			b[i] = '_'
			changed = true
		}
	}
	if !changed {
		return id
	}
	return string(b)
}

func uniqueToolUseIDWithNonce(id, nonce string) string {
	cleanID, sig := extractSignatureFromID(id)
	return embedSignatureInID(uniqueSanitizedIDWithNonce(cleanID, nonce), sig)
}

func uniqueSanitizedIDWithNonce(id, nonce string) string {
	if nonce == "" {
		return sanitizeToolUseID(id)
	}
	clean := sanitizeToolUseID(id)
	if clean == "" {
		return "toolu_router_" + nonce
	}
	if strings.HasSuffix(clean, "_"+nonce) {
		return clean
	}
	return clean + "_" + nonce
}
