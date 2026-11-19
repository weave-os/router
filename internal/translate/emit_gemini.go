package translate

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/websearch"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	ErrGeminiSchemaIncompatible      = errors.New("gemini schema is not representable")
	ErrGeminiToolDeclarationConflict = errors.New("gemini function declarations conflict")
	ErrGeminiUnsignedToolHistory     = errors.New("gemini tool history lacks thought signatures")
)

func (e *RequestEnvelope) PrepareGemini(_ http.Header, opts EmitOptions) (providers.PreparedRequest, error) {
	if usesGemini3Contract(opts.TargetModel) && e.HasUnsignedToolCallHistory() {
		return providers.PreparedRequest{}, fmt.Errorf("%w: Gemini 3.x requires a thoughtSignature on every historical function call", ErrGeminiUnsignedToolHistory)
	}
	e, err := e.withWorkspaceSystemAppended(opts)
	if err != nil {
		return providers.PreparedRequest{}, err
	}
	e, err = e.withUnambiguousReadPrefixes()
	if err != nil {
		return providers.PreparedRequest{}, err
	}
	if e.format == FormatGemini {
		body := e.body
		body, err = sjson.DeleteBytes(body, "model")
		if err != nil {
			return providers.PreparedRequest{}, fmt.Errorf("strip model field: %w", err)
		}
		body, err = sjson.DeleteBytes(body, "stream")
		if err != nil {
			return providers.PreparedRequest{}, fmt.Errorf("strip stream field: %w", err)
		}
		body, err = sanitizeGeminiTools(body)
		if err != nil {
			return providers.PreparedRequest{}, fmt.Errorf("sanitize gemini tools: %w", err)
		}
		if forced := resolveReasoningEffortFor(opts); forced != "" {
			if raw := thinkingConfigRaw(forced, opts.TargetModel); raw != "" {
				body, err = sjson.SetRawBytes(body, "generationConfig.thinkingConfig", []byte(raw))
				if err != nil {
					return providers.PreparedRequest{}, fmt.Errorf("set forced thinkingConfig: %w", err)
				}
			}
		}
		headers := make(http.Header)
		if e.Stream() {
			headers.Set(GeminiStreamHintHeader, "true")
		}
		return providers.PreparedRequest{Body: body, Headers: headers}, nil
	}

	var stats providers.RequestMutationStats
	jw := newJSONWriter()
	jw.Obj()
	switch e.format {
	case FormatOpenAI:
		stats.GeminiValidatedToolMode = !opts.DowngradeGeminiValidatedToAuto &&
			geminiEmitsValidatedToolMode(e.body, opts.TargetModel, FormatOpenAI)
		if err := writeGeminiFromOpenAI(jw, e.body, opts); err != nil {
			return providers.PreparedRequest{}, err
		}
	case FormatAnthropic:
		filtered, ccFilter, err := filterClaudeCodeOnlyToolsFromAnthropicBody(e.body, opts.ccToolFilter())
		if err != nil {
			return providers.PreparedRequest{}, fmt.Errorf("strip claude-code-only tools: %w", err)
		}
		stats.CCOnlyToolsStripped = ccFilter.ToolsRemoved
		stats.CCTaskRemindersStripped = ccFilter.TaskRemindersRemoved
		filtered, stats.ServerToolsStripped = websearch.StripServerTools(filtered)
		if reminder := geminiSystemReminder(opts.TargetModel); reminder != "" && hasNonEmptyTools(filtered) {
			stats.GeminiReminderInjected = true
		}
		stats.GeminiValidatedToolMode = !opts.DowngradeGeminiValidatedToAuto &&
			geminiEmitsValidatedToolMode(filtered, opts.TargetModel, FormatAnthropic)
		if err := writeGeminiFromAnthropic(jw, filtered, opts); err != nil {
			return providers.PreparedRequest{}, err
		}
	default:
		return providers.PreparedRequest{}, fmt.Errorf("unsupported source format for Gemini emit: %d", e.format)
	}
	jw.EndObj()
	body := jw.Bytes()

	headers := make(http.Header)
	if e.Stream() {
		headers.Set(GeminiStreamHintHeader, "true")
	}
	return providers.PreparedRequest{Body: body, Headers: headers, Stats: stats}, nil
}

const GeminiStreamHintHeader = "X-Router-Gemini-Stream"

func sanitizeGeminiTools(body []byte) ([]byte, error) {
	if !gjson.GetBytes(body, "tools").Exists() {
		return body, nil
	}
	tools, err := sanitizeGeminiToolsRaw(gjson.GetBytes(body, "tools").Value())
	if err != nil {
		return nil, err
	}
	body, err = sjson.SetBytes(body, "tools", tools)
	if err != nil {
		return nil, fmt.Errorf("set sanitized tools: %w", err)
	}
	if allowed := gjson.GetBytes(body, "toolConfig.functionCallingConfig.allowedFunctionNames"); allowed.Exists() {
		declared := geminiDeclarationNames(tools)
		for _, name := range allowed.Array() {
			if _, ok := declared[name.String()]; !ok {
				return nil, fmt.Errorf("%w: allowed function %q has no declaration", ErrGeminiToolDeclarationConflict, name.String())
			}
		}
	}
	return body, nil
}

func sanitizeGeminiToolsRaw(v any) (any, error) {
	tools, ok := v.([]any)
	if !ok {
		return v, nil
	}
	out := make([]any, 0, len(tools))
	seen := make(map[string]any)
	for _, t := range tools {
		tool, _ := t.(map[string]any)
		if tool == nil {
			out = append(out, t)
			continue
		}
		fds, _ := tool["functionDeclarations"].([]any)
		if len(fds) == 0 {
			out = append(out, t)
			continue
		}
		sanitized := make([]any, 0, len(fds))
		for _, fd := range fds {
			fdMap, _ := fd.(map[string]any)
			if fdMap == nil {
				sanitized = append(sanitized, fd)
				continue
			}
			name, _ := fdMap["name"].(string)
			if name == "" {
				return nil, fmt.Errorf("%w: declaration has no name", ErrGeminiToolDeclarationConflict)
			}
			if params, ok := fdMap["parameters"]; ok && params != nil {
				fdMap = copyMap(fdMap)
				var err error
				fdMap["parameters"], err = sanitizeSchemaForGemini(params)
				if err != nil {
					return nil, fmt.Errorf("function %q parameters: %w", name, err)
				}
			}
			if previous, exists := seen[name]; exists {
				if !semanticJSONEqual(previous, fdMap) {
					return nil, fmt.Errorf("%w: duplicate declaration %q differs", ErrGeminiToolDeclarationConflict, name)
				}
				continue
			}
			seen[name] = fdMap
			sanitized = append(sanitized, fdMap)
		}
		if len(sanitized) == 0 {
			continue
		}
		toolCopy := copyMap(tool)
		toolCopy["functionDeclarations"] = sanitized
		out = append(out, toolCopy)
	}
	return out, nil
}

