package translate

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/translate/toolcheck"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var ErrNotJSONObject = errors.New("request body must be a JSON object")

type Format int

const (
	FormatOpenAI Format = iota
	FormatAnthropic
	FormatGemini
	FormatOpenAIResponses
)

type EmitOptions struct {
	TargetModel string

	TargetProvider     string
	Capabilities       router.ModelSpec
	IncludeStreamUsage bool

	SessionAffinity string

	StripPromptCacheKey bool

	ModelSwitched bool

	ForceReasoningEffort string

	ForceEffort string

	EnableExtendedContext bool

	EnableServerSideFallback bool

	KeepCrossVendorOrchestrationTools bool

	KeepCrossVendorTaskTools bool

	AppendAutonomySystem bool

	AppendWorkspaceSystem bool

	StripOutputConfigFormat bool

	DowngradeGeminiValidatedToAuto bool

	ReasoningReplayScope string

	FastMode bool
}

type RequestEnvelope struct {
	body   []byte
	format Format

	threadDiscriminator *string
}

func (e *RequestEnvelope) Clone() *RequestEnvelope {
	if e == nil {
		return nil
	}
	return &RequestEnvelope{body: append([]byte(nil), e.body...), format: e.format, threadDiscriminator: e.threadDiscriminator}
}

func ParseOpenAI(body []byte) (*RequestEnvelope, error) {
	if err := validateJSONObject(body); err != nil {
		return nil, err
	}
	return &RequestEnvelope{body: body, format: FormatOpenAI}, nil
}

func ParseAnthropic(body []byte) (*RequestEnvelope, error) {
	if err := validateJSONObject(body); err != nil {
		return nil, err
	}
	return &RequestEnvelope{body: body, format: FormatAnthropic}, nil
}

func ParseGemini(body []byte) (*RequestEnvelope, error) {
	if err := validateJSONObject(body); err != nil {
		return nil, err
	}
	return &RequestEnvelope{body: body, format: FormatGemini}, nil
}

func validateJSONObject(body []byte) error {
	if !gjson.ValidBytes(body) {
		return fmt.Errorf("%w: invalid JSON", ErrNotJSONObject)
	}
	if !gjson.ParseBytes(body).IsObject() {
		return fmt.Errorf("%w: body is not a JSON object", ErrNotJSONObject)
	}
	return nil
}

func (e *RequestEnvelope) SourceFormat() Format { return e.format }

func (e *RequestEnvelope) Stream() bool {
	r := gjson.GetBytes(e.body, "stream")
	if r.Type == gjson.Number {
		return false
	}
	return r.Bool()
}

func (e *RequestEnvelope) Model() string {
	return gjson.GetBytes(e.body, "model").String()
}

func (e *RequestEnvelope) MetadataUserID() string {
	return gjson.GetBytes(e.body, "metadata.user_id").String()
}

func (e *RequestEnvelope) ClientSessionID() string {
	switch e.format {
	case FormatAnthropic:
		return requestcontext.BodySessionID(e.body, requestcontext.ConversationAnthropic)
	case FormatGemini:
		return requestcontext.BodySessionID(e.body, requestcontext.ConversationGemini)
	case FormatOpenAI:
		return requestcontext.BodySessionID(e.body, requestcontext.ConversationChat)
	case FormatOpenAIResponses:
	}
	return ""
}

func (e *RequestEnvelope) SystemText() string {
	switch e.format {
	case FormatAnthropic:
		return systemTextGJSON(gjson.GetBytes(e.body, "system"))
	case FormatOpenAI:
		return openAISystemText(e.body)
	case FormatGemini:
		return geminiSystemText(e.body)
	default:
		return ""
	}
}

func (e *RequestEnvelope) SystemBlocks() []string {
	if e.format == FormatAnthropic {
		return anthropicSystemTexts(gjson.GetBytes(e.body, "system"))
	}
	if text := e.SystemText(); text != "" {
		return []string{text}
	}
	return nil
}

type LastUserMessageInfo struct {
	HasText         bool
	HasToolResult   bool
	ToolResultCount int
	Text            string

	ToolResultBytes int
}

func (e *RequestEnvelope) LastUserMessage() LastUserMessageInfo {
	switch e.format {
	case FormatAnthropic:
		return anthropicLastUserMessage(e.body)
	case FormatOpenAI:
		return openAILastUserMessage(e.body)
	case FormatGemini:
		return geminiLastUserMessage(e.body)
	default:
		return LastUserMessageInfo{}
	}
}

func (e *RequestEnvelope) FirstUserMessageText() string {
	if e.format == FormatGemini {
		return geminiFirstUserMessageText(e.body)
	}
	first := gjson.GetBytes(e.body, "messages.0")
	if !first.Exists() {
		return ""
	}
	if first.Get("role").String() != "user" {
		return ""
	}
	content := first.Get("content")
	switch e.format {
	case FormatAnthropic:
		return userPromptTextGJSON(content)
	case FormatOpenAI:
		return openAIContentTextGJSON(content)
	default:
		return ""
	}
}

func (e *RequestEnvelope) ThreadDiscriminatorText() string {
	if e.threadDiscriminator != nil {
		return *e.threadDiscriminator
	}
	return e.FirstUserMessageText()
}

func (e *RequestEnvelope) captureThreadDiscriminator() {
	if e.threadDiscriminator != nil {
		return
	}
	text := e.FirstUserMessageText()
	e.threadDiscriminator = &text
}

