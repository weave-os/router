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

	"workweave/router/internal/providers"
	"workweave/router/internal/router"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	// ErrGeminiSchemaIncompatible marks a JSON Schema constraint that the Gemini
	// function declaration schema cannot represent without changing its meaning.
	ErrGeminiSchemaIncompatible = errors.New("gemini schema is not representable")
	// ErrGeminiToolDeclarationConflict marks duplicate function names with
	// different declarations. Gemini would make their resolution ambiguous.
	ErrGeminiToolDeclarationConflict = errors.New("gemini function declarations conflict")
	// ErrGeminiUnsignedToolHistory marks a Gemini 3.x continuation that would
	// require deleting prior tool history to reach the upstream.
	ErrGeminiUnsignedToolHistory = errors.New("gemini tool history lacks thought signatures")
)

// PrepareGemini builds a Gemini native REST request body. Native is required for
// multi-turn tool use on Gemini 3.x: OpenAI-compat doesn't return thought_signature.
func (e *RequestEnvelope) PrepareGemini(_ http.Header, opts EmitOptions) (providers.PreparedRequest, error) {
	if isGemini3xModel(opts.TargetModel) && e.HasUnsignedToolCallHistory() {
		return providers.PreparedRequest{}, fmt.Errorf("%w: Gemini 3.x requires a thoughtSignature on every historical function call", ErrGeminiUnsignedToolHistory)
	}
	// Strip synthetic top-level "model" and "stream" — belonging to routing, not Gemini.
	if e.format == FormatGemini {
		body := e.body
		var err error
		body, err = sjson.DeleteBytes(body, "model")
		if err != nil {
			return providers.PreparedRequest{}, fmt.Errorf("strip model field: %w", err)
		}
		body, err = sjson.DeleteBytes(body, "stream")
		if err != nil {
			return providers.PreparedRequest{}, fmt.Errorf("strip stream field: %w", err)
		}
		// Same-format Gemini requests still need tool schema sanitization:
		// the inbound tools may carry JSON Schema keywords Gemini rejects.
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
		filtered, removed, err := filterClaudeCodeOnlyToolsFromAnthropicBody(e.body, opts.KeepCrossVendorOrchestrationTools)
		if err != nil {
			return providers.PreparedRequest{}, fmt.Errorf("strip claude-code-only tools: %w", err)
		}
		stats.CCOnlyToolsStripped = removed
		// Mirror writeGeminiFromAnthropic's reminder gate so Stats reflects
		// whether the reminder actually reached upstream.
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

	// Synthetic header so proxy.Service stays ignorant of provider URL composition.
	headers := make(http.Header)
	if e.Stream() {
		headers.Set(GeminiStreamHintHeader, "true")
	}
	return providers.PreparedRequest{Body: body, Headers: headers, Stats: stats}, nil
}

// GeminiStreamHintHeader tells the native Gemini client to pick
// :streamGenerateContent over :generateContent; stripped before forwarding.
const GeminiStreamHintHeader = "X-Router-Gemini-Stream"

// sanitizeGeminiTools runs sanitizeSchemaForGemini over every function
// declaration's parameters in an already-Gemini-format body, since the
// same-format path bypasses the per-field translation helpers.
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

// sanitizeGeminiToolsRaw is the gjson-compatible walker for the tools array.
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

// copyMap returns a shallow copy of m so that modifying the copy does not
// mutate the original, which gjson may be sharing with the caller.
func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// ----- OpenAI → Gemini -----

// writeGeminiFromOpenAI translates an OpenAI-format body into Gemini fields
// written directly into jw (caller has already opened the root object).
func writeGeminiFromOpenAI(jw *jsonWriter, body []byte, opts EmitOptions) error {
	msgs := gjson.GetBytes(body, "messages")

	// Build tool_call ID → name map for tool-result recovery. Unsigned Gemini
	// 3.x history is rejected by PrepareGemini before we begin emission.
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
	// Collect system text.
	var sysParts []string
	msgs.ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() != "system" {
			return true
		}
		content := msg.Get("content")
		switch content.Type {
		case gjson.String:
			if s := content.String(); s != "" {
				sysParts = append(sysParts, s)
			}
		case gjson.JSON:
			content.ForEach(func(_, part gjson.Result) bool {
				if part.Get("type").String() == "text" {
					if s := part.Get("text").String(); s != "" {
						sysParts = append(sysParts, s)
					}
				}
				return true
			})
		}
		return true
	})
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

	// Build entries, then collapse before emitting to preserve Gemini's role
	// alternation requirement.
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