func geminiDeclarationNames(v any) map[string]struct{} {
	names := make(map[string]struct{})
	tools, _ := v.([]any)
	for _, t := range tools {
		tool, _ := t.(map[string]any)
		declarations, _ := tool["functionDeclarations"].([]any)
		for _, declaration := range declarations {
			if d, ok := declaration.(map[string]any); ok {
				if name, _ := d["name"].(string); name != "" {
					names[name] = struct{}{}
				}
			}
		}
	}
	return names
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func writeGeminiFromOpenAI(jw *jsonWriter, body []byte, opts EmitOptions) error {
	msgs := gjson.GetBytes(body, "messages")

	toolNames := make(map[string]string)
	msgs.ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() != "assistant" {
			return true
		}
		msg.Get("tool_calls").ForEach(func(_, tc gjson.Result) bool {
			if id := tc.Get("id").String(); id != "" {
				toolNames[id] = tc.Get("function.name").String()
			}
			return true
		})
		return true
	})
	sysParts := openAISystemTextParts(msgs)
	if reminder := geminiSystemReminder(opts.TargetModel); reminder != "" && hasNonEmptyTools(body) {
		sysParts = append(sysParts, reminder)
	}
	if len(sysParts) > 0 {
		jw.Key("systemInstruction")
		jw.Obj()
		jw.Key("parts")
		jw.Arr()
		jw.Obj()
		jw.Key("text")
		jw.Str(strings.Join(sysParts, "\n"))
		jw.EndObj()
		jw.EndArr()
		jw.EndObj()
	}

	entries := make([]contentEntry, 0, 8)
	var walkErr error
	msgs.ForEach(func(_, msg gjson.Result) bool {
		role := msg.Get("role").String()
		switch role {
		case "system":
			return true
		case "user":
			parts := openAIUserPartsGJSON(msg.Get("content"))
			if len(parts) == 0 {
				return true
			}
			entries = append(entries, contentEntry{role: "user", parts: parts})
		case "assistant":
			parts, parseErr := openAIAssistantPartsGJSON(msg)
			if parseErr != nil {
				walkErr = parseErr
				return false
			}
			if len(parts) == 0 {
				return true
			}
			entries = append(entries, contentEntry{role: "model", parts: parts})
		case "tool":
			tcID := msg.Get("tool_call_id").String()
			name := toolNames[tcID]
			if name == "" {
				return true
			}
			result := toolResultContentGJSON(msg.Get("content"))
			entries = append(entries, contentEntry{
				role:  "user",
				parts: []string{geminiFunctionResponsePart(name, result)},
			})
		}
		return true
	})
	if walkErr != nil {
		return walkErr
	}
	emitGeminiContents(jw, collapseConsecutiveRoles(entries))

	if err := writeGeminiToolsFromOpenAI(jw, body); err != nil {
		return err
	}
	writeGeminiToolChoiceFromOpenAI(jw, body, opts.TargetModel, opts.DowngradeGeminiValidatedToAuto)
	if err := writeGeminiGenerationConfigFromOpenAI(jw, body, opts); err != nil {
		return err
	}
	return nil
}

func openAIUserPartsGJSON(content gjson.Result) []string {
	switch content.Type {
	case gjson.String:
		s := content.String()
		if s == "" {
			return nil
		}
		return []string{geminiTextPart(s)}
	case gjson.JSON:
		var parts []string
		content.ForEach(func(_, p gjson.Result) bool {
			switch p.Get("type").String() {
			case "text":
				if s := p.Get("text").String(); s != "" {
					parts = append(parts, geminiTextPart(s))
				}
			case "image_url":
				urlStr := p.Get("image_url.url").String()
				mime, data, ok := parseDataURL(urlStr)
				if !ok {
					return true
				}
				if _, err := base64.StdEncoding.DecodeString(data); err != nil {
					return true
				}
				if mime == "" {
					mime = "image/jpeg"
				}
				pw := newJSONWriter()
				pw.Obj()
				pw.Key("inlineData")
				pw.Obj()
				pw.Key("mimeType")
				pw.Str(mime)
				pw.Key("data")
				pw.Str(data)
				pw.EndObj()
				pw.EndObj()
				parts = append(parts, string(pw.Bytes()))
			}
			return true
		})
		return parts
	default:
	}
	return nil
}

func openAIAssistantPartsGJSON(msg gjson.Result) ([]string, error) {
	var parts []string

	content := msg.Get("content")
	if text := openAIContentTextGJSON(content); text != "" {
		parts = append(parts, geminiTextPart(text))
	}

	var inheritedSig string
	if sig := msg.Get("thought_signature").String(); sig != "" {
		inheritedSig = sig
	}
	if inheritedSig == "" {
		msg.Get("tool_calls").ForEach(func(_, tc gjson.Result) bool {
			if sig := extractThoughtSignature(tc); sig != "" {
				inheritedSig = sig
				return false
			}
			return true
		})
	}

	var parseErr error
	msg.Get("tool_calls").ForEach(func(_, tc gjson.Result) bool {
		name := tc.Get("function.name").String()
		argsStr := tc.Get("function.arguments").String()
		if argsStr != "" && !gjson.Valid(argsStr) {
			parseErr = fmt.Errorf("parse tool_call arguments: invalid JSON")
			return false
		}

		pw := newJSONWriter()
		pw.Obj()
		pw.Key("functionCall")
		pw.Obj()
		pw.Key("name")
		pw.Str(name)
		pw.Key("args")
		if argsStr != "" {
			pw.Raw(argsStr)
		} else {
			pw.Raw("{}")
		}
		pw.EndObj()
		sig := extractThoughtSignature(tc)
		if sig == "" {
			sig = inheritedSig
		}
		if sig != "" {
			pw.Key("thoughtSignature")
			pw.Str(sig)
		}
		pw.EndObj()
		parts = append(parts, string(pw.Bytes()))
		return true
	})

	return parts, parseErr
}

func geminiTextPart(text string) string {
	jw := newJSONWriter()
	jw.Obj()
	jw.Key("text")
	jw.Str(text)
	jw.EndObj()
	return string(jw.Bytes())
}

func writeGeminiToolsFromOpenAI(jw *jsonWriter, body []byte) error {
	tools := gjson.GetBytes(body, "tools")
	if !tools.Exists() || !tools.IsArray() || tools.Get("#").Int() == 0 {
		return nil
	}

	declarations := make([]any, 0, int(tools.Get("#").Int()))
	var emitErr error
	tools.ForEach(func(_, t gjson.Result) bool {
		fn := t.Get("function")
		if !fn.Exists() {
			return true
		}
		name := fn.Get("name").String()
		if name == "" {
			return true
		}
		declaration := map[string]any{"name": name}
		if desc := fn.Get("description"); desc.Exists() {
			declaration["description"] = desc.String()
		}
		if params := fn.Get("parameters"); params.Exists() {
			var schema any
			if err := json.Unmarshal([]byte(params.Raw), &schema); err != nil {
				emitErr = fmt.Errorf("function %q parameters: invalid JSON: %w", name, err)
				return false
			}
			clean, err := sanitizeSchemaForGemini(schema)
			if err != nil {
				observability.Get().Warn("Gemini tool schema is unrepresentable — emitting declaration without parameters", "tool_name", name, "err", err)
			} else {
				declaration["parameters"] = clean
			}
		}
		declarations = append(declarations, declaration)
		return true
	})
	if emitErr != nil {
		return emitErr
	}
	declarations, err := dedupeGeminiDeclarations(declarations)
	if err != nil {
		return err
	}
	if len(declarations) == 0 {
		return nil
	}
	jw.Key("tools")
	jw.Arr()
	jw.Obj()
	jw.Key("functionDeclarations")
	jw.Arr()
	for _, declaration := range declarations {
		encoded, err := json.Marshal(declaration)
		if err != nil {
			return fmt.Errorf("marshal function declaration: %w", err)
		}
		jw.RawBytes(encoded)
	}
	jw.EndArr()
	jw.EndObj()
	jw.EndArr()
	return nil
}