func (e *RequestEnvelope) HasTools() bool {
	r := gjson.GetBytes(e.body, "tools.#")
	return r.Int() > 0
}

func (e *RequestEnvelope) ToolValidator() *toolcheck.Validator {
	tools := gjson.GetBytes(e.body, "tools")
	if !tools.IsArray() {
		return nil
	}
	switch e.format {
	case FormatAnthropic:
		return toolcheck.CompileCached([]byte(tools.Raw))
	case FormatOpenAI:
		return toolcheck.CompileCached(anthropicToolShapeFromOpenAI(tools))
	default:
		return nil
	}
}

func anthropicToolShapeFromOpenAI(tools gjson.Result) []byte {
	jw := newJSONWriter()
	jw.Arr()
	tools.ForEach(func(_, tool gjson.Result) bool {
		fn := tool.Get("function")
		if !fn.Exists() {
			return true
		}
		jw.Obj()
		jw.Key("name")
		jw.Str(fn.Get("name").String())
		if schema := fn.Get("parameters"); schema.Exists() {
			jw.Key("input_schema")
			jw.Raw(schema.Raw)
		}
		jw.EndObj()
		return true
	})
	jw.EndArr()
	return jw.Bytes()
}

func (e *RequestEnvelope) ImageCount() int {
	switch e.format {
	case FormatAnthropic:
		return anthropicImageCount(e.body)
	case FormatOpenAI:
		return openAIImageCount(e.body)
	case FormatGemini:
		return geminiImageCount(e.body)
	default:
		return 0
	}
}

func (e *RequestEnvelope) HasImages() bool {
	return e.ImageCount() > 0
}

func (e *RequestEnvelope) RequestsTitleSchema() bool {
	return requestRootRequestsTitleSchema(gjson.ParseBytes(e.body), e.format)
}

func requestRootRequestsTitleSchema(root gjson.Result, format Format) bool {
	switch format {
	case FormatAnthropic:
		formatNode := root.Get("output_config.format")
		return formatNode.Get("type").String() == "json_schema" &&
			formatNode.Get("schema.properties.title.type").String() == "string"
	case FormatOpenAI:

		if formatNode := root.Get("response_format"); formatNode.Exists() {
			return formatNode.Get("type").String() == "json_schema" &&
				formatNode.Get("json_schema.schema.properties.title.type").String() == "string"
		}
		formatNode := root.Get("text.format")
		return formatNode.Get("type").String() == "json_schema" &&
			formatNode.Get("schema.properties.title.type").String() == "string"
	default:
		return false
	}
}

func requestRootRequestsCodexTitleSchema(root gjson.Result) bool {
	formatNode := root.Get("text.format")
	if formatNode.Get("type").String() != "json_schema" {
		return false
	}
	schema := formatNode.Get("schema")
	if schema.Get("type").String() != "object" {
		return false
	}
	properties := schema.Get("properties")
	if !properties.IsObject() || len(properties.Map()) != 1 ||
		properties.Get("title.type").String() != "string" {
		return false
	}
	required := schema.Get("required")
	if !required.IsArray() {
		return false
	}
	requiredItems := required.Array()
	if len(requiredItems) != 1 || requiredItems[0].String() != "title" {
		return false
	}
	additionalProperties := schema.Get("additionalProperties")
	return additionalProperties.Exists() && additionalProperties.Type == gjson.False
}

const (
	codexTitlePromptMarker          = "you are generating a short conversation title."
	codexDirectResponsePromptMarker = "respond directly to the user's prompt"
	codexThreadTitleSource          = "thread_title"
)

func requestRootCodexTurnIsThreadTitle(root gjson.Result) bool {
	metadata := root.Get("client_metadata.x-codex-turn-metadata")
	if metadata.Type != gjson.String || !gjson.Valid(metadata.Str) {
		return false
	}
	turn := gjson.Parse(metadata.Str)
	return turn.Get("thread_source").Str == codexThreadTitleSource ||
		turn.Get("turn_trigger").Str == codexThreadTitleSource
}

func requestRootRequestsCodexTitle(root gjson.Result) bool {
	if requestRootRequestsCodexTitleSchema(root) || requestRootCodexTurnIsThreadTitle(root) {
		return true
	}
	text, ok := freshCodexResponsesUserText(root.Get("input"))
	if !ok {
		return false
	}
	lower := strings.ToLower(text)
	return strings.Contains(lower, codexTitlePromptMarker) &&
		strings.Contains(lower, codexDirectResponsePromptMarker)
}

func freshCodexResponsesUserText(input gjson.Result) (string, bool) {
	if input.Type == gjson.String {
		return input.Str, input.Str != ""
	}
	if !input.IsArray() {
		return "", false
	}
	var userText string
	for _, item := range input.Array() {
		itemType := item.Get("type").Str
		if itemType == "" && item.Get("role").Str != "" {
			itemType = "message"
		}
		switch itemType {
		case "additional_tools":
			continue
		case "message":
			role := item.Get("role").Str
			if role == "developer" || role == "system" {
				continue
			}
			if role != "user" {
				return "", false
			}
			if text := contentTextGJSON(item.Get("content")); text != "" {
				userText = text
			}
		default:
			return "", false
		}
	}
	return userText, userText != ""
}

