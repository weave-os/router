package translate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/websearch"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const maxToolCallIDLen = 64

func resolveReasoningEffortFor(opts EmitOptions) string {
	if opts.ForceEffort != "" {
		return ResolveForceEffort(opts.Capabilities, opts.ForceEffort)
	}
	return opts.ForceReasoningEffort
}

func reasoningEffortAcceptedOnChatCompletions(opts EmitOptions, hasTools bool) bool {
	if strings.HasPrefix(opts.TargetModel, "gpt-5") {
		return false
	}
	if hasTools && providers.IsGateway(opts.TargetProvider) {
		return false
	}
	return true
}

func toolTurnNeedsExplicitEffortNone(opts EmitOptions, hasTools bool) bool {
	return hasTools && opts.TargetProvider == providers.ProviderOpenAI &&
		strings.HasPrefix(opts.TargetModel, "gpt-5.6")
}

func samplersAccepted(opts EmitOptions) bool {
	return !opts.Capabilities.Supports(router.CapReasoning) ||
		!isOpenAIReasoningTarget(opts)
}

func isOpenAIReasoningTarget(opts EmitOptions) bool {
	return opts.TargetProvider == providers.ProviderOpenAI ||
		strings.HasPrefix(opts.TargetModel, "gpt-") || strings.HasPrefix(opts.TargetModel, "o")
}

func clampOpenAIToolCallID(id string) string {
	if id == "" {
		return id
	}
	cleanID, _ := extractSignatureFromID(id)
	cleanID = sanitizeToolUseID(cleanID)
	if len(cleanID) <= maxToolCallIDLen {
		return cleanID
	}
	sum := sha256.Sum256([]byte(cleanID))
	return "tc_" + hex.EncodeToString(sum[:20])
}

func hasNonEmptyTools(body []byte) bool {
	tools := gjson.GetBytes(body, "tools")
	return tools.Exists() && tools.IsArray() && tools.Get("#").Int() > 0
}

func (e *RequestEnvelope) PrepareOpenAI(in http.Header, opts EmitOptions) (providers.PreparedRequest, error) {
	e, err := e.withRouterSystemAppends(opts)
	if err != nil {
		return providers.PreparedRequest{}, err
	}
	e, err = e.withUnambiguousReadPrefixes()
	if err != nil {
		return providers.PreparedRequest{}, err
	}
	var body []byte
	var stats providers.RequestMutationStats
	var toolNames map[string]string
	switch e.format {
	case FormatOpenAI:
		body, err = e.buildOpenAIFromOpenAI(opts)
	case FormatAnthropic:
		body, stats, err = e.buildOpenAIFromAnthropic(opts)
		toolNames = openAIToolNameAliases(e.body, anthropicRequestToolNamePaths)
	default:
		return providers.PreparedRequest{}, fmt.Errorf("unsupported source format for OpenAI emit: %d", e.format)
	}
	if err != nil {
		return providers.PreparedRequest{}, err
	}
	body, err = ensureOpenAIToolParameters(body)
	if err != nil {
		return providers.PreparedRequest{}, err
	}
	if toolTurnNeedsExplicitEffortNone(opts, hasNonEmptyTools(body)) {
		body, err = sjson.SetBytes(body, "reasoning_effort", "none")
		if err != nil {
			return providers.PreparedRequest{}, fmt.Errorf("set reasoning_effort none: %w", err)
		}
	}
	headers := make(http.Header)
	body, err = applySessionAffinity(body, headers, opts)
	if err != nil {
		return providers.PreparedRequest{}, err
	}
	body, err = ApplyOpenAIFastMode(body, opts)
	if err != nil {
		return providers.PreparedRequest{}, err
	}
	return providers.PreparedRequest{Body: body, Headers: headers, Stats: stats, ResponseToolNames: toolNames}, nil
}