func writeGeminiToolChoiceFromOpenAI(jw *jsonWriter, body []byte, model string, downgradeValidated bool) {
	kind, name := openAIToolChoice(body)
	writeGeminiToolChoice(jw, body, model, downgradeValidated, kind, name)
}

func writeGeminiGenerationConfigFromOpenAI(jw *jsonWriter, body []byte, opts EmitOptions) error {
	model := opts.TargetModel
	type field struct {
		key string
		raw string
	}
	var fields []field

	intent, err := applyGeminiReasoning(ParseReasoningIntent(FormatOpenAI, body), opts)
	if err != nil {
		return err
	}
	thinking := geminiThinkingActive(intent, opts)

	if r := gjson.GetBytes(body, "temperature"); r.Exists() && r.Type == gjson.Number {
		fw := newJSONWriter()
		fw.Float(r.Num)
		fields = append(fields, field{"temperature", string(fw.Bytes())})
	}
	if r := gjson.GetBytes(body, "top_p"); r.Exists() && r.Type == gjson.Number {
		fw := newJSONWriter()
		fw.Float(r.Num)
		fields = append(fields, field{"topP", string(fw.Bytes())})
	}
	if r := gjson.GetBytes(body, "max_completion_tokens"); r.Exists() && r.Type == gjson.Number {
		fields = append(fields, field{"maxOutputTokens", geminiMaxOutputTokensRaw(int64(r.Num), thinking, model)})
	} else if r := gjson.GetBytes(body, "max_tokens"); r.Exists() && r.Type == gjson.Number {
		fields = append(fields, field{"maxOutputTokens", geminiMaxOutputTokensRaw(int64(r.Num), thinking, model)})
	}
	if r := gjson.GetBytes(body, "stop"); r.Exists() {
		if raw := stopToArrayRaw(r); raw != "" {
			fields = append(fields, field{"stopSequences", raw})
		}
	}
	if r := gjson.GetBytes(body, "response_format"); r.Exists() && r.IsObject() {
		if r.Get("type").String() == "json_object" {
			fields = append(fields, field{"responseMimeType", `"application/json"`})
		}
	}
	if raw, err := geminiThinkingConfigRaw(intent, model); err != nil {
		return err
	} else if raw != "" {
		fields = append(fields, field{"thinkingConfig", raw})
	}

	if len(fields) == 0 {
		return nil
	}
	jw.Key("generationConfig")
	jw.Obj()
	for _, f := range fields {
		jw.Key(f.key)
		jw.Raw(f.raw)
	}
	jw.EndObj()
	return nil
}

func writeGeminiFromAnthropic(jw *jsonWriter, body []byte, opts EmitOptions) error {
	system := gjson.GetBytes(body, "system")
	var sysParts []string
	switch system.Type {
	case gjson.String:
		if stripped := stripAnthropicBillingHeader(system.String()); stripped != "" {
			sysParts = append(sysParts, stripped)
		}
	case gjson.JSON:
		system.ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() == "text" {
				if text := block.Get("text").String(); text != "" {
					if stripped := stripAnthropicBillingHeader(text); stripped != "" {
						sysParts = append(sysParts, stripped)
					}
				}
			}
			return true
		})
	default:
	}
	if reminder := geminiSystemReminder(opts.TargetModel); reminder != "" && hasNonEmptyTools(body) {
		sysParts = append(sysParts, reminder)
	}
	if len(sysParts) > 0 {
		jw.Key("systemInstruction")
		jw.Obj()
		jw.Key("parts")
		jw.Arr()
		jw.Obj()
		jw.Key("text")
		jw.Str(strings.Join(sysParts, "\n"))
		jw.EndObj()
		jw.EndArr()
		jw.EndObj()
	}

	msgs := gjson.GetBytes(body, "messages")

	toolNames := make(map[string]string)
	msgs.ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() != "assistant" {
			return true
		}
		msg.Get("content").ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() == "tool_use" {
				if id := block.Get("id").String(); id != "" {
					toolNames[id] = block.Get("name").String()
				}
			}
			return true
		})
		return true
	})
	entries := make([]contentEntry, 0, 8)
	msgs.ForEach(func(_, msg gjson.Result) bool {
		role := msg.Get("role").String()
		switch role {
		case "user":
			parts := anthropicUserPartsGJSON(msg.Get("content"), toolNames)
			if len(parts) == 0 {
				return true
			}
			entries = append(entries, contentEntry{role: "user", parts: parts})
		case "assistant":
			parts := anthropicAssistantPartsGJSON(msg.Get("content"))
			if len(parts) == 0 {
				return true
			}
			entries = append(entries, contentEntry{role: "model", parts: parts})
		}
		return true
	})
	emitGeminiContents(jw, collapseConsecutiveRoles(entries))

	if err := writeGeminiToolsFromAnthropic(jw, body); err != nil {
		return err
	}
	writeGeminiToolChoiceFromAnthropic(jw, body, opts.TargetModel, opts.DowngradeGeminiValidatedToAuto)
	if err := writeGeminiGenerationConfigFromAnthropic(jw, body, opts); err != nil {
		return err
	}
	return nil
}

type contentEntry struct {
	role  string
	parts []string
}

func collapseConsecutiveRoles(in []contentEntry) []contentEntry {
	if len(in) == 0 {
		return in
	}
	out := make([]contentEntry, 0, len(in))
	for _, e := range in {
		if len(out) == 0 || out[len(out)-1].role != e.role {
			out = append(out, e)
			continue
		}
		out[len(out)-1].parts = append(out[len(out)-1].parts, e.parts...)
	}
	return out
}

func emitGeminiContents(jw *jsonWriter, entries []contentEntry) {
	if len(entries) == 0 {
		return
	}
	jw.Key("contents")
	jw.Arr()
	for _, e := range entries {
		jw.Obj()
		jw.Key("role")
		jw.Str(e.role)
		jw.Key("parts")
		jw.Arr()
		for _, p := range e.parts {
			jw.Raw(p)
		}
		jw.EndArr()
		jw.EndObj()
	}
	jw.EndArr()
}

func geminiFunctionResponsePart(name, result string) string {
	pw := newJSONWriter()
	pw.Obj()
	pw.Key("functionResponse")
	pw.Obj()
	pw.Key("name")
	pw.Str(name)
	pw.Key("response")
	pw.Obj()
	pw.Key("result")
	pw.Str(result)
	pw.EndObj()
	pw.EndObj()
	pw.EndObj()
	return string(pw.Bytes())
}

func usesGemini3Contract(model string) bool {
	return strings.HasPrefix(model, "gemini-3") || model == router.ModelGemini4Argon || model == router.ModelGemini4Neon
}

func geminiToolMode(downgradeValidated bool) string {
	if downgradeValidated {
		return "AUTO"
	}
	return "VALIDATED"
}

func geminiEmitsValidatedToolMode(body []byte, model string, format Format) bool {
	if !hasNonEmptyTools(body) || !usesGemini3Contract(model) {
		return false
	}
	var kind toolChoiceKind
	switch format {
	case FormatAnthropic:
		kind, _ = anthropicToolChoice(body)
	case FormatOpenAI:
		kind, _ = openAIToolChoice(body)
	default:
		return false
	}
	return kind == toolChoiceAbsent || kind == toolChoiceAuto
}