type EmitOverrides struct {
	Model                   string
	DeleteKeys              []string
	SetMaxCompletionTokens  *int64
	ClampMaxTokensKey       string
	ClampMaxTokensValue     int64
	ClampMaxCompTokensValue int64
	DefaultMaxTokensKey     string
	DefaultMaxTokensValue   int64
	InjectStreamUsage       bool
	StripThinkingBlocks     bool

	StripUnsignedThinkingBlocks bool

	StripForeignSignedThinkingBlocks bool

	SanitizeToolUseIDs bool

	StripThoughtSignature bool

	SanitizeAnthropicToolSchemas bool

	RewriteThinkingAdaptive bool
	OutputConfigEffort      string

	FloorMaxTokensValue int64

	ForceOutputConfigEffort string

	StripOutputConfigFormat bool

	ClampEffortXhighTo string

	DowngradeForcedToolChoice bool
}

func (e *RequestEnvelope) emitSameFormat(ov EmitOverrides) ([]byte, error) {
	return applyOverrides(e.body, ov)
}

func applyOverrides(body []byte, ov EmitOverrides) ([]byte, error) {
	var err error
	out := body

	if ov.StripThinkingBlocks {
		out, err = stripThinkingBlocksBytes(out)
		if err != nil {
			return nil, fmt.Errorf("strip thinking blocks: %w", err)
		}
	} else {
		if ov.StripUnsignedThinkingBlocks {
			out, err = stripUnsignedThinkingBlocksBytes(out)
			if err != nil {
				return nil, fmt.Errorf("strip unsigned thinking blocks: %w", err)
			}
		}
		if ov.StripForeignSignedThinkingBlocks {
			out, err = stripForeignSignedThinkingBlocksBytes(out)
			if err != nil {
				return nil, fmt.Errorf("strip foreign-signed thinking blocks: %w", err)
			}
		}
	}

	if ov.StripThoughtSignature {
		out, err = stripThoughtSignatureBytes(out)
		if err != nil {
			return nil, fmt.Errorf("strip thought_signature: %w", err)
		}
	}

	if ov.SanitizeToolUseIDs {
		out, err = sanitizeToolUseIDsBytes(out)
		if err != nil {
			return nil, fmt.Errorf("sanitize tool_use ids: %w", err)
		}
	}

	if ov.SanitizeAnthropicToolSchemas {
		out, err = sanitizeAnthropicToolSchemasBytes(out)
		if err != nil {
			return nil, fmt.Errorf("sanitize anthropic tool schemas: %w", err)
		}
	}

	if ov.Model != "" {
		out, err = sjson.SetBytes(out, "model", ov.Model)
		if err != nil {
			return nil, fmt.Errorf("set model: %w", err)
		}
	}

	if ov.SetMaxCompletionTokens != nil {
		out, err = sjson.SetBytes(out, "max_completion_tokens", *ov.SetMaxCompletionTokens)
		if err != nil {
			return nil, fmt.Errorf("set max_completion_tokens: %w", err)
		}
	}

	if ov.FloorMaxTokensValue > 0 {
		if r := gjson.GetBytes(out, "max_tokens"); r.Type == gjson.Number && r.Int() < ov.FloorMaxTokensValue {
			out, err = sjson.SetBytes(out, "max_tokens", ov.FloorMaxTokensValue)
			if err != nil {
				return nil, fmt.Errorf("floor max_tokens: %w", err)
			}
		}
	}
	if ov.ClampMaxTokensKey != "" && ov.ClampMaxTokensValue > 0 {
		out = clampFieldBytes(out, ov.ClampMaxTokensKey, ov.ClampMaxTokensValue)
	}
	if ov.ClampMaxCompTokensValue > 0 {
		out = clampFieldBytes(out, "max_completion_tokens", ov.ClampMaxCompTokensValue)
	}

	if ov.DefaultMaxTokensKey != "" && ov.DefaultMaxTokensValue > 0 {
		if !gjson.GetBytes(out, ov.DefaultMaxTokensKey).Exists() {
			out, err = sjson.SetBytes(out, ov.DefaultMaxTokensKey, ov.DefaultMaxTokensValue)
			if err != nil {
				return nil, fmt.Errorf("set default %s: %w", ov.DefaultMaxTokensKey, err)
			}
		}
	}

	if ov.InjectStreamUsage {
		if gjson.GetBytes(out, "stream").Bool() {
			out, err = sjson.SetBytes(out, "stream_options.include_usage", true)
			if err != nil {
				return nil, fmt.Errorf("set stream_options.include_usage: %w", err)
			}
		}
	}

	if ov.RewriteThinkingAdaptive {
		out, err = sjson.SetBytes(out, "thinking", map[string]string{"type": "adaptive"})
		if err != nil {
			return nil, fmt.Errorf("rewrite thinking to adaptive: %w", err)
		}
		if ov.OutputConfigEffort != "" {
			if !gjson.GetBytes(out, "output_config.effort").Exists() {
				out, err = sjson.SetBytes(out, "output_config.effort", ov.OutputConfigEffort)
				if err != nil {
					return nil, fmt.Errorf("set output_config.effort: %w", err)
				}
			}
		}
	}

	if ov.ClampEffortXhighTo != "" {
		for _, key := range []string{"effort", "output_config.effort"} {
			if gjson.GetBytes(out, key).String() != effortXhigh {
				continue
			}
			out, err = sjson.SetBytes(out, key, ov.ClampEffortXhighTo)
			if err != nil {
				return nil, fmt.Errorf("clamp %s: %w", key, err)
			}
		}
	}

	if ov.ForceOutputConfigEffort != "" {
		out, err = sjson.SetBytes(out, "output_config.effort", ov.ForceOutputConfigEffort)
		if err != nil {
			return nil, fmt.Errorf("set output_config.effort (forced): %w", err)
		}
		out, err = sjson.DeleteBytes(out, "effort")
		if err != nil {
			return nil, fmt.Errorf("delete effort (forced): %w", err)
		}
	}

	if ov.DowngradeForcedToolChoice {
		out, err = downgradeForcedToolChoiceBytes(out)
		if err != nil {
			return nil, err
		}
	}

	for _, key := range ov.DeleteKeys {
		out, err = sjson.DeleteBytes(out, key)
		if err != nil {
			return nil, fmt.Errorf("delete %s: %w", key, err)
		}
	}

	if ov.StripOutputConfigFormat {
		out, err = stripOutputConfigFormatBytes(out)
		if err != nil {
			return nil, err
		}
	}

	return out, nil
}