// openAIUserPartsGJSON converts an OpenAI user content value to raw JSON part strings.
// http(s) image URLs are unsupported on the native surface and dropped.
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
	}
	return nil
}

// openAIAssistantPartsGJSON converts an OpenAI assistant message to raw JSON part strings.
func openAIAssistantPartsGJSON(msg gjson.Result) ([]string, error) {
	var parts []string

	content := msg.Get("content")
	if text := openAIContentTextGJSON(content); text != "" {
		parts = append(parts, geminiTextPart(text))
	}

	// Inherit any sig from sibling tool_calls or message-level thought_signature
	// so every functionCall carries one on round-trip to Gemini 3.x.
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

// writeGeminiToolsFromOpenAI writes the tools array into jw from an OpenAI body.
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
				emitErr = fmt.Errorf("function %q parameters: %w", name, err)
				return false
			}
			declaration["parameters"] = clean
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

// writeGeminiToolChoiceFromOpenAI writes toolConfig into jw from an OpenAI body.
// Mirrors writeGeminiToolChoiceFromAnthropic: unforced tool_choice (absent or
// "auto") on Gemini 3.x gets mode=VALIDATED for schema-constrained decoding;
// explicit none/required/function choices pass through untouched.
func writeGeminiToolChoiceFromOpenAI(jw *jsonWriter, body []byte, model string, downgradeValidated bool) {
	kind, name := openAIToolChoice(body)
	writeGeminiToolChoice(jw, body, model, downgradeValidated, kind, name)
}