func anthropicUserPartsGJSON(content gjson.Result, toolNames map[string]string) []string {
	switch content.Type {
	case gjson.String:
		s := content.String()
		if s == "" {
			return nil
		}
		return []string{geminiTextPart(s)}
	case gjson.JSON:
		var parts []string
		content.ForEach(func(_, block gjson.Result) bool {
			switch block.Get("type").String() {
			case "text":
				if text := block.Get("text").String(); text != "" {
					parts = append(parts, geminiTextPart(text))
				}
			case "image":
				if block.Get("source.type").String() != "base64" {
					return true
				}
				data := block.Get("source.data").String()
				if data == "" {
					return true
				}
				mime := block.Get("source.media_type").String()
				if mime == "" {
					mime = "image/jpeg"
				}
				pw := newJSONWriter()
				pw.Obj()
				pw.Key("inlineData")
				pw.Obj()
				pw.Key("mimeType")
				pw.Str(mime)
				pw.Key("data")
				pw.Str(data)
				pw.EndObj()
				pw.EndObj()
				parts = append(parts, string(pw.Bytes()))
			case "tool_result":
				id := block.Get("tool_use_id").String()
				name := toolNames[id]
				if name == "" {
					return true
				}
				result := toolResultContentGJSON(block.Get("content"))
				pw := newJSONWriter()
				pw.Obj()
				pw.Key("functionResponse")
				pw.Obj()
				pw.Key("name")
				pw.Str(name)
				pw.Key("response")
				pw.Obj()
				pw.Key("result")
				pw.Str(result)
				pw.EndObj()
				pw.EndObj()
				pw.EndObj()
				parts = append(parts, string(pw.Bytes()))
			}
			return true
		})
		return parts
	default:
	}
	return nil
}

func anthropicAssistantPartsGJSON(content gjson.Result) []string {
	switch content.Type {
	case gjson.String:
		s := content.String()
		if s == "" {
			return nil
		}
		return []string{geminiTextPart(s)}
	case gjson.JSON:
		var inheritedSig string
		content.ForEach(func(_, block gjson.Result) bool {
			if sig := extractThoughtSignature(block); sig != "" {
				inheritedSig = sig
				return false
			}
			return true
		})
		var parts []string
		content.ForEach(func(_, block gjson.Result) bool {
			switch block.Get("type").String() {
			case "text":
				text := block.Get("text").String()
				if text == "" {
					return true
				}
				pw := newJSONWriter()
				pw.Obj()
				pw.Key("text")
				pw.Str(text)
				if sig := block.Get("thought_signature").String(); sig != "" {
					pw.Key("thoughtSignature")
					pw.Str(sig)
				}
				pw.EndObj()
				parts = append(parts, string(pw.Bytes()))
			case "tool_use":
				name := block.Get("name").String()
				inputRaw := block.Get("input").Raw
				if inputRaw == "" || inputRaw == "null" {
					inputRaw = "{}"
				}
				pw := newJSONWriter()
				pw.Obj()
				pw.Key("functionCall")
				pw.Obj()
				pw.Key("name")
				pw.Str(name)
				pw.Key("args")
				pw.Raw(inputRaw)
				pw.EndObj()
				sig := extractThoughtSignature(block)
				if sig == "" {
					sig = inheritedSig
				}
				if sig != "" {
					pw.Key("thoughtSignature")
					pw.Str(sig)
				}
				pw.EndObj()
				parts = append(parts, string(pw.Bytes()))
			}
			return true
		})
		return parts
	default:
	}
	return nil
}

func writeGeminiToolsFromAnthropic(jw *jsonWriter, body []byte) error {
	tools := gjson.GetBytes(body, "tools")
	if !tools.Exists() || !tools.IsArray() || tools.Get("#").Int() == 0 {
		return nil
	}

	declarations := make([]any, 0, int(tools.Get("#").Int()))
	var emitErr error
	tools.ForEach(func(_, t gjson.Result) bool {
		name := t.Get("name").String()
		if name == "" {
			return true
		}
		declaration := map[string]any{"name": name}
		if desc := t.Get("description"); desc.Exists() {
			declaration["description"] = desc.String()
		}
		if params := t.Get("input_schema"); params.Exists() {
			var schema any
			if err := json.Unmarshal([]byte(params.Raw), &schema); err != nil {
				emitErr = fmt.Errorf("function %q input_schema: invalid JSON: %w", name, err)
				return false
			}
			clean, err := sanitizeSchemaForGemini(schema)
			if err != nil {
				observability.Get().Warn("Gemini tool schema is unrepresentable — emitting declaration without parameters", "tool_name", name, "err", err)
			} else {
				declaration["parameters"] = clean
			}
		}
		declarations = append(declarations, declaration)
		return true
	})
	if emitErr != nil {
		return emitErr
	}
	var err error
	declarations, err = dedupeGeminiDeclarations(declarations)
	if err != nil {
		return err
	}
	if len(declarations) == 0 {
		return nil
	}
	jw.Key("tools")
	jw.Arr()
	jw.Obj()
	jw.Key("functionDeclarations")
	jw.Arr()
	for _, declaration := range declarations {
		encoded, err := json.Marshal(declaration)
		if err != nil {
			return fmt.Errorf("marshal function declaration: %w", err)
		}
		jw.RawBytes(encoded)
	}
	jw.EndArr()
	jw.EndObj()
	jw.EndArr()
	return nil
}

func writeGeminiToolChoiceFromAnthropic(jw *jsonWriter, body []byte, model string, downgradeValidated bool) {
	kind, name := anthropicToolChoice(body)
	writeGeminiToolChoice(jw, body, model, downgradeValidated, kind, name)
}

func writeGeminiToolChoice(jw *jsonWriter, body []byte, model string, downgradeValidated bool, kind toolChoiceKind, name string) {
	if (kind == toolChoiceAbsent || kind == toolChoiceAuto) && hasNonEmptyTools(body) && usesGemini3Contract(model) {
		writeGeminiFunctionCallingMode(jw, geminiToolMode(downgradeValidated), "")
		return
	}
	switch kind {
	case toolChoiceAuto:
		writeGeminiFunctionCallingMode(jw, "AUTO", "")
	case toolChoiceRequired:
		writeGeminiFunctionCallingMode(jw, "ANY", "")
	case toolChoiceNone:
		writeGeminiFunctionCallingMode(jw, "NONE", "")
	case toolChoiceNamed:
		writeGeminiFunctionCallingMode(jw, "ANY", name)
	case toolChoiceAbsent, toolChoiceUnrecognized:
	}
}

func writeGeminiFunctionCallingMode(jw *jsonWriter, mode, name string) {
	jw.Key("toolConfig")
	jw.Obj()
	jw.Key("functionCallingConfig")
	jw.Obj()
	jw.Key("mode")
	jw.Str(mode)
	if name != "" {
		jw.Key("allowedFunctionNames")
		jw.Arr()
		jw.Str(name)
		jw.EndArr()
	}
	jw.EndObj()
	jw.EndObj()
}