func downgradeForcedToolChoiceBytes(body []byte) ([]byte, error) {
	tc := gjson.GetBytes(body, "tool_choice")
	if !tc.IsObject() {
		return body, nil
	}
	switch tc.Get("type").String() {
	case "any", "tool":
	default:
		return body, nil
	}
	out, err := sjson.SetBytes(body, "tool_choice", map[string]string{"type": "auto"})
	if err != nil {
		return nil, fmt.Errorf("downgrade tool_choice: %w", err)
	}
	if dp := tc.Get("disable_parallel_tool_use"); dp.Exists() {
		out, err = sjson.SetRawBytes(out, "tool_choice.disable_parallel_tool_use", []byte(dp.Raw))
		if err != nil {
			return nil, fmt.Errorf("downgrade tool_choice: %w", err)
		}
	}
	return out, nil
}

func stripOutputConfigFormatBytes(body []byte) ([]byte, error) {
	if !gjson.GetBytes(body, "output_config.format").Exists() {
		return body, nil
	}
	out, err := sjson.DeleteBytes(body, "output_config.format")
	if err != nil {
		return nil, fmt.Errorf("delete output_config.format: %w", err)
	}
	if len(gjson.GetBytes(out, "output_config").Map()) > 0 {
		return out, nil
	}
	out, err = sjson.DeleteBytes(out, "output_config")
	if err != nil {
		return nil, fmt.Errorf("delete empty output_config: %w", err)
	}
	return out, nil
}

func clampFieldBytes(body []byte, key string, maxVal int64) []byte {
	r := gjson.GetBytes(body, key)
	if !r.Exists() || r.Type != gjson.Number {
		return body
	}
	if r.Num <= float64(maxVal) {
		return body
	}
	out, err := sjson.SetBytes(body, key, maxVal)
	if err != nil {
		return body
	}
	return out
}

func rewriteMessageBlocks(
	body []byte,
	needsRewrite func(block gjson.Result) bool,
	rewrite func(blockRaw string) (string, error),
) ([]byte, error) {
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body, nil
	}

	anyChanged := false
	droppedMessage := false
	var msgRaws []string
	var walkErr error

	messages.ForEach(func(_, msg gjson.Result) bool {
		content := msg.Get("content")
		if !content.Exists() || !content.IsArray() {
			msgRaws = append(msgRaws, msg.Raw)
			return true
		}

		msgNeedsRewrite := false
		content.ForEach(func(_, block gjson.Result) bool {
			if needsRewrite(block) {
				msgNeedsRewrite = true
				return false
			}
			return true
		})

		if !msgNeedsRewrite {
			msgRaws = append(msgRaws, msg.Raw)
			return true
		}

		anyChanged = true
		var kept []string
		content.ForEach(func(_, block gjson.Result) bool {
			raw := block.Raw
			if needsRewrite(block) {
				var err error
				raw, err = rewrite(raw)
				if err != nil {
					walkErr = err
					return false
				}
			}
			if raw != "" {
				kept = append(kept, raw)
			}
			return true
		})
		if walkErr != nil {
			return false
		}

		if len(kept) == 0 {

			droppedMessage = true
			return true
		}

		newMsg, err := sjson.SetRaw(msg.Raw, "content", "["+strings.Join(kept, ",")+"]")
		if err != nil {
			walkErr = fmt.Errorf("replace content in message: %w", err)
			return false
		}
		msgRaws = append(msgRaws, newMsg)
		return true
	})

	if walkErr != nil {
		return nil, walkErr
	}
	if !anyChanged {
		return body, nil
	}
	if droppedMessage {
		msgRaws, _ = placeAnthropicSystemMessages(msgRaws)
	}

	return sjson.SetRawBytes(body, "messages", []byte("["+strings.Join(msgRaws, ",")+"]"))
}

func stripThinkingBlocksBytes(body []byte) ([]byte, error) {
	return rewriteMessageBlocks(body, isThinkingBlock, dropMatchedBlock)
}

func isThinkingBlock(block gjson.Result) bool {
	blockType := block.Get("type").String()
	return blockType == "thinking" || blockType == "redacted_thinking"
}

func stripUnsignedThinkingBlocksBytes(body []byte) ([]byte, error) {
	return rewriteMessageBlocks(body, isUnsignedThinkingBlock, dropMatchedBlock)
}

func isUnsignedThinkingBlock(block gjson.Result) bool {
	return block.Get("type").String() == "thinking" && block.Get("signature").String() == ""
}

func stripForeignSignedThinkingBlocksBytes(body []byte) ([]byte, error) {
	return rewriteMessageBlocks(body, isForeignSignedThinkingBlock, dropMatchedBlock)
}