func applySessionAffinity(body []byte, headers http.Header, opts EmitOptions) ([]byte, error) {
	switch opts.TargetProvider {
	case providers.ProviderOpenRouter:
		if opts.SessionAffinity != "" {
			headers.Set("x-session-id", opts.SessionAffinity)
		}
		return body, nil
	case providers.ProviderOpenAI, providers.ProviderOpenAIGateway:
		if opts.TargetProvider == providers.ProviderOpenAIGateway &&
			strings.HasPrefix(opts.TargetModel, "grok") && opts.SessionAffinity != "" {
			headers.Set("x-grok-conv-id", opts.SessionAffinity)
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
			cacheKey = stablePromptCacheKey(body)
			if cacheKey == "" {
				return body, nil
			}
		}
		out, err := sjson.SetBytes(body, "prompt_cache_key", cacheKey)
		if err != nil {
			return nil, fmt.Errorf("set prompt_cache_key: %w", err)
		}
		return out, nil
	case providers.ProviderBedrock:
		return body, nil
	case providers.ProviderXAI:
		if opts.SessionAffinity != "" {
			headers.Set("x-grok-conv-id", opts.SessionAffinity)
		}
		return body, nil
	}
	if providers.IsOpenAICompat(opts.TargetProvider) && opts.SessionAffinity != "" {
		headers.Set("x-session-affinity", opts.SessionAffinity)
	}
	return body, nil
}

func stablePromptCacheKey(body []byte) string {
	h := sha256.New()
	var hasPrefix bool
	gjson.GetBytes(body, "messages").ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() == "system" {
			if content := msg.Get("content"); content.Raw != "" {
				h.Write([]byte(content.Raw))
				hasPrefix = true
			}
		}
		return true
	})
	h.Write([]byte{0x00})
	if hasNonEmptyTools(body) {
		h.Write([]byte(gjson.GetBytes(body, "tools").Raw))
		hasPrefix = true
	}
	if !hasPrefix {
		return ""
	}
	return "wv_" + hex.EncodeToString(h.Sum(nil)[:20])
}