func writeGeminiGenerationConfigFromAnthropic(jw *jsonWriter, body []byte, opts EmitOptions) error {
	model := opts.TargetModel
	type field struct {
		key string
		raw string
	}
	var fields []field

	intent, err := applyGeminiReasoning(ParseReasoningIntent(FormatAnthropic, body), opts)
	if err != nil {
		return err
	}
	thinking := geminiThinkingActive(intent, opts)

	if r := gjson.GetBytes(body, "temperature"); r.Exists() && r.Type == gjson.Number {
		fw := newJSONWriter()
		fw.Float(r.Num)
		fields = append(fields, field{"temperature", string(fw.Bytes())})
	}
	if r := gjson.GetBytes(body, "top_p"); r.Exists() && r.Type == gjson.Number {
		fw := newJSONWriter()
		fw.Float(r.Num)
		fields = append(fields, field{"topP", string(fw.Bytes())})
	}
	if r := gjson.GetBytes(body, "max_tokens"); r.Exists() && r.Type == gjson.Number {
		fields = append(fields, field{"maxOutputTokens", geminiMaxOutputTokensRaw(int64(r.Num), thinking, model)})
	}
	if r := gjson.GetBytes(body, "stop_sequences"); r.Exists() {
		if raw := stopToArrayRaw(r); raw != "" {
			fields = append(fields, field{"stopSequences", raw})
		}
	}
	if raw, err := geminiThinkingConfigRaw(intent, model); err != nil {
		return err
	} else if raw != "" {
		fields = append(fields, field{"thinkingConfig", raw})
	}

	if len(fields) == 0 {
		return nil
	}
	jw.Key("generationConfig")
	jw.Obj()
	for _, f := range fields {
		jw.Key(f.key)
		jw.Raw(f.raw)
	}
	jw.EndObj()
	return nil
}

func geminiMaxOutputTokensRaw(want int64, thinking bool, model string) string {
	fw := newJSONWriter()
	fw.Int(clampToModelOutputCap(reasoningOutputFloor(want, thinking), model))
	return string(fw.Bytes())
}

func geminiThinkingActive(intent ReasoningIntent, opts EmitOptions) bool {
	caps := opts.Capabilities
	if len(caps.Reasoning().Levels) == 0 {
		caps = router.Lookup(opts.TargetModel)
	}
	if len(caps.Reasoning().Levels) == 0 {
		return false
	}
	switch intent.Kind {
	case ReasoningDisabled:
		return usesGemini3Contract(opts.TargetModel)
	case ReasoningBudget:
		return intent.BudgetTokens > 0
	case ReasoningLevel:
		return intent.Level != "none"
	case ReasoningAuto:
	}
	return true
}

func clampToModelOutputCap(v int64, model string) int64 {
	outputCap := modelMaxOutputTokens[model]
	if outputCap == 0 {
		outputCap = defaultMaxOutputTokenCap
	}
	if v > int64(outputCap) {
		return int64(outputCap)
	}
	return v
}

func stopToArrayRaw(r gjson.Result) string {
	var items []string
	if r.IsArray() {
		r.ForEach(func(_, v gjson.Result) bool {
			if s := v.String(); s != "" {
				sw := newJSONWriter()
				sw.Str(s)
				items = append(items, string(sw.Bytes()))
			}
			return true
		})
	} else if r.Type == gjson.String {
		if s := r.String(); s != "" {
			sw := newJSONWriter()
			sw.Str(s)
			items = append(items, string(sw.Bytes()))
		}
	}
	if len(items) == 0 {
		return ""
	}
	return "[" + strings.Join(items, ",") + "]"
}

func applyGeminiReasoning(intent ReasoningIntent, opts EmitOptions) (ReasoningIntent, error) {
	if usesGemini3Contract(opts.TargetModel) && intent.Kind == ReasoningBudget {
		intent.Kind = ReasoningLevel
		intent.Level = effortForBudget(intent.BudgetTokens)
		intent.NormalizationNotes = append(
			intent.NormalizationNotes,
			"Gemini 3.x normalized numeric thinking budget to "+intent.Level+" thinking level",
		)
	}
	caps := opts.Capabilities
	if len(caps.Reasoning().Levels) == 0 {
		caps = router.Lookup(opts.TargetModel)
	}
	return ApplyReasoningIntent(intent, caps, opts.ForceReasoningEffort)
}

func geminiThinkingConfigRaw(intent ReasoningIntent, model string) (string, error) {
	if intent.Kind == "" || intent.Kind == ReasoningAuto {
		return "", nil
	}
	if intent.Kind == ReasoningDisabled {
		if usesGemini3Contract(model) {
			return "", nil
		}
		return thinkingConfigRaw("none", model), nil
	}
	if intent.Kind == ReasoningBudget {
		if usesGemini3Contract(model) {
			return "", fmt.Errorf("%w: Gemini 3.x does not support thinking budgets", ErrReasoningIncompatible)
		}
		fw := newJSONWriter()
		fw.Obj()
		fw.Key("thinkingBudget")
		fw.Int(intent.BudgetTokens)
		fw.EndObj()
		return string(fw.Bytes()), nil
	}
	if intent.Kind != ReasoningLevel {
		return "", fmt.Errorf("%w: unsupported Gemini reasoning intent", ErrReasoningIncompatible)
	}
	return thinkingConfigRaw(intent.Level, model), nil
}

var geminiSchemaAllowedKeys = map[string]struct{}{
	"type":             {},
	"nullable":         {},
	"description":      {},
	"format":           {},
	"enum":             {},
	"items":            {},
	"properties":       {},
	"required":         {},
	"title":            {},
	"default":          {},
	"example":          {},
	"pattern":          {},
	"anyOf":            {},
	"maxItems":         {},
	"maxLength":        {},
	"maxProperties":    {},
	"maximum":          {},
	"minItems":         {},
	"minLength":        {},
	"minProperties":    {},
	"minimum":          {},
	"propertyOrdering": {},
}

var geminiSupportedFormats = map[string]struct{}{
	"enum":      {},
	"date-time": {},
	"float":     {},
	"double":    {},
	"int32":     {},
	"int64":     {},
}

func sanitizeSchemaForGemini(v any) (any, error) {
	inlined, err := inlineGeminiSchemaRefs(v)
	if err != nil {
		return nil, err
	}
	return sanitizeGeminiSchemaNode(inlined, "$")
}