func isForeignSignedThinkingBlock(block gjson.Result) bool {
	if block.Get("type").String() != "thinking" {
		return false
	}
	_, ok := decodeOpenAIReasoningSignature(block.Get("signature").String())
	return ok
}

func dropMatchedBlock(string) (string, error) { return "", nil }

func sanitizeToolUseIDsBytes(body []byte) ([]byte, error) {
	return rewriteMessageBlocks(body, blockNeedsToolUseIDSanitize, sanitizeBlockToolUseID)
}

func blockNeedsToolUseIDSanitize(block gjson.Result) bool {
	switch block.Get("type").String() {
	case "tool_use":
		id := block.Get("id").String()
		return anthropicRequestToolUseID(id) != id
	case "tool_result":
		id := block.Get("tool_use_id").String()
		return anthropicRequestToolUseID(id) != id
	default:
		return false
	}
}

func anthropicRequestToolUseID(id string) string {
	id, _ = stripOpenAIReasoningCarrier(id)
	return sanitizeToolUseID(id)
}

func sanitizeBlockToolUseID(raw string) (string, error) {
	block := gjson.Parse(raw)
	switch block.Get("type").String() {
	case "tool_use":
		id := block.Get("id").String()
		out, err := sjson.Set(raw, "id", anthropicRequestToolUseID(id))
		if err != nil {
			return "", fmt.Errorf("rewrite tool_use id: %w", err)
		}
		return out, nil
	case "tool_result":
		id := block.Get("tool_use_id").String()
		out, err := sjson.Set(raw, "tool_use_id", anthropicRequestToolUseID(id))
		if err != nil {
			return "", fmt.Errorf("rewrite tool_use_id: %w", err)
		}
		return out, nil
	default:
		return raw, nil
	}
}

func stripThoughtSignatureBytes(body []byte) ([]byte, error) {
	return rewriteMessageBlocks(body, hasThoughtSignature, deleteThoughtSignature)
}

func hasThoughtSignature(block gjson.Result) bool {
	return block.Get("thought_signature").Exists()
}

func deleteThoughtSignature(raw string) (string, error) {
	out, err := sjson.Delete(raw, "thought_signature")
	if err != nil {
		return "", fmt.Errorf("delete thought_signature: %w", err)
	}
	return out, nil
}

func encodeJSONStringNoHTMLEscape(s string) ([]byte, error) {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return nil, err
	}
	out := buf.String()

	return []byte(strings.TrimSuffix(out, "\n")), nil
}

var routingMarkerPattern = replayedNoticePattern

var feedbackFooterPattern = regexp.MustCompile(`\n*_Weave Router feedback:_ [^\n]*`)

func StripRoutingMarkerFromMessages(body []byte) ([]byte, error) {
	return stripPatternFromMessages(body, routingMarkerPattern)
}

func StripFeedbackFooterFromMessages(body []byte) ([]byte, error) {
	return stripPatternFromMessages(body, feedbackFooterPattern)
}

func stripPatternFromMessages(body []byte, pattern *regexp.Regexp) ([]byte, error) {
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body, nil
	}

	anyChanged := false
	var msgRaws []string
	var walkErr error

	messages.ForEach(func(_, msg gjson.Result) bool {
		content := msg.Get("content")
		if !content.Exists() {
			msgRaws = append(msgRaws, msg.Raw)
			return true
		}

		if content.Type == gjson.String {
			text := content.String()
			if !pattern.MatchString(text) {
				msgRaws = append(msgRaws, msg.Raw)
				return true
			}
			stripped := pattern.ReplaceAllString(text, "")
			anyChanged = true
			encoded, err := encodeJSONStringNoHTMLEscape(stripped)
			if err != nil {
				walkErr = fmt.Errorf("marshal stripped string content: %w", err)
				return false
			}
			newMsg, err := sjson.SetRawBytes([]byte(msg.Raw), "content", encoded)
			if err != nil {
				walkErr = fmt.Errorf("replace string content in message: %w", err)
				return false
			}
			msgRaws = append(msgRaws, string(newMsg))
			return true
		}

		if !content.IsArray() {
			msgRaws = append(msgRaws, msg.Raw)
			return true
		}

		var newBlocks []string
		msgChanged := false
		content.ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() != "text" {
				newBlocks = append(newBlocks, block.Raw)
				return true
			}
			text := block.Get("text").String()
			if !pattern.MatchString(text) {
				newBlocks = append(newBlocks, block.Raw)
				return true
			}
			stripped := pattern.ReplaceAllString(text, "")
			msgChanged = true
			if strings.TrimSpace(stripped) == "" {
				return true
			}
			encoded, err := encodeJSONStringNoHTMLEscape(stripped)
			if err != nil {
				walkErr = fmt.Errorf("marshal stripped text: %w", err)
				return false
			}
			newBlock, err := sjson.SetRawBytes([]byte(block.Raw), "text", encoded)
			if err != nil {
				walkErr = fmt.Errorf("replace text in block: %w", err)
				return false
			}
			newBlocks = append(newBlocks, string(newBlock))
			return true
		})
		if walkErr != nil {
			return false
		}
		if !msgChanged {
			msgRaws = append(msgRaws, msg.Raw)
			return true
		}

		anyChanged = true
		newContent := "[" + strings.Join(newBlocks, ",") + "]"
		newMsg, err := sjson.SetRawBytes([]byte(msg.Raw), "content", []byte(newContent))
		if err != nil {
			walkErr = fmt.Errorf("replace content in message: %w", err)
			return false
		}
		msgRaws = append(msgRaws, string(newMsg))
		return true
	})

	if walkErr != nil {
		return nil, walkErr
	}
	if !anyChanged {
		return body, nil
	}

	newMessagesArray := "[" + strings.Join(msgRaws, ",") + "]"
	return sjson.SetRawBytes(body, "messages", []byte(newMessagesArray))
}