func (e *RequestEnvelope) buildOpenAIFromOpenAI(opts EmitOptions) ([]byte, error) {
	ov := resolveOpenAIOverrides(e.body, opts)
	body, err := e.emitSameFormat(ov)
	if err != nil {
		return nil, err
	}
	forced := resolveReasoningEffortFor(opts)
	if forced == "" {
		forced = alwaysOnFloorForDisabledEffort(body, opts)
	}
	if forced != "" && opts.Capabilities.Supports(router.CapReasoning) && reasoningEffortAcceptedOnChatCompletions(opts, hasNonEmptyTools(body)) {
		body, err = sjson.SetBytes(body, "reasoning_effort", forced)
		if err != nil {
			return nil, fmt.Errorf("set reasoning_effort: %w", err)
		}
	}
	if targetIsOpenRouter(opts) {
		if hint := openRouterProviderHint(opts.TargetModel); hint != nil {
			body, err = sjson.SetBytes(body, "provider", hint)
			if err != nil {
				return nil, fmt.Errorf("set openrouter provider hint: %w", err)
			}
		}
		if reasoning := openRouterReasoningHint(opts.TargetModel); reasoning != nil {
			body, err = sjson.SetBytes(body, "reasoning", reasoning)
			if err != nil {
				return nil, fmt.Errorf("set openrouter reasoning hint: %w", err)
			}
		}
		if reminder := openRouterSystemReminder(opts.TargetModel); reminder != "" && hasNonEmptyTools(body) {
			body, err = applySystemReminderToBody(body, reminder)
			if err != nil {
				return nil, fmt.Errorf("set system reminder: %w", err)
			}
		}
		if openRouterForcesToolTemperatureZero(opts.TargetModel) &&
			hasNonEmptyTools(body) &&
			!gjson.GetBytes(body, "temperature").Exists() {
			body, err = sjson.SetBytes(body, "temperature", 0)
			if err != nil {
				return nil, fmt.Errorf("set tool temperature override: %w", err)
			}
		}
	}
	body, err = applyQwen3SamplersIfNeeded(body, opts)
	if err != nil {
		return nil, err
	}
	body, err = applyGLM51FlagsIfNeeded(body, opts)
	if err != nil {
		return nil, err
	}
	body, err = applyGLM53FlashFlagsIfNeeded(body, opts)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func targetIsOpenRouter(opts EmitOptions) bool {
	if opts.TargetProvider != "" {
		return opts.TargetProvider == providers.ProviderOpenRouter
	}
	return true
}

func (e *RequestEnvelope) buildOpenAIFromAnthropic(opts EmitOptions) ([]byte, providers.RequestMutationStats, error) {
	var stats providers.RequestMutationStats
	body, ccFilter, err := filterClaudeCodeOnlyToolsFromAnthropicBody(e.body, opts.ccToolFilter())
	if err != nil {
		return nil, stats, fmt.Errorf("strip claude-code-only tools: %w", err)
	}
	stats.CCOnlyToolsStripped = ccFilter.ToolsRemoved
	stats.CCTaskRemindersStripped = ccFilter.TaskRemindersRemoved
	body, stats.ServerToolsStripped = websearch.StripServerTools(body)
	jw := newJSONWriter()
	jw.Obj()
	jw.Key("model")
	jw.Str(opts.TargetModel)

	if r := gjson.GetBytes(body, "stream"); r.Exists() {
		jw.Key("stream")
		jw.Raw(r.Raw)
	}

	writeOpenAISystemAndMessagesFromAnthropic(jw, body, opts)

	if r := gjson.GetBytes(body, "stop_sequences"); r.Exists() &&
		!opts.Capabilities.Supports(router.CapReasoning) {
		jw.Key("stop")
		jw.Raw(r.Raw)
	}

	writeOpenAIToolsFromAnthropic(jw, body)

	writeOpenAIToolChoiceFromAnthropic(jw, body)
	writeOpenAIParallelToolCallsFromAnthropic(jw, body)

	clientSetTemp := false
	sampleOK := samplersAccepted(opts)
	if r := gjson.GetBytes(body, "temperature"); r.Exists() && sampleOK {
		jw.Key("temperature")
		jw.Raw(r.Raw)
		clientSetTemp = true
	}
	if r := gjson.GetBytes(body, "top_p"); r.Exists() && sampleOK {
		jw.Key("top_p")
		jw.Raw(r.Raw)
	}

	if !clientSetTemp && targetIsOpenRouter(opts) && openRouterForcesToolTemperatureZero(opts.TargetModel) {
		if hasNonEmptyTools(body) {
			jw.Key("temperature")
			jw.Int(0)
		}
	}

	writeOpenAIMaxTokensFromAnthropic(jw, body, opts)

	if forced := resolveReasoningEffortFor(opts); forced != "" && opts.Capabilities.Supports(router.CapReasoning) && reasoningEffortAcceptedOnChatCompletions(opts, hasNonEmptyTools(body)) {
		jw.Key("reasoning_effort")
		jw.Str(forced)
	}

	if opts.IncludeStreamUsage && gjson.GetBytes(body, "stream").Bool() {
		jw.Key("stream_options")
		jw.Obj()
		jw.Key("include_usage")
		jw.Bool(true)
		jw.EndObj()
	}

	if targetIsOpenRouter(opts) {
		if hint := openRouterProviderHint(opts.TargetModel); hint != nil {
			if hintBytes, err := json.Marshal(hint); err == nil {
				jw.Key("provider")
				jw.RawBytes(hintBytes)
			}
		}
		if reasoning := openRouterReasoningHint(opts.TargetModel); reasoning != nil {
			if reasoningBytes, err := json.Marshal(reasoning); err == nil {
				jw.Key("reasoning")
				jw.RawBytes(reasoningBytes)
			}
		}
	}

	jw.EndObj()
	body, err = applyQwen3SamplersIfNeeded(jw.Bytes(), opts)
	if err != nil {
		return nil, stats, err
	}
	body, err = applyGLM51FlagsIfNeeded(body, opts)
	if err != nil {
		return nil, stats, err
	}
	body, err = applyGLM53FlashFlagsIfNeeded(body, opts)
	if err != nil {
		return nil, stats, err
	}
	return body, stats, nil
}

func applyGLM51FlagsIfNeeded(body []byte, opts EmitOptions) ([]byte, error) {
	if !isGLM51(opts.TargetModel) {
		return body, nil
	}
	if !gjson.GetBytes(body, "tool_stream").Exists() {
		out, err := sjson.SetBytes(body, "tool_stream", true)
		if err != nil {
			return nil, fmt.Errorf("set glm-5.1 tool_stream: %w", err)
		}
		body = out
	}
	if !targetIsOpenRouter(opts) && !gjson.GetBytes(body, "chat_template_kwargs.enable_thinking").Exists() {
		out, err := sjson.SetBytes(body, "chat_template_kwargs.enable_thinking", false)
		if err != nil {
			return nil, fmt.Errorf("set glm-5.1 chat_template_kwargs.enable_thinking: %w", err)
		}
		body = out
	}
	return body, nil
}

func applyGLM53FlashFlagsIfNeeded(body []byte, opts EmitOptions) ([]byte, error) {
	if !isGLM53Flash(opts.TargetModel) {
		return body, nil
	}
	if gjson.GetBytes(body, "tool_stream").Exists() {
		return body, nil
	}
	out, err := sjson.SetBytes(body, "tool_stream", true)
	if err != nil {
		return nil, fmt.Errorf("set glm-5.3-flash tool_stream: %w", err)
	}
	return out, nil
}

func applyQwen3SamplersIfNeeded(body []byte, opts EmitOptions) ([]byte, error) {
	if !isQwen3Family(opts.TargetModel) {
		return body, nil
	}
	type sampler struct {
		key string
		val float64
	}
	defaults := []sampler{
		{"temperature", qwen3Temperature},
		{"top_p", qwen3TopP},
		{"presence_penalty", qwen3PresencePenalty},
	}
	if opts.TargetProvider != providers.ProviderFireworks {
		defaults = append(defaults, sampler{"repetition_penalty", qwen3RepetitionPenalty})
	}
	for _, s := range defaults {
		if gjson.GetBytes(body, s.key).Exists() {
			continue
		}
		out, err := sjson.SetBytes(body, s.key, s.val)
		if err != nil {
			return nil, fmt.Errorf("set qwen3 %s: %w", s.key, err)
		}
		body = out
	}
	return body, nil
}

func writeOpenAISystemAndMessagesFromAnthropic(jw *jsonWriter, body []byte, opts EmitOptions) {
	systemText := flattenAnthropicSystemGJSON(gjson.GetBytes(body, "system"))
	if targetIsOpenRouter(opts) && hasNonEmptyTools(body) {
		if reminder := openRouterSystemReminder(opts.TargetModel); reminder != "" {
			if systemText == "" {
				systemText = reminder
			} else {
				systemText = systemText + "\n\n" + reminder
			}
		}
	}

	jw.Key("messages")
	jw.Arr()

	if systemText != "" {
		jw.Obj()
		jw.Key("role")
		jw.Str("system")
		jw.Key("content")
		jw.Str(systemText)
		jw.EndObj()
	}

	gjson.GetBytes(body, "messages").ForEach(func(_, msg gjson.Result) bool {
		role := msg.Get("role").String()
		switch role {
		case "assistant":
			writeOpenAIAssistantFromAnthropic(jw, msg)
		default:
			writeOpenAIUserFromAnthropic(jw, msg)
		}
		return true
	})

	jw.EndArr()
}

func flattenAnthropicSystemGJSON(system gjson.Result) string {
	switch system.Type {
	case gjson.String:
		return stripAnthropicBillingHeader(system.String())
	case gjson.JSON:
		if !system.IsArray() {
			return ""
		}
		var parts []string
		system.ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() == "text" {
				if text := block.Get("text").String(); text != "" {
					if stripped := stripAnthropicBillingHeader(text); stripped != "" {
						parts = append(parts, stripped)
					}
				}
			}
			return true
		})
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func writeOpenAIAssistantFromAnthropic(jw *jsonWriter, msg gjson.Result) {
	jw.Obj()
	jw.Key("role")
	jw.Str("assistant")

	content := msg.Get("content")
	switch content.Type {
	case gjson.String:
		jw.Key("content")
		jw.Raw(content.Raw)
	case gjson.JSON:
		if !content.IsArray() {
			jw.Key("content")
			jw.Null()
			jw.EndObj()
			return
		}
		var textParts []string
		var toolCallRaws []string

		content.ForEach(func(_, block gjson.Result) bool {
			switch block.Get("type").String() {
			case "text":
				if text := block.Get("text").String(); text != "" {
					textParts = append(textParts, text)
				}
			case "tool_use":
				toolCallRaws = append(toolCallRaws, buildOpenAIToolCall(block))
			}
			return true
		})

		switch {
		case len(textParts) > 0:
			jw.Key("content")
			jw.Str(strings.Join(textParts, "\n"))
		case len(toolCallRaws) > 0:
			jw.Key("content")
			jw.Null()
		default:
			jw.Key("content")
			jw.Str("")
		}
		if len(toolCallRaws) > 0 {
			jw.Key("tool_calls")
			jw.Arr()
			for _, raw := range toolCallRaws {
				jw.Raw(raw)
			}
			jw.EndArr()
		}
	default:
		jw.Key("content")
		jw.Null()
	}

	jw.EndObj()
}

func buildOpenAIToolCall(block gjson.Result) string {
	inner := newJSONWriter()
	inner.Obj()
	inner.Key("id")
	inner.Str(clampOpenAIToolCallID(block.Get("id").String()))
	inner.Key("type")
	inner.Str("function")
	inner.Key("function")
	inner.Obj()
	inner.Key("name")
	inner.Str(sanitizeResponsesToolAlias(block.Get("name").String()))
	inputRaw := block.Get("input").Raw
	if inputRaw == "" {
		inputRaw = "{}"
	}
	inner.Key("arguments")
	inner.Str(inputRaw)
	inner.EndObj()
	inner.EndObj()
	return string(inner.Bytes())
}

func writeOpenAIUserFromAnthropic(jw *jsonWriter, msg gjson.Result) {
	content := msg.Get("content")
	switch content.Type {
	case gjson.String:
		jw.Obj()
		jw.Key("role")
		jw.Str("user")
		jw.Key("content")
		jw.Raw(content.Raw)
		jw.EndObj()
		return
	case gjson.JSON:
		if !content.IsArray() {
			return
		}
	default:
		return
	}

	var toolResultRaws []string
	var userPartRaws []string

	content.ForEach(func(_, block gjson.Result) bool {
		switch block.Get("type").String() {
		case "tool_result":
			toolResultRaws = append(toolResultRaws, buildOpenAIToolResultMessage(block))
			block.Get("content").ForEach(func(_, inner gjson.Result) bool {
				if inner.Get("type").String() == "image" {
					if part := buildOpenAIImagePart(inner); part != "" {
						userPartRaws = append(userPartRaws, part)
					}
				}
				return true
			})
		case "text":
			inner := newJSONWriter()
			inner.Obj()
			inner.Key("type")
			inner.Str("text")
			inner.Key("text")
			inner.Str(block.Get("text").String())
			inner.EndObj()
			userPartRaws = append(userPartRaws, string(inner.Bytes()))
		case "image":
			if part := buildOpenAIImagePart(block); part != "" {
				userPartRaws = append(userPartRaws, part)
			}
		}
		return true
	})

	for _, raw := range toolResultRaws {
		jw.Raw(raw)
	}

	if len(userPartRaws) == 0 {
		return
	}

	if len(userPartRaws) == 1 {
		p := gjson.Parse(userPartRaws[0])
		if p.Get("type").String() == "text" {
			jw.Obj()
			jw.Key("role")
			jw.Str("user")
			jw.Key("content")
			jw.Str(p.Get("text").String())
			jw.EndObj()
			return
		}
	}

	jw.Obj()
	jw.Key("role")
	jw.Str("user")
	jw.Key("content")
	jw.Arr()
	for _, raw := range userPartRaws {
		jw.Raw(raw)
	}
	jw.EndArr()
	jw.EndObj()
}

func buildOpenAIToolResultMessage(block gjson.Result) string {
	inner := newJSONWriter()
	inner.Obj()
	inner.Key("role")
	inner.Str("tool")
	inner.Key("tool_call_id")
	inner.Str(clampOpenAIToolCallID(block.Get("tool_use_id").String()))
	inner.Key("content")
	inner.Str(toolResultContentGJSON(block.Get("content")))
	inner.EndObj()
	return string(inner.Bytes())
}

func buildOpenAIImagePart(block gjson.Result) string {
	src := block.Get("source")
	if !src.Exists() {
		return ""
	}
	inner := newJSONWriter()
	inner.Obj()
	inner.Key("type")
	inner.Str("image_url")
	inner.Key("image_url")
	inner.Obj()
	inner.Key("url")
	switch src.Get("type").String() {
	case "base64":
		mediaType := src.Get("media_type").String()
		if mediaType == "" {
			mediaType = "image/jpeg"
		}
		data := src.Get("data").String()
		if data == "" {
			return ""
		}
		inner.Str("data:" + mediaType + ";base64," + data)
	case "url":
		urlStr := src.Get("url").String()
		if urlStr == "" {
			return ""
		}
		inner.Str(urlStr)
	default:
		return ""
	}
	inner.EndObj()
	inner.EndObj()
	return string(inner.Bytes())
}

const openAIMaxTools = 128

func writeOpenAIToolsFromAnthropic(jw *jsonWriter, body []byte) {
	tools := gjson.GetBytes(body, "tools")
	if !tools.Exists() || !tools.IsArray() || tools.Get("#").Int() == 0 {
		return
	}

	jw.Key("tools")
	jw.Arr()
	count := 0
	tools.ForEach(func(_, tool gjson.Result) bool {
		if count >= openAIMaxTools {
			return false
		}
		count++
		var params any
		if schema := tool.Get("input_schema"); schema.Exists() {
			_ = json.Unmarshal([]byte(schema.Raw), &params)
			params = inlineSchemaDefs(params)
			sanitizeOpenAIToolSchema(params)
		}
		jw.Obj()
		jw.Key("type")
		jw.Str("function")
		jw.Key("function")
		jw.Obj()
		jw.Key("name")
		jw.Str(sanitizeResponsesToolAlias(tool.Get("name").String()))
		if desc := tool.Get("description"); desc.Exists() {
			jw.Key("description")
			jw.Raw(desc.Raw)
		}
		paramBytes := emptyOpenAIToolParameters
		if params != nil {
			if marshaled, err := json.Marshal(params); err == nil {
				paramBytes = marshaled
			}
		}
		jw.Key("parameters")
		jw.RawBytes(paramBytes)
		jw.EndObj()
		jw.EndObj()
		return true
	})
	jw.EndArr()
}

func writeOpenAIMaxTokensFromAnthropic(jw *jsonWriter, body []byte, opts EmitOptions) {
	r := gjson.GetBytes(body, "max_tokens")
	val := defaultOutputTokens(opts.TargetModel)
	if r.Exists() {
		val = r.Int()
	}
	reasoning := opts.Capabilities.Supports(router.CapReasoning)
	effort := resolveReasoningEffortFor(opts)
	if toolTurnNeedsExplicitEffortNone(opts, hasNonEmptyTools(body)) {
		effort = "none"
	}
	val = reasoningOutputFloor(val, reasoning && effort != "none")
	val = clampToModelOutputCap(val, opts.TargetModel)
	if reasoning {
		jw.Key("max_completion_tokens")
	} else {
		jw.Key("max_tokens")
	}
	jw.Int(val)
}

func inlineSchemaDefs(node any) any {
	root, ok := node.(map[string]any)
	if !ok {
		return node
	}
	defs := map[string]any{}
	for _, key := range []string{"$defs", "definitions"} {
		d, ok := root[key].(map[string]any)
		if !ok {
			continue
		}
		for name, v := range d {
			defs[key+"/"+name] = v
		}
	}
	if len(defs) == 0 {
		return node
	}
	resolved, _ := resolveSchemaRefs(node, defs, map[string]struct{}{}).(map[string]any)
	delete(resolved, "$defs")
	delete(resolved, "definitions")
	return resolved
}

func resolveSchemaRefs(node any, defs map[string]any, visited map[string]struct{}) any {
	switch v := node.(type) {
	case map[string]any:
		if ref, ok := v["$ref"].(string); ok {
			name := strings.TrimPrefix(ref, "#/")
			if _, cycle := visited[name]; cycle {
				return v
			}
			target, ok := defs[name]
			if !ok {
				return v
			}
			visited[name] = struct{}{}
			out := resolveSchemaRefs(deepCopyJSON(target), defs, visited)
			delete(visited, name)
			return out
		}
		out := make(map[string]any, len(v))
		for k, child := range v {
			out[k] = resolveSchemaRefs(child, defs, visited)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = resolveSchemaRefs(child, defs, visited)
		}
		return out
	default:
		return v
	}
}

func deepCopyJSON(node any) any {
	switch v := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, c := range v {
			out[k] = deepCopyJSON(c)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, c := range v {
			out[i] = deepCopyJSON(c)
		}
		return out
	default:
		return v
	}
}

var emptyOpenAIToolParameters = []byte(`{"type":"object","properties":{}}`)

func ensureOpenAIToolParameters(body []byte) ([]byte, error) {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body, nil
	}
	out := body
	var err error
	for i, tool := range tools.Array() {
		path, ok := openAIToolParametersPath(tool)
		if !ok {
			continue
		}
		schema := tool.Get(path)
		if schema.Exists() && schema.Type != gjson.Null {
			continue
		}
		out, err = sjson.SetRawBytes(out, fmt.Sprintf("tools.%d.%s", i, path), emptyOpenAIToolParameters)
		if err != nil {
			return nil, fmt.Errorf("set tools[%d].%s: %w", i, path, err)
		}
	}
	return out, nil
}