func sanitizeGeminiSchemaNode(v any, path string) (any, error) {
	if truth, ok := v.(bool); ok {
		if truth {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("%w at %s: false schemas cannot be represented", ErrGeminiSchemaIncompatible, path)
	}
	node, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w at %s: schema must be an object", ErrGeminiSchemaIncompatible, path)
	}

	if allOf, exists := node["allOf"]; exists {
		merged, widened, err := mergeGeminiAllOf(allOf, path+".allOf")
		if err != nil {
			return nil, err
		}
		base := cloneSchemaMap(node)
		delete(base, "allOf")
		if err := normalizeGeminiNullableType(base, path); err != nil {
			return nil, err
		}
		node, ok = intersectGeminiSchemas(base, merged)
		if !ok {
			node = mergeSchemaMapsLenient(base, merged)
			widened = true
			observability.Get().Info("Gemini allOf conflicts with sibling constraints — keeping sibling values", "path", path)
		}
		if widened {
			pruneGeminiRequired(node)
		}
	}
	if _, exists := node["oneOf"]; exists {
		return nil, fmt.Errorf("%w at %s.oneOf: Gemini does not support oneOf", ErrGeminiSchemaIncompatible, path)
	}

	nullableFromAnyOf := false
	out := make(map[string]any, len(node))
	for key, child := range node {
		if key == "$defs" || key == "definitions" || key == "$ref" {
			continue
		}
		if key == "const" {
			continue
		}
		if key == "$schema" || key == "additionalProperties" || key == "propertyNames" {
			continue
		}
		if key == "exclusiveMinimum" || key == "exclusiveMaximum" || key == "minimum" || key == "maximum" {
			continue
		}
		if _, supported := geminiSchemaAllowedKeys[key]; !supported {
			return nil, fmt.Errorf("%w at %s.%s: unsupported constraint", ErrGeminiSchemaIncompatible, path, key)
		}
		switch key {
		case "properties":
			properties, ok := child.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%w at %s.properties: expected object", ErrGeminiSchemaIncompatible, path)
			}
			clean := make(map[string]any, len(properties))
			for name, value := range properties {
				var err error
				clean[name], err = sanitizeGeminiSchemaNode(value, path+".properties."+name)
				if err != nil {
					return nil, err
				}
			}
			out[key] = clean
		case "items":
			clean, err := sanitizeGeminiSchemaNode(child, path+".items")
			if err != nil {
				return nil, err
			}
			out[key] = clean
		case "anyOf":
			branches, ok := child.([]any)
			if !ok || len(branches) == 0 {
				return nil, fmt.Errorf("%w at %s.anyOf: expected non-empty array", ErrGeminiSchemaIncompatible, path)
			}
			clean := make([]any, 0, len(branches))
			for i, branch := range branches {
				if isGeminiNullLikeSchema(branch) {
					nullableFromAnyOf = true
					continue
				}
				sanitized, err := sanitizeGeminiSchemaNode(branch, fmt.Sprintf("%s.anyOf[%d]", path, i))
				if err != nil {
					return nil, err
				}
				if isGeminiNullLikeSchema(sanitized) {
					nullableFromAnyOf = true
					continue
				}
				clean = append(clean, sanitized)
			}
			if len(clean) == 0 {
				return nil, fmt.Errorf("%w at %s.anyOf: expected a non-null branch", ErrGeminiSchemaIncompatible, path)
			}
			out[key] = clean
		case "format":
			format, ok := child.(string)
			if !ok {
				return nil, fmt.Errorf("%w at %s.format: expected string", ErrGeminiSchemaIncompatible, path)
			}
			if _, supported := geminiSupportedFormats[format]; !supported {
				continue
			}
			out[key] = format
		default:
			out[key] = deepCopyJSON(child)
		}
	}

	if constant, exists := node["const"]; exists {
		if enum, exists := out["enum"]; exists && !valueInEnum(constant, enum) {
			return nil, fmt.Errorf("%w at %s: const conflicts with enum", ErrGeminiSchemaIncompatible, path)
		}
		out["enum"] = []any{deepCopyJSON(constant)}
	}
	if err := resolveGeminiExclusiveBound(node, out, "minimum", "exclusiveMinimum", path); err != nil {
		return nil, err
	}
	if err := resolveGeminiExclusiveBound(node, out, "maximum", "exclusiveMaximum", path); err != nil {
		return nil, err
	}
	if err := normalizeGeminiNullableType(out, path); err != nil {
		return nil, err
	}
	if nullable, ok := out["nullable"].(bool); ok && nullable {
		nullableFromAnyOf = true
	}
	flattened, err := flattenGeminiAnyOf(out, path)
	if err != nil {
		return nil, err
	}
	out = flattened
	if err := validateGeminiRequired(out, path); err != nil {
		return nil, err
	}
	resolveGeminiEnum(out)
	if _, hasType := out["type"]; !hasType {
		if _, hasEnum := out["enum"]; hasEnum {
			out["type"] = "string"
		}
	}
	if nullableFromAnyOf {
		out["nullable"] = true
	}
	return stripEmptyNullable(out), nil
}

func resolveGeminiExclusiveBound(node, out map[string]any, key, exclusiveKey, path string) error {
	explicit, hasExplicit := node[key]
	exclusiveVal, hasExclusive := node[exclusiveKey]
	if !hasExplicit && !hasExclusive {
		return nil
	}
	if !hasExclusive {
		out[key] = deepCopyJSON(explicit)
		return nil
	}
	if !hasExplicit {
		out[key] = deepCopyJSON(exclusiveVal)
		return nil
	}
	e, eOK := explicit.(float64)
	x, xOK := exclusiveVal.(float64)
	if !eOK || !xOK {
		return fmt.Errorf("%w at %s.%s: non-numeric bound", ErrGeminiSchemaIncompatible, path, exclusiveKey)
	}
	if key == "minimum" && x > e || key == "maximum" && x < e {
		out[key] = exclusiveVal
	} else {
		out[key] = explicit
	}
	return nil
}

func mergeGeminiAllOf(v any, path string) (merged map[string]any, widened bool, err error) {
	branches, ok := v.([]any)
	if !ok || len(branches) == 0 {
		return nil, false, fmt.Errorf("%w at %s: expected non-empty array", ErrGeminiSchemaIncompatible, path)
	}
	merged = map[string]any{}
	var (
		matched  bool
		firstErr error
	)
	for i, branch := range branches {
		branchPath := fmt.Sprintf("%s[%d]", path, i)
		clean, cleanErr := sanitizeGeminiSchemaNode(branch, branchPath)
		if cleanErr != nil {
			widened = true
			if firstErr == nil {
				firstErr = cleanErr
			}
			observability.Get().Info("Gemini allOf branch is unrepresentable — widening by dropping it", "path", branchPath, "err", cleanErr)
			continue
		}
		object, isObject := clean.(map[string]any)
		if !isObject {
			widened = true
			branchErr := fmt.Errorf("%w at %s: branch is not an object schema", ErrGeminiSchemaIncompatible, branchPath)
			if firstErr == nil {
				firstErr = branchErr
			}
			observability.Get().Info("Gemini allOf branch is unrepresentable — widening by dropping it", "path", branchPath, "err", branchErr)
			continue
		}
		candidate, mergedOK := intersectGeminiSchemas(merged, object)
		if !mergedOK {
			widened = true
			if firstErr == nil {
				firstErr = fmt.Errorf("%w at %s: branch conflicts with prior branches", ErrGeminiSchemaIncompatible, branchPath)
			}
			observability.Get().Info("Gemini allOf branch is unrepresentable — widening by dropping it", "path", branchPath, "conflict", "branch conflicts with prior branches")
			continue
		}
		merged = candidate
		matched = true
	}
	if !matched {
		return nil, false, firstErr
	}
	return stripEmptyNullable(merged), widened, nil
}

func cloneSchemaMap(base map[string]any) map[string]any {
	out := make(map[string]any, len(base))
	for key, value := range base {
		out[key] = deepCopyJSON(value)
	}
	return out
}

var geminiAnnotationKeys = map[string]struct{}{
	"description": {},
	"title":       {},
	"default":     {},
	"example":     {},
}

var geminiStricterBoundKeys = map[string]bool{
	"minimum":       true,
	"minLength":     true,
	"minItems":      true,
	"minProperties": true,
	"maximum":       false,
	"maxLength":     false,
	"maxItems":      false,
	"maxProperties": false,
}