func StripFeedbackFooterFromGeminiContents(body []byte) ([]byte, error) {
	return stripPatternFromGeminiContents(body, feedbackFooterPattern)
}

func StripRoutingMarkerFromGeminiContents(body []byte) ([]byte, error) {
	return stripPatternFromGeminiContents(body, routingMarkerPattern)
}

func stripPatternFromGeminiContents(body []byte, pattern *regexp.Regexp) ([]byte, error) {
	contents := gjson.GetBytes(body, "contents")
	if !contents.Exists() || !contents.IsArray() {
		return body, nil
	}

	anyChanged := false
	var contentRaws []string
	var walkErr error

	contents.ForEach(func(_, content gjson.Result) bool {
		parts := content.Get("parts")
		if !parts.Exists() || !parts.IsArray() {
			contentRaws = append(contentRaws, content.Raw)
			return true
		}

		var newParts []string
		contentChanged := false
		parts.ForEach(func(_, part gjson.Result) bool {
			textNode := part.Get("text")
			if !textNode.Exists() {
				newParts = append(newParts, part.Raw)
				return true
			}
			text := textNode.String()
			if !pattern.MatchString(text) {
				newParts = append(newParts, part.Raw)
				return true
			}
			stripped := pattern.ReplaceAllString(text, "")
			contentChanged = true
			if strings.TrimSpace(stripped) == "" {
				return true
			}
			encoded, err := encodeJSONStringNoHTMLEscape(stripped)
			if err != nil {
				walkErr = fmt.Errorf("marshal stripped gemini text: %w", err)
				return false
			}
			newPart, err := sjson.SetRawBytes([]byte(part.Raw), "text", encoded)
			if err != nil {
				walkErr = fmt.Errorf("replace text in gemini part: %w", err)
				return false
			}
			newParts = append(newParts, string(newPart))
			return true
		})
		if walkErr != nil {
			return false
		}
		if !contentChanged {
			contentRaws = append(contentRaws, content.Raw)
			return true
		}

		anyChanged = true
		newPartsArray := "[" + strings.Join(newParts, ",") + "]"
		newContent, err := sjson.SetRawBytes([]byte(content.Raw), "parts", []byte(newPartsArray))
		if err != nil {
			walkErr = fmt.Errorf("replace parts in gemini content: %w", err)
			return false
		}
		contentRaws = append(contentRaws, string(newContent))
		return true
	})

	if walkErr != nil {
		return nil, walkErr
	}
	if !anyChanged {
		return body, nil
	}
	return sjson.SetRawBytes(body, "contents", []byte("["+strings.Join(contentRaws, ",")+"]"))
}

func resolveOpenAIOverrides(body []byte, opts EmitOptions) EmitOverrides {
	ov := EmitOverrides{
		Model: opts.TargetModel,
	}

	ov.DeleteKeys = append(ov.DeleteKeys, "thinking")

	if gjson.GetBytes(body, "reasoning_effort").Exists() &&
		(!opts.Capabilities.Supports(router.CapReasoning) ||
			!reasoningEffortAcceptedOnChatCompletions(opts, hasNonEmptyTools(body))) {
		ov.DeleteKeys = append(ov.DeleteKeys, "reasoning_effort")
	}

	supportsReasoning := opts.Capabilities.Supports(router.CapReasoning)

	if supportsReasoning {
		for _, key := range []string{"stop", "presence_penalty", "frequency_penalty"} {
			if gjson.GetBytes(body, key).Exists() {
				ov.DeleteKeys = append(ov.DeleteKeys, key)
			}
		}
	}

	if !samplersAccepted(opts) {
		for _, key := range []string{"temperature", "top_p"} {
			if gjson.GetBytes(body, key).Exists() {
				ov.DeleteKeys = append(ov.DeleteKeys, key)
			}
		}
	}

	maxTokens := gjson.GetBytes(body, "max_tokens")
	hasMaxTokens := maxTokens.Type == gjson.Number
	hasMaxComp := gjson.GetBytes(body, "max_completion_tokens").Type == gjson.Number

	if maxTokens.Exists() && supportsReasoning {
		if hasMaxTokens && !hasMaxComp {
			val := maxTokens.Int()
			ov.SetMaxCompletionTokens = &val
		}
		ov.DeleteKeys = append(ov.DeleteKeys, "max_tokens")
	}

	tokenCap := modelMaxOutputTokens[opts.TargetModel]
	if tokenCap == 0 {
		tokenCap = defaultMaxOutputTokenCap
	}
	maxTokensDeleted := hasMaxTokens && supportsReasoning
	if hasMaxTokens && !maxTokensDeleted {
		ov.ClampMaxTokensKey = "max_tokens"
		ov.ClampMaxTokensValue = int64(tokenCap)
	}
	if hasMaxComp || ov.SetMaxCompletionTokens != nil {
		ov.ClampMaxCompTokensValue = int64(tokenCap)
	}

	if !hasMaxTokens && !hasMaxComp {
		if supportsReasoning {
			ov.DefaultMaxTokensKey = "max_completion_tokens"
		} else {
			ov.DefaultMaxTokensKey = "max_tokens"
		}
		ov.DefaultMaxTokensValue = defaultOutputTokens(opts.TargetModel)
	}

	if opts.IncludeStreamUsage {
		ov.InjectStreamUsage = true
	}

	return ov
}