// writeGeminiGenerationConfigFromOpenAI writes generationConfig into jw from an OpenAI body.
func writeGeminiGenerationConfigFromOpenAI(jw *jsonWriter, body []byte, opts EmitOptions) error {
	model := opts.TargetModel
	// Collect all generation config fields; only write the object if non-empty.
	type field struct {
		key string
		raw string
	}
	var fields []field

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
	// max_completion_tokens takes precedence over max_tokens if both present.
	if r := gjson.GetBytes(body, "max_completion_tokens"); r.Exists() && r.Type == gjson.Number {
		fw := newJSONWriter()
		fw.Int(clampToModelOutputCap(int64(r.Num), model))
		fields = append(fields, field{"maxOutputTokens", string(fw.Bytes())})
	} else if r := gjson.GetBytes(body, "max_tokens"); r.Exists() && r.Type == gjson.Number {
		fw := newJSONWriter()
		fw.Int(clampToModelOutputCap(int64(r.Num), model))
		fields = append(fields, field{"maxOutputTokens", string(fw.Bytes())})
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
	intent, err := applyGeminiReasoning(ParseReasoningIntent(FormatOpenAI, body), opts)
	if err != nil {
		return err
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

// ----- Anthropic → Gemini -----

// writeGeminiFromAnthropic translates an Anthropic-format body into Gemini fields
// written directly into jw (caller has already opened the root object).
func writeGeminiFromAnthropic(jw *jsonWriter, body []byte, opts EmitOptions) error {
	// System prompt.
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

	// Collect tool_use ID → name for tool-result recovery. PrepareGemini
	// rejects unsigned Gemini 3.x history before emission begins.
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
	// Build entries, then collapse + emit to preserve role alternation.
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

// contentEntry is a buffered Gemini `contents` entry.
type contentEntry struct {
	role  string   // "user" or "model"
	parts []string // pre-serialized Gemini part JSON objects
}

// collapseConsecutiveRoles merges adjacent same-role entries.
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

// emitGeminiContents writes the contents array from a collapsed entry slice.
// Skips writing the key entirely when there are no entries so absence and
// emptiness aren't conflated downstream.
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

// geminiFunctionResponsePart serializes a Gemini functionResponse part.
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

// isGemini3xModel reports whether model is a Gemini 3.x preview, which
// requires thoughtSignature on every functionCall; 2.x accepts sig-less calls.
func isGemini3xModel(model string) bool {
	return strings.HasPrefix(model, "gemini-3")
}

// geminiToolMode returns VALIDATED normally, or AUTO when the proxy requested
// a downgrade after a VALIDATED-mode INVALID_ARGUMENT — AUTO skips decode-time
// schema-grammar compilation so uncompilable tool schemas stop 400ing.
func geminiToolMode(downgradeValidated bool) string {
	if downgradeValidated {
		return "AUTO"
	}
	return "VALIDATED"
}

// geminiEmitsValidatedToolMode reports whether the emit path would set
// mode=VALIDATED for body+model. Mirrors the gate in
// writeGeminiToolChoiceFrom{Anthropic,OpenAI} so RequestMutationStats can
// predict it without re-deriving.
func geminiEmitsValidatedToolMode(body []byte, model string, format Format) bool {
	if !hasNonEmptyTools(body) || !isGemini3xModel(model) {
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

// filterOutGeminiFunctionCallParts strips functionCall parts; used when
// history has tool_use blocks without thoughtSignature (Gemini 3.x would 400).
func filterOutGeminiFunctionCallParts(parts []string) []string {
	out := parts[:0]
	for _, p := range parts {
		if gjson.Get(p, "functionCall").Exists() {
			continue
		}
		out = append(out, p)
	}
	return out
}

// filterOutGeminiToolResponseParts strips functionResponse parts left dangling
// by a dropped functionCall; Gemini rejects responses with no matching call.
func filterOutGeminiToolResponseParts(parts []string) []string {
	out := parts[:0]
	for _, p := range parts {
		if gjson.Get(p, "functionResponse").Exists() {
			continue
		}
		out = append(out, p)
	}
	return out
}

// anthropicUserPartsGJSON converts an Anthropic user content value to raw JSON part strings.
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
	}
	return nil
}

// anthropicAssistantPartsGJSON converts an Anthropic assistant content value to raw JSON part strings.
func anthropicAssistantPartsGJSON(content gjson.Result) []string {
	switch content.Type {
	case gjson.String:
		s := content.String()
		if s == "" {
			return nil
		}
		return []string{geminiTextPart(s)}
	case gjson.JSON:
		// Find any thought_signature in the turn so functionCall blocks without
		// their own sig can inherit it (only one block per turn usually has one).
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
				// Fall back to the turn's inherited sig so every functionCall has one.
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
	}
	return nil
}

// writeGeminiToolsFromAnthropic writes the tools array into jw from an Anthropic body.
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
				emitErr = fmt.Errorf("function %q input_schema: %w", name, err)
				return false
			}
			declaration["parameters"] = clean
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

// writeGeminiToolChoiceFromAnthropic writes toolConfig into jw from an Anthropic
// body. Unforced tool_choice (absent or "auto") on Gemini 3.x gets
// mode=VALIDATED — schema-constrained decoding without forcing a tool call.
// Explicit any/none/tool choices pass through untouched.
func writeGeminiToolChoiceFromAnthropic(jw *jsonWriter, body []byte, model string, downgradeValidated bool) {
	kind, name := anthropicToolChoice(body)
	writeGeminiToolChoice(jw, body, model, downgradeValidated, kind, name)
}

// writeGeminiToolChoice writes toolConfig into jw from a source-neutral
// tool_choice kind. Unforced tool_choice (absent or auto) on Gemini 3.x gets
// mode=VALIDATED for schema-constrained decoding; explicit
// none/required/named choices map straight to a functionCallingConfig mode.
func writeGeminiToolChoice(jw *jsonWriter, body []byte, model string, downgradeValidated bool, kind toolChoiceKind, name string) {
	if (kind == toolChoiceAbsent || kind == toolChoiceAuto) && hasNonEmptyTools(body) && isGemini3xModel(model) {
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
	}
}

// writeGeminiFunctionCallingMode writes a toolConfig.functionCallingConfig
// object with the given mode, optionally pinning a single allowed function
// name (used for the named-tool choice, which Gemini expresses as ANY mode
// plus an allowedFunctionNames allowlist of one).
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

// writeGeminiGenerationConfigFromAnthropic writes generationConfig into jw from an Anthropic body.
func writeGeminiGenerationConfigFromAnthropic(jw *jsonWriter, body []byte, opts EmitOptions) error {
	model := opts.TargetModel
	type field struct {
		key string
		raw string
	}
	var fields []field

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
		fw := newJSONWriter()
		fw.Int(clampToModelOutputCap(int64(r.Num), model))
		fields = append(fields, field{"maxOutputTokens", string(fw.Bytes())})
	}
	if r := gjson.GetBytes(body, "stop_sequences"); r.Exists() {
		if raw := stopToArrayRaw(r); raw != "" {
			fields = append(fields, field{"stopSequences", raw})
		}
	}
	intent, err := applyGeminiReasoning(ParseReasoningIntent(FormatAnthropic, body), opts)
	if err != nil {
		return err
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

// clampToModelOutputCap caps v to the model's max output token limit.
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

// stopToArrayRaw serializes a stop gjson.Result into a raw JSON array string.
// Returns "" when no valid stops exist.
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
	// Anthropic's legacy thinking API carries a numeric token budget, but Gemini
	// 3.x accepts only named thinking levels. Normalize at the Gemini boundary so
	// the generic capability validator still rejects numeric budgets for targets
	// that genuinely cannot represent them.
	if isGemini3xModel(opts.TargetModel) && intent.Kind == ReasoningBudget {
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

// geminiThinkingConfigRaw applies a validated canonical intent to Gemini's
// native thinkingConfig shape.
func geminiThinkingConfigRaw(intent ReasoningIntent, model string) (string, error) {
	if intent.Kind == "" || intent.Kind == ReasoningAuto {
		return "", nil
	}
	if intent.Kind == ReasoningDisabled {
		if isGemini3xModel(model) {
			return "", nil
		}
		return thinkingConfigRaw("none", model), nil
	}
	if intent.Kind == ReasoningBudget {
		if isGemini3xModel(model) {
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

// thinkingConfigRaw returns raw JSON for a validated Gemini reasoning level.
func thinkingConfigRaw(effort, model string) string {
	// 3.x uses thinkingLevel; the legacy numeric thinkingBudget is documented as
	// suboptimal there and mixing both fields 400s. 2.5 keeps thinkingBudget.
	if isGemini3xModel(model) {
		var level string
		switch effort {
		case "low", "medium", "high":
			level = effort
		case "max", "xhigh":
			level = "high"
		default:
			// 3.x can't disable thinking — omit config rather than send an invalid value.
			return ""
		}
		fw := newJSONWriter()
		fw.Obj()
		fw.Key("thinkingLevel")
		fw.Str(level)
		fw.EndObj()
		return string(fw.Bytes())
	}

	var budget int64
	switch effort {
	case "none":
		budget = 0
	case "low":
		budget = 1024
	case "medium":
		budget = 8192
	case "high", "max", "xhigh":
		budget = 24576
	default:
		return ""
	}
	fw := newJSONWriter()
	fw.Obj()
	fw.Key("thinkingBudget")
	fw.Int(budget)
	fw.EndObj()
	return string(fw.Bytes())
}

// geminiSchemaAllowedKeys is the set of JSON Schema keywords Gemini's
// function-calling API accepts (allow-list, not deny-list, so new keywords
// tool authors add are rejected before they can 400).
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

// geminiSupportedFormats are the "format" values Gemini accepts; any other
// value (uri, email, uuid, ...) makes Google reject the whole request, so
// sanitizeSchemaFiltered drops formats outside this set.
var geminiSupportedFormats = map[string]struct{}{
	"enum":      {},
	"date-time": {},
	"float":     {},
	"double":    {},
	"int32":     {},
	"int64":     {},
}

// sanitizeSchemaForGemini preserves schema meaning or returns ErrGeminiSchemaIncompatible.
// Gemini schema limits are a routing constraint, not license to widen a tool's input language.
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
		merged, err := mergeGeminiAllOf(allOf, path+".allOf")
		if err != nil {
			return nil, err
		}
		base := mergeSchemaMaps(node, nil, false)
		delete(base, "allOf")
		var conflictKey string
		node, conflictKey, ok = mergeSchemaMapsExact(base, merged)
		if !ok {
			return nil, fmt.Errorf(
				"%w at %s.allOf: %q is unsatisfiable against the sibling constraints",
				ErrGeminiSchemaIncompatible, path, conflictKey,
			)
		}
	}
	if _, exists := node["oneOf"]; exists {
		return nil, fmt.Errorf("%w at %s.oneOf: Gemini does not support oneOf", ErrGeminiSchemaIncompatible, path)
	}

	out := make(map[string]any, len(node))
	for key, child := range node {
		if key == "$defs" || key == "definitions" || key == "$ref" {
			continue
		}
		if key == "const" {
			continue
		}
		// $schema/additionalProperties/propertyNames have no Gemini
		// equivalent; drop them rather than drop the whole tool (#764
		// regressed this from #62's original strip-list to an allowlist).
		if key == "$schema" || key == "additionalProperties" || key == "propertyNames" {
			continue
		}
		// exclusiveMinimum/exclusiveMaximum are skipped here because both keys
		// of each pair must be seen before resolving; map iteration order is
		// undefined (see resolveGeminiExclusiveBound).
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
			clean := make([]any, len(branches))
			for i, branch := range branches {
				var err error
				clean[i], err = sanitizeGeminiSchemaNode(branch, fmt.Sprintf("%s.anyOf[%d]", path, i))
				if err != nil {
					return nil, err
				}
			}
			out[key] = clean
		case "format":
			format, ok := child.(string)
			if !ok {
				return nil, fmt.Errorf("%w at %s.format: expected string", ErrGeminiSchemaIncompatible, path)
			}
			if _, supported := geminiSupportedFormats[format]; !supported {
				// Drop unsupported format values (#387; #764 regressed to a hard
				// reject). toolcheck validates against the ORIGINAL schema, so
				// the format hint is still enforced where it matters.
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
	if err := validateGeminiRequired(out, path); err != nil {
		return nil, err
	}
	resolveGeminiEnum(out)
	return out, nil
}

// resolveGeminiExclusiveBound writes the inclusive bound key ("minimum" or
// "maximum") into out. Gemini has no exclusive-bound keyword, so
// exclusiveMinimum/exclusiveMaximum widen to inclusive; an explicit sibling
// wins if it is the stricter of the two. No-op if neither key is present.
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

func mergeGeminiAllOf(v any, path string) (map[string]any, error) {
	branches, ok := v.([]any)
	if !ok || len(branches) == 0 {
		return nil, fmt.Errorf("%w at %s: expected non-empty array", ErrGeminiSchemaIncompatible, path)
	}
	merged := map[string]any{}
	for i, branch := range branches {
		clean, err := sanitizeGeminiSchemaNode(branch, fmt.Sprintf("%s[%d]", path, i))
		if err != nil {
			return nil, err
		}
		object, ok := clean.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w at %s[%d]: branch is not an object schema", ErrGeminiSchemaIncompatible, path, i)
		}
		var conflictKey string
		var mergedOK bool
		merged, conflictKey, mergedOK = mergeSchemaMapsExact(merged, object)
		if !mergedOK {
			return nil, fmt.Errorf(
				"%w at %s[%d]: %q is unsatisfiable across branches",
				ErrGeminiSchemaIncompatible, path, i, conflictKey,
			)
		}
	}
	return merged, nil
}

func mergeSchemaMaps(base, extra map[string]any, overwrite bool) map[string]any {
	out := make(map[string]any, len(base)+len(extra))
	for key, value := range base {
		out[key] = deepCopyJSON(value)
	}
	for key, value := range extra {
		if _, exists := out[key]; !exists || overwrite {
			out[key] = deepCopyJSON(value)
		}
	}
	return out
}

// mergeSchemaMapsExact merges two schemas that must BOTH hold — the allOf
// branches of one node, or an allOf result against its sibling constraints —
// by intersecting each shared keyword. It reports false only when the
// intersection is genuinely empty (no value could satisfy both), never merely
// because two branches spell a constraint differently.
//
// The equality-or-reject version of this function 502'd every request carrying
// a tool whose schema intersects two string refinements: two `pattern`s, or a
// `$defs` description bound narrowed twice. allOf means intersection, so
// "different" is the normal case, not a conflict.
// conflictKey names the keyword (or dotted path to it) whose intersection was
// empty, so the error a rejected tool produces says which constraint could not
// be satisfied instead of only "branches conflict".
func mergeSchemaMapsExact(left, right map[string]any) (merged map[string]any, conflictKey string, ok bool) {
	out := mergeSchemaMaps(left, nil, false)
	for key, value := range right {
		current, exists := out[key]
		if !exists {
			out[key] = deepCopyJSON(value)
			continue
		}
		intersected, childKey, intersectedOK := intersectSchemaKeyword(key, current, value)
		if !intersectedOK {
			if childKey != "" {
				return nil, key + "." + childKey, false
			}
			return nil, key, false
		}
		out[key] = intersected
	}
	// nullable is a permission, not a restriction: alone among these keywords,
	// its ABSENCE narrows the schema. The per-key loop copies a key present on
	// only one side, which for nullable would hand back a schema admitting null
	// when the other side forbids it — so reconcile it from both inputs, and
	// only when one of them actually says something about null (otherwise a
	// merge of two unrelated bounds would sprout a nullable flag).
	if schemaMentionsNull(left) || schemaMentionsNull(right) {
		if schemaAllowsNull(left) && schemaAllowsNull(right) {
			out["nullable"] = true
		} else {
			delete(out, "nullable")
		}
	}
	return out, "", true
}

// schemaMentionsNull reports whether a schema says anything about null, in
// either spelling: the sanitized `nullable` flag, or the raw ["<type>","null"]
// union an un-sanitized sibling schema can still carry at the allOf seam.
func schemaMentionsNull(schema map[string]any) bool {
	if _, hasFlag := schema["nullable"]; hasFlag {
		return true
	}
	typeValue, hasType := schema["type"]
	if !hasType {
		return false
	}
	_, nullable, ok := splitNullableType(typeValue)
	return ok && nullable
}

// schemaAllowsNull reports whether a schema admits null. A schema with no type
// constraint and no nullable flag constrains nothing, so it admits null.
func schemaAllowsNull(schema map[string]any) bool {
	if flag, ok := schema["nullable"].(bool); ok {
		return flag
	}
	typeValue, hasType := schema["type"]
	if !hasType {
		return true
	}
	_, nullable, ok := splitNullableType(typeValue)
	return ok && nullable
}

// schemaLowerBoundKeys/schemaUpperBoundKeys hold the keywords whose
// intersection is the stricter of the two bounds.
var (
	schemaLowerBoundKeys = map[string]struct{}{
		"minimum":       {},
		"minLength":     {},
		"minItems":      {},
		"minProperties": {},
	}
	schemaUpperBoundKeys = map[string]struct{}{
		"maximum":       {},
		"maxLength":     {},
		"maxItems":      {},
		"maxProperties": {},
	}
)

// schemaAnnotationKeys carry documentation, not constraints. Two branches
// describing the same field differently is not an unsatisfiable schema, and
// Gemini accepts exactly one value, so the left (parent, or earlier-branch)
// spelling wins.
var schemaAnnotationKeys = map[string]struct{}{
	"description":      {},
	"title":            {},
	"default":          {},
	"example":          {},
	"propertyOrdering": {},
}

// schemaSingleValuedConstraintKeys are real constraints that a Gemini schema
// can only carry once. The true intersection of two different values is
// narrower than either, so keeping the left one WIDENS the tool's accepted
// input language. That is the same trade already made for
// exclusiveMinimum -> minimum (resolveGeminiExclusiveBound) and for dropping
// unsupported `format` values: a slightly wide tool schema routes and the
// tool's own handler still validates its arguments, whereas a rejected one
// fails the whole turn.
var schemaSingleValuedConstraintKeys = map[string]struct{}{
	"pattern": {},
	"format":  {},
}

// intersectSchemaKeyword intersects one keyword's two values under allOf
// semantics. Unknown keywords keep the old equality rule: silently picking a
// side for a constraint this function does not understand could widen a tool's
// input language without anyone noticing.
func intersectSchemaKeyword(key string, left, right any) (value any, conflictKey string, ok bool) {
	switch key {
	case "properties":
		leftProperties, leftOK := left.(map[string]any)
		rightProperties, rightOK := right.(map[string]any)
		if !leftOK || !rightOK {
			return nil, "", false
		}
		merged, childKey, mergedOK := intersectSchemaProperties(leftProperties, rightProperties)
		if !mergedOK {
			return nil, childKey, false
		}
		return merged, "", true
	case "items":
		leftItems, leftOK := left.(map[string]any)
		rightItems, rightOK := right.(map[string]any)
		if !leftOK || !rightOK {
			// items may legitimately be a non-object (a bool schema); fall back
			// to equality rather than pretending to intersect it.
			if semanticJSONEqual(left, right) {
				return deepCopyJSON(left), "", true
			}
			return nil, "", false
		}
		merged, childKey, mergedOK := mergeSchemaMapsExact(leftItems, rightItems)
		if !mergedOK {
			return nil, childKey, false
		}
		return merged, "", true
	case "required":
		merged := uniqueStrings(left, right)
		if merged == nil {
			return nil, "", false
		}
		return merged, "", true
	case "type":
		merged, mergedOK := intersectSchemaType(left, right)
		return merged, "", mergedOK
	case "enum":
		merged, mergedOK := intersectSchemaEnum(left, right)
		return merged, "", mergedOK
	case "nullable":
		// A value has to satisfy both branches, so null is permitted only if
		// both permit it.
		leftNullable, leftOK := left.(bool)
		rightNullable, rightOK := right.(bool)
		if !leftOK || !rightOK {
			return nil, "", false
		}
		return leftNullable && rightNullable, "", true
	}
	if _, isAnnotation := schemaAnnotationKeys[key]; isAnnotation {
		return deepCopyJSON(left), "", true
	}
	if _, isSingleValued := schemaSingleValuedConstraintKeys[key]; isSingleValued {
		return deepCopyJSON(left), "", true
	}
	if _, isLowerBound := schemaLowerBoundKeys[key]; isLowerBound {
		merged, mergedOK := intersectNumericBound(left, right, true)
		return merged, "", mergedOK
	}
	if _, isUpperBound := schemaUpperBoundKeys[key]; isUpperBound {
		merged, mergedOK := intersectNumericBound(left, right, false)
		return merged, "", mergedOK
	}
	if !semanticJSONEqual(left, right) {
		return nil, "", false
	}
	return deepCopyJSON(left), "", true
}

// intersectSchemaProperties merges two `properties` maps. Its keys are property
// NAMES, not schema keywords, so a shared name recurses as a whole schema
// instead of going through intersectSchemaKeyword — which would have compared
// two sub-schemas for equality and rejected any property both branches
// constrain differently.
func intersectSchemaProperties(left, right map[string]any) (merged map[string]any, conflictKey string, ok bool) {
	out := make(map[string]any, len(left)+len(right))
	for name, schema := range left {
		out[name] = deepCopyJSON(schema)
	}
	for name, schema := range right {
		current, exists := out[name]
		if !exists {
			out[name] = deepCopyJSON(schema)
			continue
		}
		leftNode, leftOK := current.(map[string]any)
		rightNode, rightOK := schema.(map[string]any)
		if !leftOK || !rightOK {
			if semanticJSONEqual(current, schema) {
				continue
			}
			return nil, name, false
		}
		intersected, childKey, intersectedOK := mergeSchemaMapsExact(leftNode, rightNode)
		if !intersectedOK {
			if childKey != "" {
				return nil, name + "." + childKey, false
			}
			return nil, name, false
		}
		out[name] = intersected
	}
	return out, "", true
}

// intersectSchemaType intersects two `type` values. Branches are sanitized
// before they reach here so their type is a bare string, but the sibling
// schema merged in at the allOf seam is still raw and may carry the
// ["string","null"] union form, so both sides are normalized first.
func intersectSchemaType(left, right any) (value any, ok bool) {
	// Nullability rides on the `nullable` key, reconciled once per merge by
	// mergeSchemaMapsExact, so it is deliberately ignored here: letting both
	// spellings decide would let them disagree.
	leftName, _, leftOK := splitNullableType(left)
	rightName, _, rightOK := splitNullableType(right)
	if !leftOK || !rightOK {
		return nil, false
	}
	name := leftName
	switch {
	case leftName == rightName:
	case leftName == "integer" && rightName == "number",
		leftName == "number" && rightName == "integer":
		// Every integer is a number, so the intersection is the integers.
		name = "integer"
	default:
		// No value is two different JSON types at once.
		return nil, false
	}
	return name, true
}

// splitNullableType reads a `type` value as (name, nullable). It accepts the
// bare string form and the two-element ["<type>","null"] union; anything else
// (a three-way union, a non-string entry) is not representable and reports
// ok=false so the caller rejects rather than guesses.
func splitNullableType(v any) (name string, nullable bool, ok bool) {
	if single, isString := v.(string); isString {
		return single, false, true
	}
	names, isArray := v.([]any)
	if !isArray || len(names) != 2 {
		return "", false, false
	}
	for _, candidate := range names {
		entry, isString := candidate.(string)
		if !isString {
			return "", false, false
		}
		if entry == "null" {
			nullable = true
			continue
		}
		if name != "" {
			return "", false, false
		}
		name = entry
	}
	if name == "" || !nullable {
		return "", false, false
	}
	return name, true, true
}

// intersectSchemaEnum keeps only the values both branches allow. Disjoint
// enums are a genuinely unsatisfiable schema, so that case still rejects.
func intersectSchemaEnum(left, right any) (value any, ok bool) {
	leftValues, leftOK := left.([]any)
	if _, rightOK := right.([]any); !leftOK || !rightOK {
		return nil, false
	}
	out := make([]any, 0, len(leftValues))
	for _, candidate := range leftValues {
		if valueInEnum(candidate, right) && !valueInEnum(candidate, out) {
			out = append(out, deepCopyJSON(candidate))
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// intersectNumericBound keeps the stricter bound: the larger of two lower
// bounds, the smaller of two upper bounds. Non-numeric bounds fall back to the
// equality rule rather than being silently reordered.
func intersectNumericBound(left, right any, keepLarger bool) (value any, ok bool) {
	leftValue, leftOK := left.(float64)
	rightValue, rightOK := right.(float64)
	if !leftOK || !rightOK {
		return deepCopyJSON(left), semanticJSONEqual(left, right)
	}
	if keepLarger == (leftValue > rightValue) {
		return leftValue, true
	}
	return rightValue, true
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

// resolveGeminiEnum drops enum keys Gemini cannot represent. Google types
// every enum member as TYPE_STRING and 400s on anything else. Stringifying
// was rejected: toolcheck validates model output against the ORIGINAL schema,
// so a coerced "7" breaks the caller's `enum: [7]`. Dropping is lossless
// where it counts (#65, #83; #764 regressed this by missing type-less enums).
func resolveGeminiEnum(schema map[string]any) {
	values, exists := schema["enum"]
	if !exists {
		return
	}
	if enum, ok := values.([]any); ok && len(enum) > 0 && allStringEnum(enum) {
		return
	}
	delete(schema, "enum")
	// format:"enum" without an enum is itself unrepresentable.
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