func intersectGeminiSchemas(left, right map[string]any) (map[string]any, bool) {
	left = clampNullableTyped(left)
	right = clampNullableTyped(right)
	out := cloneSchemaMap(left)
	for key, value := range right {
		current, exists := out[key]
		if !exists {
			out[key] = deepCopyJSON(value)
			continue
		}
		if key == "properties" {
			leftProperties, leftOK := current.(map[string]any)
			rightProperties, rightOK := value.(map[string]any)
			if !leftOK || !rightOK {
				return nil, false
			}
			mergedProperties, ok := mergeGeminiPropertyMaps(leftProperties, rightProperties)
			if !ok {
				return nil, false
			}
			out[key] = mergedProperties
			continue
		}
		if key == "required" {
			out[key] = uniqueStrings(current, value)
			if out[key] == nil {
				return nil, false
			}
			continue
		}
		if key == "items" {
			leftItems, leftOK := current.(map[string]any)
			rightItems, rightOK := value.(map[string]any)
			if !leftOK || !rightOK {
				return nil, false
			}
			mergedItems, ok := intersectGeminiSchemas(leftItems, rightItems)
			if !ok {
				return nil, false
			}
			out[key] = mergedItems
			continue
		}
		if _, isAnnotation := geminiAnnotationKeys[key]; isAnnotation {
			continue
		}
		if key == "nullable" {
			leftNullable, leftOK := current.(bool)
			rightNullable, rightOK := value.(bool)
			if !leftOK || !rightOK {
				return nil, false
			}
			out[key] = leftNullable && rightNullable
			continue
		}
		if key == "enum" {
			shared, ok := intersectEnums(current, value)
			if !ok {
				return nil, false
			}
			out[key] = shared
			continue
		}
		if larger, isBound := geminiStricterBoundKeys[key]; isBound {
			stricter, ok := stricterBound(current, value, larger)
			if !ok {
				return nil, false
			}
			out[key] = stricter
			continue
		}
		if !semanticJSONEqual(current, value) {
			return nil, false
		}
	}
	return out, true
}

func clampNullableTyped(schema map[string]any) map[string]any {
	typeValue, hasType := schema["type"]
	if !hasType {
		return schema
	}
	if types, isArray := typeValue.([]any); isArray {
		primary, ok := lowerNullableTypeArray(types)
		if !ok {
			return schema
		}
		out := cloneSchemaMap(schema)
		out["type"] = primary
		out["nullable"] = true
		return out
	}
	if _, hasNullable := schema["nullable"]; hasNullable {
		return schema
	}
	out := cloneSchemaMap(schema)
	out["nullable"] = false
	return out
}

func lowerNullableTypeArray(types []any) (string, bool) {
	if len(types) != 2 {
		return "", false
	}
	primary := ""
	hasNull := false
	for _, candidate := range types {
		name, ok := candidate.(string)
		if !ok {
			return "", false
		}
		if name == "null" {
			if hasNull {
				return "", false
			}
			hasNull = true
			continue
		}
		if primary != "" {
			return "", false
		}
		primary = name
	}
	if primary == "" || !hasNull {
		return "", false
	}
	return primary, true
}

func stripEmptyNullable(schema map[string]any) map[string]any {
	if _, hasType := schema["type"]; !hasType {
		return schema
	}
	if nullable, ok := schema["nullable"].(bool); ok && !nullable {
		delete(schema, "nullable")
	}
	return schema
}

func mergeGeminiPropertyMaps(left, right map[string]any) (map[string]any, bool) {
	out := cloneSchemaMap(left)
	for name, value := range right {
		current, exists := out[name]
		if !exists {
			out[name] = deepCopyJSON(value)
			continue
		}
		leftSchema, leftOK := current.(map[string]any)
		rightSchema, rightOK := value.(map[string]any)
		if !leftOK || !rightOK {
			if !semanticJSONEqual(current, value) {
				return nil, false
			}
			continue
		}
		merged, ok := intersectGeminiSchemas(leftSchema, rightSchema)
		if !ok {
			return nil, false
		}
		out[name] = merged
	}
	return out, true
}

func intersectEnums(left, right any) (any, bool) {
	leftValues, leftOK := left.([]any)
	rightValues, rightOK := right.([]any)
	if !leftOK || !rightOK {
		return nil, false
	}
	shared := make([]any, 0, len(leftValues))
	for _, candidate := range leftValues {
		if valueInEnum(candidate, rightValues) {
			shared = append(shared, deepCopyJSON(candidate))
		}
	}
	if len(shared) == 0 {
		return nil, false
	}
	return shared, true
}

func stricterBound(left, right any, larger bool) (any, bool) {
	leftValue, leftOK := left.(float64)
	rightValue, rightOK := right.(float64)
	if !leftOK || !rightOK {
		if !semanticJSONEqual(left, right) {
			return nil, false
		}
		return deepCopyJSON(left), true
	}
	if larger == (rightValue > leftValue) {
		return rightValue, true
	}
	return leftValue, true
}

func mergeSchemaMapsLenient(left, right map[string]any) map[string]any {
	out := cloneSchemaMap(left)
	for key, value := range right {
		current, exists := out[key]
		if !exists {
			out[key] = deepCopyJSON(value)
			continue
		}
		if key == "properties" {
			leftProperties, leftOK := current.(map[string]any)
			rightProperties, rightOK := value.(map[string]any)
			if !leftOK || !rightOK {
				continue
			}
			out[key] = mergeSchemaMapsLenient(leftProperties, rightProperties)
			continue
		}
		if key == "required" {
			if merged := uniqueStrings(current, value); merged != nil {
				out[key] = merged
			}
			continue
		}
	}
	return out
}

func pruneGeminiRequired(node map[string]any) {
	required, ok := node["required"].([]any)
	if !ok {
		return
	}
	properties, _ := node["properties"].(map[string]any)
	if properties == nil {
		delete(node, "required")
		return
	}
	kept := make([]any, 0, len(required))
	for _, entry := range required {
		name, ok := entry.(string)
		if !ok {
			continue
		}
		if _, exists := properties[name]; exists {
			kept = append(kept, name)
		}
	}
	if len(kept) == 0 {
		delete(node, "required")
		return
	}
	node["required"] = kept
}

func uniqueStrings(left, right any) any {
	values := make(map[string]struct{})
	for _, list := range []any{left, right} {
		entries, ok := list.([]any)
		if !ok {
			return nil
		}
		for _, entry := range entries {
			name, ok := entry.(string)
			if !ok {
				return nil
			}
			values[name] = struct{}{}
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]any, len(keys))
	for i, key := range keys {
		out[i] = key
	}
	return out
}

func normalizeGeminiNullableType(schema map[string]any, path string) error {
	types, isArray := schema["type"].([]any)
	if !isArray {
		return nil
	}
	if len(types) != 2 {
		return fmt.Errorf("%w at %s.type: only [type, null] is representable", ErrGeminiSchemaIncompatible, path)
	}
	primary := ""
	hasNull := false
	for _, candidate := range types {
		name, ok := candidate.(string)
		if !ok {
			return fmt.Errorf("%w at %s.type: type names must be strings", ErrGeminiSchemaIncompatible, path)
		}
		if name == "null" {
			if hasNull {
				return fmt.Errorf("%w at %s.type: duplicate null", ErrGeminiSchemaIncompatible, path)
			}
			hasNull = true
			continue
		}
		if primary != "" {
			return fmt.Errorf("%w at %s.type: multiple non-null types", ErrGeminiSchemaIncompatible, path)
		}
		primary = name
	}
	if primary == "" || !hasNull {
		return fmt.Errorf("%w at %s.type: only [type, null] is representable", ErrGeminiSchemaIncompatible, path)
	}
	schema["type"] = primary
	schema["nullable"] = true
	return nil
}