const (
	effortLow    = "low"
	effortMedium = "medium"
	effortHigh   = "high"
	effortMax    = "max"
	effortXhigh  = "xhigh"
)

func ResolveForceEffort(caps router.ModelSpec, level string) string {
	if level == "" {
		return ""
	}
	canonical := router.CanonicalizeEffort(level)
	switch canonical {
	case effortXhigh:
		if !caps.Supports(router.CapXhighEffort) {
			return effortMax
		}
	case effortMax:

		if caps.Supports(router.CapXhighEffort) && !containsReasoningLevel(caps.Reasoning().Levels, effortMax) {
			return effortXhigh
		}
	}
	return canonical
}

func resolveForceEffort(opts EmitOptions) string {
	return ResolveForceEffort(opts.Capabilities, opts.ForceEffort)
}

func resolveAnthropicOverrides(body []byte, opts EmitOptions) EmitOverrides {
	ov := EmitOverrides{
		Model:                        opts.TargetModel,
		SanitizeToolUseIDs:           true,
		StripThoughtSignature:        true,
		SanitizeAnthropicToolSchemas: true,
	}
	if opts.Capabilities.Supports(router.CapSamplingParamsUnsupported) {
		for _, key := range anthropicSamplingParams {
			if gjson.GetBytes(body, key).Exists() {
				ov.DeleteKeys = append(ov.DeleteKeys, key)
			}
		}
	}

	if forced := resolveForceEffort(opts); forced != "" && opts.Capabilities.Supports(router.CapAdaptiveThinking) {
		ov.RewriteThinkingAdaptive = true
		ov.OutputConfigEffort = forced
		ov.ForceOutputConfigEffort = forced
	}

	thinkingResult := gjson.GetBytes(body, "thinking")
	if thinkingResult.Exists() {
		thinkingType := thinkingResult.Get("type").String()
		shouldDelete := false
		switch thinkingType {
		case "adaptive":
			shouldDelete = !opts.Capabilities.Supports(router.CapAdaptiveThinking)
		case "enabled":
			if opts.Capabilities.Supports(router.CapExtendedThinking) {

			} else if opts.Capabilities.Supports(router.CapAdaptiveThinking) {
				ov.RewriteThinkingAdaptive = true
				ov.OutputConfigEffort = effortForBudget(thinkingResult.Get("budget_tokens").Int())
			} else {
				shouldDelete = true
			}
		case "disabled":
			if opts.Capabilities.Supports(router.CapExtendedThinking) {

			} else if opts.Capabilities.Supports(router.CapAdaptiveThinking) &&
				!opts.Capabilities.Reasoning().AlwaysOn {

			} else if opts.Capabilities.Supports(router.CapAdaptiveThinking) {

				ov.RewriteThinkingAdaptive = true
				ov.OutputConfigEffort = effortLow
				ov.FloorMaxTokensValue = minReasoningOutputTokens
				if gjson.GetBytes(body, "temperature").Exists() && !slices.Contains(ov.DeleteKeys, "temperature") {
					ov.DeleteKeys = append(ov.DeleteKeys, "temperature")
				}
			} else {
				shouldDelete = true
			}
		default:
			shouldDelete = !opts.Capabilities.Supports(router.CapAdaptiveThinking) && !opts.Capabilities.Supports(router.CapExtendedThinking)
		}
		if shouldDelete {
			ov.DeleteKeys = append(ov.DeleteKeys, "thinking")
		}
	}

	if !opts.Capabilities.Supports(router.CapAdaptiveThinking) {
		for _, key := range []string{"context_management", "effort", "output_config"} {
			if gjson.GetBytes(body, key).Exists() {
				ov.DeleteKeys = append(ov.DeleteKeys, key)
			}
		}
	}

	ov.StripOutputConfigFormat = opts.StripOutputConfigFormat

	if opts.Capabilities.Supports(router.CapAdaptiveThinking) && !opts.Capabilities.Supports(router.CapXhighEffort) {
		ov.ClampEffortXhighTo = effortMax
	}

	if !opts.Capabilities.Supports(router.CapAdaptiveThinking) && !opts.Capabilities.Supports(router.CapExtendedThinking) {
		ov.StripThinkingBlocks = true
	}

	if opts.ModelSwitched {
		ov.StripThinkingBlocks = true
	}

	ov.DowngradeForcedToolChoice = opts.Capabilities.Supports(router.CapAutoToolChoiceOnly)

	ov.StripUnsignedThinkingBlocks = true
	ov.StripForeignSignedThinkingBlocks = true

	if !gjson.GetBytes(body, "max_tokens").Exists() {
		ov.DefaultMaxTokensKey = "max_tokens"
		ov.DefaultMaxTokensValue = defaultAnthropicOutputTokens(opts.TargetModel, opts.Capabilities)
	}

	return ov
}

func resolvePassthroughOverrides(body []byte) (EmitOverrides, bool) {
	var deleteKeys []string
	for _, key := range []string{"effort", "thinking", "context_management", "output_config"} {
		if gjson.GetBytes(body, key).Exists() {
			deleteKeys = append(deleteKeys, key)
		}
	}
	if len(deleteKeys) == 0 {
		return EmitOverrides{}, false
	}
	return EmitOverrides{
		Model:      gjson.GetBytes(body, "model").String(),
		DeleteKeys: deleteKeys,
	}, true
}