func openAIToolParametersPath(tool gjson.Result) (string, bool) {
	typ := tool.Get("type").String()
	if tool.Get("function").Exists() {
		if typ != "" && typ != "function" {
			return "", false
		}
		return "function.parameters", true
	}
	if typ == "function" || (typ == "" && tool.Get("name").Exists()) {
		return "parameters", true
	}
	return "", false
}

func sanitizeOpenAIToolSchema(node any) {
	m, ok := node.(map[string]any)
	if !ok {
		return
	}
	if pattern, hasPattern := m["pattern"]; hasPattern {
		appendSchemaConstraintDescription(m, "pattern", pattern)
		delete(m, "pattern")
	}
	if t, _ := m["type"].(string); t == "array" {
		if _, hasItems := m["items"]; !hasItems {
			m["items"] = map[string]any{}
		}
	}
	if props, ok := m["properties"].(map[string]any); ok {
		for _, v := range props {
			sanitizeOpenAIToolSchema(v)
		}
	}
	sanitizeOpenAIToolSchema(m["items"])
	sanitizeOpenAIToolSchema(m["additionalProperties"])
	sanitizeOpenAIToolSchema(m["propertyNames"])
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if arr, ok := m[key].([]any); ok {
			for _, v := range arr {
				sanitizeOpenAIToolSchema(v)
				if key == "anyOf" {
					addOpenAIAnyOfBranchType(v)
				}
			}
		}
	}
	sanitizeOpenAIToolSchema(m["not"])
}

func addOpenAIAnyOfBranchType(node any) {
	m, ok := node.(map[string]any)
	if !ok {
		return
	}
	if _, hasType := m["type"]; hasType {
		return
	}
	m["type"] = []any{"string", "number", "boolean", "object", "array", "null"}
}

func appendSchemaConstraintDescription(node map[string]any, key string, value any) {
	raw := compactJSONValue(value)
	fingerprint := sha256.Sum256([]byte(key + "|" + raw))
	note := fmt.Sprintf("(%s constraint %s)", key, hex.EncodeToString(fingerprint[:4]))
	if desc, _ := node["description"].(string); desc != "" {
		node["description"] = desc + " " + note
		return
	}
	node["description"] = note
}

func alwaysOnFloorForDisabledEffort(body []byte, opts EmitOptions) string {
	caps := opts.Capabilities.Reasoning()
	if !caps.AlwaysOn || len(caps.Levels) == 0 || ParseReasoningIntent(FormatOpenAI, body).Kind != ReasoningDisabled {
		return ""
	}
	return caps.Levels[0]
}