func validateGeminiRequired(schema map[string]any, path string) error {
	required, exists := schema["required"]
	if !exists {
		return nil
	}
	properties, _ := schema["properties"].(map[string]any)
	entries, ok := required.([]any)
	if !ok {
		return fmt.Errorf("%w at %s.required: expected array", ErrGeminiSchemaIncompatible, path)
	}
	for _, entry := range entries {
		name, ok := entry.(string)
		if !ok || properties == nil {
			return fmt.Errorf("%w at %s.required: cannot represent required property", ErrGeminiSchemaIncompatible, path)
		}
		if _, exists := properties[name]; !exists {
			return fmt.Errorf("%w at %s.required: %q has no declared property", ErrGeminiSchemaIncompatible, path, name)
		}
	}
	return nil
}

func resolveGeminiEnum(schema map[string]any) {
	values, exists := schema["enum"]
	if !exists {
		return
	}
	enum, ok := values.([]any)
	if ok && len(enum) > 0 && allStringEnum(enum) {
		filtered := filterEmptyStringEnum(enum)
		if len(filtered) == 0 {
			delete(schema, "enum")
		} else {
			schema["enum"] = filtered
		}
		if _, exists := schema["enum"]; exists {
			return
		}
	} else {
		delete(schema, "enum")
	}
	if format, _ := schema["format"].(string); format == "enum" {
		delete(schema, "format")
	}
}

func allStringEnum(enum []any) bool {
	for _, value := range enum {
		if _, ok := value.(string); !ok {
			return false
		}
	}
	return true
}

func filterEmptyStringEnum(enum []any) []any {
	out := make([]any, 0, len(enum))
	for _, value := range enum {
		s, ok := value.(string)
		if !ok || s == "" {
			continue
		}
		out = append(out, s)
	}
	return out
}

func isGeminiNullSchema(v any) bool {
	node, ok := v.(map[string]any)
	if !ok {
		return false
	}
	if typeName, ok := node["type"].(string); ok {
		return typeName == "null"
	}
	types, ok := node["type"].([]any)
	if !ok || len(types) == 0 {
		return false
	}
	for _, candidate := range types {
		name, ok := candidate.(string)
		if !ok || name != "null" {
			return false
		}
	}
	return true
}

func isGeminiNullLikeSchema(v any) bool {
	if isGeminiNullSchema(v) {
		return true
	}
	node, ok := v.(map[string]any)
	if !ok {
		return false
	}
	enum, hasEnum := node["enum"].([]any)
	if hasEnum {
		if !allStringEnum(enum) || len(filterEmptyStringEnum(enum)) > 0 {
			return false
		}
	}
	switch typeValue := node["type"].(type) {
	case string:
		if typeValue != "" && typeValue != "null" {
			return false
		}
	case []any:
		if _, ok := lowerNullableTypeArray(typeValue); ok {
			return false
		}
		if !isGeminiNullSchema(node) {
			return false
		}
	case nil:
		if !hasEnum {
			return false
		}
	default:
		return false
	}
	for key := range node {
		switch key {
		case "enum", "type", "description", "title", "nullable", "default", "example", "format":
			continue
		default:
			return false
		}
	}
	return true
}

func flattenGeminiAnyOf(schema map[string]any, path string) (map[string]any, error) {
	branches, ok := schema["anyOf"].([]any)
	if !ok || len(branches) != 1 {
		return schema, nil
	}
	object, ok := branches[0].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w at %s.anyOf: branch is not an object schema", ErrGeminiSchemaIncompatible, path)
	}
	delete(schema, "anyOf")
	merged, ok := intersectGeminiSchemas(schema, object)
	if !ok {
		return nil, fmt.Errorf("%w at %s.anyOf: flattened branch conflicts with sibling constraints", ErrGeminiSchemaIncompatible, path)
	}
	return merged, nil
}

func valueInEnum(value, enum any) bool {
	values, ok := enum.([]any)
	if !ok {
		return false
	}
	for _, candidate := range values {
		if semanticJSONEqual(value, candidate) {
			return true
		}
	}
	return false
}

func inlineGeminiSchemaRefs(v any) (any, error) {
	root, ok := v.(map[string]any)
	if !ok {
		return v, nil
	}
	defs := make(map[string]any)
	for _, key := range []string{"$defs", "definitions"} {
		if definitions, ok := root[key].(map[string]any); ok {
			for name, value := range definitions {
				defs[key+"/"+name] = value
			}
		}
	}
	return resolveGeminiSchemaRefs(v, defs, map[string]struct{}{}, "$")
}

func resolveGeminiSchemaRefs(v any, defs map[string]any, visiting map[string]struct{}, path string) (any, error) {
	switch node := v.(type) {
	case map[string]any:
		if ref, ok := node["$ref"].(string); ok {
			name := strings.TrimPrefix(ref, "#/")
			target, exists := defs[name]
			if !exists {
				return nil, fmt.Errorf("%w at %s: unresolved reference %q", ErrGeminiSchemaIncompatible, path, ref)
			}
			if _, cycle := visiting[name]; cycle {
				return nil, fmt.Errorf("%w at %s: cyclic reference %q", ErrGeminiSchemaIncompatible, path, ref)
			}
			visiting[name] = struct{}{}
			resolved, err := resolveGeminiSchemaRefs(deepCopyJSON(target), defs, visiting, path)
			delete(visiting, name)
			return resolved, err
		}
		out := make(map[string]any, len(node))
		for key, child := range node {
			if key == "$defs" || key == "definitions" {
				continue
			}
			resolved, err := resolveGeminiSchemaRefs(child, defs, visiting, path+"."+key)
			if err != nil {
				return nil, err
			}
			out[key] = resolved
		}
		return out, nil
	case []any:
		out := make([]any, len(node))
		for i, child := range node {
			resolved, err := resolveGeminiSchemaRefs(child, defs, visiting, fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return nil, err
			}
			out[i] = resolved
		}
		return out, nil
	default:
		return deepCopyJSON(v), nil
	}
}

func semanticJSONEqual(left, right any) bool {
	return reflect.DeepEqual(canonicalJSON(left), canonicalJSON(right))
}

func canonicalJSON(v any) any {
	switch node := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(node))
		for key, value := range node {
			out[key] = canonicalJSON(value)
		}
		return out
	case []any:
		out := make([]any, len(node))
		for i, value := range node {
			out[i] = canonicalJSON(value)
		}
		return out
	default:
		return node
	}
}

func dedupeGeminiDeclarations(declarations []any) ([]any, error) {
	seen := make(map[string]any, len(declarations))
	out := make([]any, 0, len(declarations))
	for _, declaration := range declarations {
		object, ok := declaration.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: declaration is not an object", ErrGeminiToolDeclarationConflict)
		}
		name, _ := object["name"].(string)
		if name == "" {
			return nil, fmt.Errorf("%w: declaration has no name", ErrGeminiToolDeclarationConflict)
		}
		if previous, exists := seen[name]; exists {
			if !semanticJSONEqual(previous, object) {
				return nil, fmt.Errorf("%w: duplicate declaration %q differs", ErrGeminiToolDeclarationConflict, name)
			}
			continue
		}
		seen[name] = object
		out = append(out, object)
	}
	return out, nil
}