var modelMaxOutputTokens = map[string]int{
	catalog.ModelIDClaudeHaiku45.String(): 64000,
	"claude-haiku-5-5":                    128000,
	"gpt-4.1":                             32768, "gpt-4.1-mini": 32768, "gpt-4.1-nano": 32768,
	"gpt-4o": 16384, "gpt-4o-mini": 16384,
	"gpt-4-turbo": 4096, "gpt-4": 8192,
	"gpt-5": 128000, "gpt-5-chat": 128000, "gpt-5-pro": 128000,
	"gpt-5-mini": 128000, "gpt-5-nano": 128000,
	"gpt-5.1": 128000, "gpt-5.2": 128000, "gpt-5.2-pro": 128000,
	"gpt-5.3": 128000, "gpt-5.4": 128000, "gpt-5.4-pro": 128000,
	"gpt-5.4-mini": 128000, "gpt-5.4-nano": 128000,
	"gpt-5.5": 128000, "gpt-5.5-pro": 128000, "gpt-5.5-mini": 128000,
	"gpt-5.5-nano": 128000,
	"gpt-5.6-sol":  128000, "gpt-5.6-sol-pro": 128000,
	"gpt-5.6-terra": 128000, "gpt-5.6-luna": 128000, "gpt-5.6-luna-pro": 128000,
	"gpt-6-astra": 128000, "gpt-6-sol": 128000, "gpt-6.1-sol": 128000, "gpt-6.1-terra": 128000, "gpt-6-luna": 128000, "gpt-6.2-sol": 128000, "gpt-6.2-astra": 128000,
	"grok-4.5": 131072, "grok-4.6": 131072, "grok-4.7": 131072,
	"muse-spark-1.3": 131072,
	"muse-ember-1.0": 131072, "muse-wonder-1.0": 131072,
	"o1": 100000, "o1-pro": 100000, "o1-mini": 65536,
	"o3": 100000, "o3-pro": 100000, "o3-mini": 100000,
	"o4-mini":              100000,
	"gemini-3-pro-preview": 65536, "gemini-3.1-pro-preview": 65536,
	"gemini-3-flash-preview": 65536, "gemini-3.1-flash-lite-preview": 65536,
	"gemini-3.1-flash-live-preview": 65536,
	"gemini-3.5-flash-lite":         65536, "gemini-3.6-flash": 65536,
	"gemini-3.7-flash": 65536, "gemini-3.8-flash": 65536,
	router.ModelGemini4Argon: 32768, router.ModelGemini4Neon: 65536, router.ModelGemini4Carbon: 32768, router.ModelGemini4Hydrogen: 65536,
	"gemini-2.5-pro": 65536, "gemini-2.5-flash": 65536,
	"gemini-2.5-flash-lite": 65536,
	"gemini-2.0-flash":      8192, "gemini-2.0-flash-lite": 8192,

	"moonshotai/kimi-k3":               131072,
	"moonshotai/kimi-k3.1":             131072,
	"qwen/qwen3.8-max":                 64000,
	"qwen/qwen3-coder-next":            16384,
	"qwen/qwen3-235b-a22b-2507":        16384,
	"qwen/qwen3-next-80b-a3b-instruct": 16384,
	"deepseek/deepseek-v4-flash":       131072,
	"deepseek/deepseek-v4.1-flash":     131072,
	"deepseek/deepseek-v4-pro":         131072,
	"deepseek/deepseek-v4-pro-0813":    131072,
	"minimax/minimax-m3.1-flash":       131072,
	catalog.ModelMuseSparkOpen:         131072,
	"minimax/minimax-m3":               131072,
	"minimax/minimax-m2.7":             65536,
	"z-ai/glm-5":                       65536,
	"z-ai/glm-5.1":                     65536,
	"z-ai/glm-5.3":                     131072,
	"z-ai/glm-5.3-flash":               131072,
	"google/gemini-3.7-flash":          65536,
	"google/gemini-3.8-flash":          65536,
}

const defaultMaxOutputTokenCap = 8192

func defaultOutputTokens(model string) int64 {
	if tokenCap, ok := modelMaxOutputTokens[model]; ok && tokenCap < defaultMaxOutputTokenCap {
		return int64(tokenCap)
	}
	return defaultMaxOutputTokenCap
}

func defaultAnthropicOutputTokens(model string, capabilities router.ModelSpec) int64 {
	if !capabilities.Supports(router.CapAdaptiveThinking) {
		catalogCapabilities := router.Lookup(model)
		if catalogCapabilities.Supports(router.CapAdaptiveThinking) {
			capabilities = catalogCapabilities
		}
	}
	defaultTokens := defaultOutputTokens(model)
	if !capabilities.Supports(router.CapAdaptiveThinking) || !capabilities.Reasoning().AlwaysOn {
		return defaultTokens
	}
	floored := int64(anthropicAdaptiveDefaultOutputTokens)
	if tokenCap, ok := modelMaxOutputTokens[model]; ok && int64(tokenCap) < floored {
		return int64(tokenCap)
	}
	return floored
}

func (e *RequestEnvelope) setMessages(rebuilt []string, ret int) int {
	out, err := sjson.SetRawBytes(e.body, "messages", []byte("["+strings.Join(rebuilt, ",")+"]"))
	if err != nil {
		return 0
	}
	e.body = out
	return ret
}
