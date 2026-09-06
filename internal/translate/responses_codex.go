package translate

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"weave-os/router/internal/router"

	"github.com/tidwall/gjson"
)

const (
	responsesDefaultToolNamespace = "functions"
	responsesMaxToolAliasBytes    = 64
)

// ResponsesConversionOptions selects opt-in Responses ingress projections.
// The zero value preserves ConvertResponsesToChatCompletions behavior.
type ResponsesConversionOptions struct {
	PortableCodex bool
}

// ConvertResponsesToChatCompletionsWithOptions projects a Responses request
// into Chat Completions using the explicitly requested compatibility mode.
func ConvertResponsesToChatCompletionsWithOptions(body []byte, options ResponsesConversionOptions) (ResponsesConversion, error) {
	if !options.PortableCodex {
		return ConvertResponsesToChatCompletions(body)
	}
	return convertPortableCodexResponses(body)
}

type responsesToolIdentity struct {
	name      string
	namespace string
	custom    bool
}

type portableCodexResponsesConverter struct {
	result          ResponsesConversion
	toolAliases     map[responsesToolIdentity]string
	declaredAliases map[string]struct{}
	tools           []map[string]any
}

func codexFeedbackSkillInvocation(input gjson.Result) bool {
	if !input.IsArray() {
		return false
	}
	awaitingToolOutput := false
	matched := false
	for _, item := range input.Array() {
		itemType := item.Get("type").Str
		if itemType == "message" && item.Get("role").Str == "user" {
			content := item.Get("content")
			if codexFeedbackCommandContent(content) {
				awaitingToolOutput = true
				matched = false
			} else if !codexFeedbackSkillInstructions(content) {
				awaitingToolOutput = false
				matched = false
			}
			continue
		}
		if itemType != "function_call_output" && itemType != "custom_tool_call_output" {
			continue
		}
		if awaitingToolOutput && codexFeedbackToolOutput(item.Get("output")) {
			matched = true
			awaitingToolOutput = false
		}
	}
	return matched
}

func codexFeedbackToolOutput(output gjson.Result) bool {
	if output.Type == gjson.String {
		return routerFeedbackCommandFound(output.Str)
	}
	if !output.IsArray() {
		return false
	}
	var text strings.Builder
	for _, part := range output.Array() {
		if part.Get("type").Str == "input_text" {
			text.WriteString(part.Get("text").Str)
		}
	}
	return routerFeedbackCommandFound(text.String())
}

func codexFeedbackSkillInstructions(content gjson.Result) bool {
	if content.Type == gjson.String {
		return strings.Contains(content.Str, "<skill>") && strings.Contains(content.Str, "</skill>")
	}
	if !content.IsArray() {
		return false
	}
	for _, part := range content.Array() {
		if part.Get("type").Str == "input_text" && codexFeedbackSkillInstructions(part.Get("text")) {
			return true
		}
	}
	return false
}

func codexFeedbackCommandContent(content gjson.Result) bool {
	if content.Type == gjson.String {
		return codexFeedbackCommandText(content.Str)
	}
	if !content.IsArray() {
		return false
	}
	for _, part := range content.Array() {
		if part.Get("type").Str == "input_text" && codexFeedbackCommandText(part.Get("text").Str) {
			return true
		}
	}
	return false
}

func codexFeedbackCommandText(text string) bool {
	first, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	if !strings.HasPrefix(first, "$") {
		return false
	}
	_, _, found := matchRouterFeedbackCommand(first)
	return found
}

func routerFeedbackCommandFound(text string) bool {
	_, found, _ := parseRouterFeedbackCommand(text)
	return found
}

func convertPortableCodexResponses(body []byte) (ResponsesConversion, error) {
	if err := validateResponsesRequest(body); err != nil {
		return ResponsesConversion{}, err
	}

	root := gjson.ParseBytes(body)
	converter := portableCodexResponsesConverter{
		result: ResponsesConversion{
			OriginalBody: body,
			Requirements: router.TranslationRequirements{
				SourceFormat: router.WireFormatOpenAI,
				Endpoint:     router.EndpointOpenAIResponses,
			},
			ToolMappings: make(map[string]ResponsesToolMapping),
		},
		toolAliases:     make(map[responsesToolIdentity]string),
		declaredAliases: make(map[string]struct{}),
	}
	converter.result.TitleGeneration = requestRootRequestsCodexTitleSchema(root)
	converter.result.Requirements.Images = root.Get("input").Exists() && containsAnyKey(body, "image_url", "input_image")
	converter.result.Requirements.Audio, converter.result.Requirements.Files = openAIMediaRequirements(body)
	converter.result.Requirements.CitationsOrSearch = len(nativeServerToolsFromBody(body, FormatOpenAI)) > 0
	converter.result.Requirements.StructuredOutput = root.Get("text.format").Exists() || root.Get("response_format").Exists()

	out := make(map[string]any)
	if model := root.Get("model").Str; model != "" {
		converter.result.Model = model
		out["model"] = model
	}
	converter.result.Stream = root.Get("stream").Bool()
	converter.result.Requirements.UsageDetail = converter.result.Stream && root.Get("stream_options.include_usage").Bool()
	out["stream"] = converter.result.Stream
	if converter.result.Stream {
		out["stream_options"] = map[string]any{"include_usage": true}
	}

	converter.collectDeclaredTools(root)
	converter.result.CodexFeedbackSkill = codexFeedbackSkillInvocation(root.Get("input"))
	messages := converter.convertInput(root.Get("input"))
	var systemMessages []map[string]any
	if instructions := root.Get("instructions").Str; instructions != "" {
		systemMessages = append(systemMessages, map[string]any{"role": "system", "content": instructions})
	}
	if verbosity := root.Get("text.verbosity"); verbosity.Exists() {
		if instruction, ok := converter.convertTextVerbosity(verbosity); ok {
			systemMessages = append(systemMessages, map[string]any{"role": "system", "content": instruction})
		}
	}
	if len(systemMessages) > 0 {
		messages = append(systemMessages, messages...)
	}
	out["messages"] = messages
	if len(converter.tools) > 0 {
		out["tools"] = converter.tools
	}
	converter.copyRequestControls(root, out)

	bodyOut, err := json.Marshal(out)
	if err != nil {
		return ResponsesConversion{}, fmt.Errorf("marshal portable Codex chat completions: %w", err)
	}
	converter.result.Body = bodyOut
	if len(converter.result.ToolMappings) == 0 {
		converter.result.ToolMappings = nil
	}
	return converter.result, nil
}

func (c *portableCodexResponsesConverter) collectDeclaredTools(root gjson.Result) {
	if tools := root.Get("tools"); tools.IsArray() {
		c.collectToolArray(tools, "", "tools")
	}
	input := root.Get("input")
	if !input.IsArray() {
		return
	}
	for index, item := range input.Array() {
		if item.Get("type").Str != "additional_tools" {
			continue
		}
		tools := item.Get("tools")
		path := "input." + strconv.Itoa(index) + ".tools"
		if !tools.IsArray() {
			c.markNativeOnly("responses_additional_tools_native_only", path)
			continue
		}
		c.collectToolArray(tools, "", path)
	}
}

func (c *portableCodexResponsesConverter) collectToolArray(tools gjson.Result, namespace, path string) {
	for index, tool := range tools.Array() {
		toolPath := path + "." + strconv.Itoa(index)
		switch tool.Get("type").Str {
		case "namespace":
			name := normalizeResponsesToolNamespace(tool.Get("name").Str)
			children := tool.Get("tools")
			if tool.Get("name").Str == "" || !children.IsArray() {
				c.markNativeOnly("responses_namespace_tool_native_only", toolPath)
				continue
			}
			c.collectToolArray(children, name, toolPath+".tools")
		case "function":
			c.collectFunctionTool(tool, namespace, toolPath)
		case "custom":
			c.collectCustomTool(tool, namespace, toolPath)
		default:
			c.result.Requirements.CustomTools = true
			c.markNativeOnly("responses_unknown_tool_native_only", toolPath)
		}
	}
}

func (c *portableCodexResponsesConverter) collectFunctionTool(tool gjson.Result, namespace, path string) {
	name := tool.Get("name").Str
	if name == "" {
		name = tool.Get("function.name").Str
	}
	if name == "" {
		c.markNativeOnly("responses_function_tool_native_only", path)
		return
	}
	c.result.Requirements.FunctionTools = true
	alias := c.ensureToolMapping(name, namespace, false)
	if _, duplicate := c.declaredAliases[alias]; duplicate {
		return
	}
	c.declaredAliases[alias] = struct{}{}

	function := map[string]any{"name": alias}
	if description := firstNonEmpty(tool.Get("description").Str, tool.Get("function.description").Str); description != "" {
		function["description"] = description
	}
	if parameters := firstExisting(tool.Get("parameters"), tool.Get("function.parameters")); parameters.Exists() {
		var schema any
		if !parameters.IsObject() || json.Unmarshal([]byte(parameters.Raw), &schema) != nil {
			c.markNativeOnly("responses_function_schema_native_only", path+".parameters")
			return
		}
		hadDefinitions := schemaContainsDefinitionsOrRefs(schema)
		schema = inlineSchemaDefs(schema)
		if schemaContainsDefinitionsOrRefs(schema) {
			// Unresolvable refs can't be made self-contained; keep native.
			c.markNativeOnly("responses_function_schema_native_only", path+".parameters")
			return
		}
		function["parameters"] = schema
		if hadDefinitions {
			c.report("responses_function_schema_inlined", "inlined", path+".parameters")
		}
	}
	if strict := firstExisting(tool.Get("strict"), tool.Get("function.strict")); strict.Exists() {
		function["strict"] = strict.Bool()
	}
	c.tools = append(c.tools, map[string]any{"type": "function", "function": function})
	if namespace != "" {
		c.report("responses_namespace_tool_flattened", "flattened", path)
	}
}

func (c *portableCodexResponsesConverter) collectCustomTool(tool gjson.Result, namespace, path string) {
	name := tool.Get("name").Str
	if name == "" {
		c.markNativeOnly("responses_custom_tool_native_only", path)
		return
	}
	c.result.Requirements.CustomTools = true
	c.result.Requirements.FunctionTools = true
	alias := c.ensureToolMapping(name, namespace, true)
	if _, duplicate := c.declaredAliases[alias]; duplicate {
		return
	}
	c.declaredAliases[alias] = struct{}{}

	description := tool.Get("description").Str
	if format := portableCustomToolFormatDescription(tool.Get("format")); format != "" {
		if description != "" {
			description += "\n\n"
		}
		description += format
	}
	function := map[string]any{
		"name":        alias,
		"description": description,
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"input": map[string]any{"type": "string", "description": "Raw custom-tool input."},
			},
			"required":             []string{"input"},
			"additionalProperties": false,
		},
		"strict": true,
	}
	c.tools = append(c.tools, map[string]any{"type": "function", "function": function})
	c.report("responses_custom_tool_projected", "projected", path)
}

func (c *portableCodexResponsesConverter) convertInput(input gjson.Result) []map[string]any {
	messages := make([]map[string]any, 0, 8)
	switch {
	case input.Type == gjson.String:
		return append(messages, map[string]any{"role": "user", "content": input.Str})
	case !input.IsArray():
		return messages
	}

	for index, item := range input.Array() {
		path := "input." + strconv.Itoa(index)
		itemType := item.Get("type").Str
		if itemType == "" && item.Get("role").Str != "" {
			itemType = "message"
		}
		switch itemType {
		case "additional_tools":
			continue
		case "message":
			if message, ok := c.convertMessage(item, path); ok {
				messages = append(messages, message)
			}
		case "agent_message":
			if message, ok := c.convertAgentMessage(item, path); ok {
				messages = append(messages, message)
			}
		case "reasoning":
			c.convertReasoning(item, path)
		case "function_call":
			c.appendToolCall(&messages, item, path, false)
		case "custom_tool_call":
			c.appendToolCall(&messages, item, path, true)
		case "function_call_output", "custom_tool_call_output":
			if message, ok := c.convertToolOutput(item, path); ok {
				messages = append(messages, message)
			}
		default:
			c.markNativeOnly("responses_unknown_input_native_only", path)
		}
	}
	return messages
}

func (c *portableCodexResponsesConverter) convertReasoning(item gjson.Result, path string) {
	summary := item.Get("summary")
	if summary.Exists() && (!summary.IsArray() || len(summary.Array()) > 0) {
		c.result.Requirements.ReasoningReplay = true
		c.markNativeOnly("responses_reasoning_summary_native_only", path+".summary")
		return
	}
	content := item.Get("content")
	if content.Exists() && (!content.IsArray() || len(content.Array()) > 0) {
		c.result.Requirements.ReasoningReplay = true
		c.markNativeOnly("responses_reasoning_content_native_only", path+".content")
		return
	}
	unknown := false
	item.ForEach(func(key, _ gjson.Result) bool {
		switch key.Str {
		case "type", "id", "summary", "content", "encrypted_content", "status", "internal_chat_message_metadata_passthrough":
		default:
			unknown = true
		}
		return true
	})
	if unknown {
		c.result.Requirements.ReasoningReplay = true
		c.markNativeOnly("responses_reasoning_unknown_native_only", path)
		return
	}
	if encrypted := item.Get("encrypted_content"); encrypted.Type != gjson.String || encrypted.Str == "" {
		c.result.Requirements.ReasoningReplay = true
		c.markNativeOnly("responses_reasoning_replay_native_only", path)
		return
	}
	c.report("responses_encrypted_reasoning_dropped", "dropped", path)
}

func (c *portableCodexResponsesConverter) convertMessage(item gjson.Result, path string) (map[string]any, bool) {
	role := item.Get("role").Str
	if role == "" {
		role = "user"
	}
	if phase := item.Get("phase"); phase.Exists() {
		if role != "assistant" || phase.Type != gjson.String || (phase.Str != "commentary" && phase.Str != "final_answer") {
			c.markNativeOnly("responses_message_phase_native_only", path+".phase")
		} else {
			// Chat Completions has no equivalent field. The phase only classifies
			// already-visible assistant text for Codex's presentation layer.
			c.report("responses_message_phase_dropped", "dropped", path+".phase")
		}
	}
	if role == "developer" {
		// Responses Lite sends base instructions as developer-role messages;
		// project to system for Chat-only provider compatibility.
		role = "system"
		c.report("responses_developer_message_projected", "projected", path+".role")
	}
	if role != "user" && role != "assistant" && role != "system" {
		c.markNativeOnly("responses_message_role_native_only", path+".role")
		return nil, false
	}
	content, ok := c.convertMessageContent(item.Get("content"), role, path+".content")
	if !ok {
		return nil, false
	}
	return map[string]any{"role": role, "content": content}, true
}

func (c *portableCodexResponsesConverter) convertMessageContent(content gjson.Result, role, path string) (any, bool) {
	if content.Type == gjson.String {
		text := content.Str
		if role == "assistant" {
			text = codexResponsesBadgePattern.ReplaceAllString(text, "")
			text = feedbackFooterPattern.ReplaceAllString(text, "")
		}
		if text == "" {
			// Router-owned text can be the only content after ingress markers are
			// stripped. Omitting the shell prevents empty Anthropic text blocks.
			return nil, false
		}
		return text, true
	}
	if !content.IsArray() {
		c.markNativeOnly("responses_message_content_native_only", path)
		return nil, false
	}

	parts := make([]map[string]any, 0, len(content.Array()))
	firstAssistantText := true
	for index, part := range content.Array() {
		partPath := path + "." + strconv.Itoa(index)
		switch part.Get("type").Str {
		case "input_text", "output_text", "text":
			text := part.Get("text").Str
			if role == "assistant" && firstAssistantText {
				text = codexResponsesBadgePattern.ReplaceAllString(text, "")
				firstAssistantText = false
			}
			if role == "assistant" {
				text = feedbackFooterPattern.ReplaceAllString(text, "")
			}
			if text == "" {
				continue
			}
			parts = append(parts, map[string]any{"type": "text", "text": text})
		case "refusal":
			if text := part.Get("refusal").Str; text != "" {
				parts = append(parts, map[string]any{"type": "text", "text": text})
			}
		default:
			c.markNativeOnly("responses_message_content_native_only", partPath)
			return nil, false
		}
	}
	if len(parts) == 0 {
		return nil, false
	}
	return parts, true
}

func (c *portableCodexResponsesConverter) convertAgentMessage(item gjson.Result, path string) (map[string]any, bool) {
	content := item.Get("content")
	if !content.IsArray() {
		c.markNativeOnly("responses_agent_message_native_only", path)
		return nil, false
	}
	textParts := make([]string, 0, len(content.Array()))
	hasPlainText := false
	for index, part := range content.Array() {
		partPath := path + ".content." + strconv.Itoa(index)
		switch part.Get("type").Str {
		case "input_text", "output_text", "text":
			text := part.Get("text").Str
			textParts = append(textParts, text)
			hasPlainText = hasPlainText || strings.TrimSpace(text) != ""
		case "encrypted_content":
			c.report("responses_encrypted_agent_content_dropped", "dropped", partPath)
		default:
			c.markNativeOnly("responses_agent_message_native_only", partPath)
			return nil, false
		}
	}
	if !hasPlainText {
		c.markNativeOnly("responses_agent_message_native_only", path)
		return nil, false
	}
	c.report("responses_agent_message_projected", "projected", path)
	return map[string]any{"role": "assistant", "content": strings.Join(textParts, "\n")}, true
}

func (c *portableCodexResponsesConverter) appendToolCall(messages *[]map[string]any, item gjson.Result, path string, custom bool) {
	name := item.Get("name").Str
	callID := firstNonEmpty(item.Get("call_id").Str, item.Get("id").Str)
	if name == "" || callID == "" {
		c.markNativeOnly("responses_tool_call_native_only", path)
		return
	}
	namespace := normalizeResponsesToolNamespace(item.Get("namespace").Str)
	alias := c.ensureToolMapping(name, namespace, custom)

	arguments := item.Get("arguments")
	if custom {
		arguments = item.Get("input")
	}
	if arguments.Type != gjson.String {
		c.markNativeOnly("responses_tool_call_native_only", path)
		return
	}
	argumentText := arguments.Str
	if custom {
		encoded, err := json.Marshal(map[string]string{"input": argumentText})
		if err != nil {
			c.markNativeOnly("responses_tool_call_native_only", path)
			return
		}
		argumentText = string(encoded)
	}
	toolCall := map[string]any{
		"id":   callID,
		"type": "function",
		"function": map[string]any{
			"name":      alias,
			"arguments": argumentText,
		},
	}

	current := *messages
	if len(current) > 0 && current[len(current)-1]["role"] == "assistant" {
		if calls, ok := current[len(current)-1]["tool_calls"].([]map[string]any); ok {
			current[len(current)-1]["tool_calls"] = append(calls, toolCall)
			return
		}
	}
	*messages = append(current, map[string]any{
		"role":       "assistant",
		"content":    "",
		"tool_calls": []map[string]any{toolCall},
	})
}

func (c *portableCodexResponsesConverter) convertToolOutput(item gjson.Result, path string) (map[string]any, bool) {
	callID := item.Get("call_id").Str
	if callID == "" {
		c.markNativeOnly("responses_tool_output_native_only", path)
		return nil, false
	}
	content, structured, ok := c.convertToolOutputContent(item.Get("output"), path+".output")
	if !ok {
		return nil, false
	}
	if structured {
		c.report("responses_structured_tool_output_projected", "projected", path+".output")
	}
	return map[string]any{"role": "tool", "tool_call_id": callID, "content": content}, true
}

func (c *portableCodexResponsesConverter) convertToolOutputContent(output gjson.Result, path string) (any, bool, bool) {
	if output.Type == gjson.String {
		return output.Str, false, true
	}
	if !output.IsArray() {
		c.markNativeOnly("responses_tool_output_native_only", path)
		return nil, false, false
	}
	parts := make([]string, 0, len(output.Array()))
	for index, part := range output.Array() {
		if part.Get("type").Str != "input_text" {
			c.markNativeOnly("responses_tool_output_native_only", path+"."+strconv.Itoa(index))
			return nil, true, false
		}
		parts = append(parts, part.Get("text").Str)
	}
	return strings.Join(parts, "\n"), true, true
}

func (c *portableCodexResponsesConverter) copyRequestControls(root gjson.Result, out map[string]any) {
	if choice := root.Get("tool_choice"); choice.Exists() && len(c.tools) > 0 {
		if converted, ok := c.convertToolChoice(choice); ok {
			out["tool_choice"] = converted
		}
	}
	if parallel := root.Get("parallel_tool_calls"); parallel.Exists() {
		out["parallel_tool_calls"] = parallel.Bool()
	}
	if temperature := root.Get("temperature"); temperature.Exists() {
		out["temperature"] = temperature.Num
	}
	if topP := root.Get("top_p"); topP.Exists() {
		out["top_p"] = topP.Num
	}
	if maxOutput := root.Get("max_output_tokens"); maxOutput.Exists() {
		out["max_completion_tokens"] = maxOutput.Int()
	}
	if metadata := root.Get("metadata"); metadata.IsObject() {
		out["metadata"] = json.RawMessage(metadata.Raw)
	}
	if serviceTier := root.Get("service_tier"); serviceTier.Exists() {
		// OriginalBody retains this OpenAI-only hint for native OpenAI dispatch.
		// The portable body may be sent to any HMM-selected Chat provider.
		c.report("responses_service_tier_dropped", "dropped", "service_tier")
	}
	if promptCacheKey := root.Get("prompt_cache_key"); promptCacheKey.Exists() {
		c.report("responses_prompt_cache_key_dropped", "dropped", "prompt_cache_key")
	}
	if responseFormat := root.Get("response_format"); responseFormat.IsObject() {
		out["response_format"] = json.RawMessage(responseFormat.Raw)
		return
	}
	if format := root.Get("text.format"); format.Exists() {
		if converted, ok := c.convertTextFormat(format); ok {
			out["response_format"] = converted
		}
	}
}

func (c *portableCodexResponsesConverter) convertTextVerbosity(verbosity gjson.Result) (string, bool) {
	if verbosity.Type != gjson.String {
		c.markNativeOnly("responses_text_verbosity_native_only", "text.verbosity")
		return "", false
	}
	var instruction string
	switch verbosity.Str {
	case "low":
		instruction = "Keep the response concise and focused."
	case "medium":
		instruction = "Use a moderate level of detail in the response."
	case "high":
		instruction = "Provide a detailed and thorough response."
	default:
		c.markNativeOnly("responses_text_verbosity_native_only", "text.verbosity")
		return "", false
	}
	c.report("responses_text_verbosity_projected", "projected", "text.verbosity")
	return instruction, true
}

func (c *portableCodexResponsesConverter) convertToolChoice(choice gjson.Result) (any, bool) {
	if choice.Type == gjson.String {
		switch choice.Str {
		case "auto", "none", "required":
			return choice.Str, true
		default:
			c.markNativeOnly("responses_tool_choice_native_only", "tool_choice")
			return nil, false
		}
	}
	if !choice.IsObject() {
		c.markNativeOnly("responses_tool_choice_native_only", "tool_choice")
		return nil, false
	}
	choiceType := choice.Get("type").Str
	custom := choiceType == "custom"
	if choiceType != "function" && !custom {
		c.markNativeOnly("responses_tool_choice_native_only", "tool_choice")
		return nil, false
	}
	name := firstNonEmpty(choice.Get("name").Str, choice.Get("function.name").Str)
	namespace := normalizeResponsesToolNamespace(firstNonEmpty(choice.Get("namespace").Str, choice.Get("function.namespace").Str))
	alias, ok := c.toolAliases[responsesToolIdentity{name: name, namespace: namespace, custom: custom}]
	if !ok {
		c.markNativeOnly("responses_tool_choice_native_only", "tool_choice")
		return nil, false
	}
	return map[string]any{"type": "function", "function": map[string]any{"name": alias}}, true
}

func (c *portableCodexResponsesConverter) convertTextFormat(format gjson.Result) (any, bool) {
	switch format.Get("type").Str {
	case "json_schema":
		schema := format.Get("schema")
		if !schema.IsObject() {
			c.markNativeOnly("responses_text_format_native_only", "text.format")
			return nil, false
		}
		jsonSchema := map[string]any{"schema": json.RawMessage(schema.Raw)}
		if name := format.Get("name").Str; name != "" {
			jsonSchema["name"] = name
		}
		if strict := format.Get("strict"); strict.Exists() {
			jsonSchema["strict"] = strict.Bool()
		}
		return map[string]any{"type": "json_schema", "json_schema": jsonSchema}, true
	case "json_object":
		return map[string]any{"type": "json_object"}, true
	case "text", "":
		return nil, false
	default:
		c.markNativeOnly("responses_text_format_native_only", "text.format")
		return nil, false
	}
}

func (c *portableCodexResponsesConverter) ensureToolMapping(name, namespace string, custom bool) string {
	namespace = normalizeResponsesToolNamespace(namespace)
	identity := responsesToolIdentity{name: name, namespace: namespace, custom: custom}
	if alias, ok := c.toolAliases[identity]; ok {
		return alias
	}
	base := name
	if namespace != "" {
		base = namespace + "__" + name
	}
	base = sanitizeResponsesToolAlias(base)
	if base == "" {
		base = "tool"
	}
	alias := base
	if _, collision := c.result.ToolMappings[alias]; collision {
		alias = responsesToolAliasWithHash(base, identity, 0)
		for attempt := 1; ; attempt++ {
			if _, collision := c.result.ToolMappings[alias]; !collision {
				break
			}
			alias = responsesToolAliasWithHash(base, identity, attempt)
		}
	}
	mapping := ResponsesToolMapping{Alias: alias, Name: name, Namespace: namespace, Custom: custom}
	c.toolAliases[identity] = alias
	c.result.ToolMappings[alias] = mapping
	return alias
}

func (c *portableCodexResponsesConverter) report(code, action, path string) {
	c.result.Report = append(c.result.Report, ResponseTransform{Code: code, Action: action, Path: path})
}

func (c *portableCodexResponsesConverter) markNativeOnly(code, path string) {
	c.result.Requirements.NativeOnly = true
	c.report(code, "preserved", path)
}

func normalizeResponsesToolNamespace(namespace string) string {
	if namespace == responsesDefaultToolNamespace {
		return ""
	}
	return namespace
}

func sanitizeResponsesToolAlias(name string) string {
	var out strings.Builder
	for _, char := range name {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '_' || char == '-' {
			out.WriteRune(char)
			continue
		}
		out.WriteByte('_')
	}
	alias := out.String()
	if len(alias) > responsesMaxToolAliasBytes {
		alias = alias[:responsesMaxToolAliasBytes]
	}
	return alias
}

func responsesToolAliasWithHash(base string, identity responsesToolIdentity, attempt int) string {
	sum := sha256.Sum256([]byte(identity.namespace + "\x00" + identity.name + "\x00" + strconv.FormatBool(identity.custom) + "\x00" + strconv.Itoa(attempt)))
	suffix := fmt.Sprintf("__%x", sum[:5])
	maxBase := responsesMaxToolAliasBytes - len(suffix)
	if len(base) > maxBase {
		base = base[:maxBase]
	}
	return base + suffix
}

func portableCustomToolFormatDescription(format gjson.Result) string {
	if !format.IsObject() {
		return ""
	}
	definition := format.Get("definition").Str
	if definition == "" {
		return ""
	}
	syntax := format.Get("syntax").Str
	if syntax == "" {
		syntax = format.Get("type").Str
	}
	if syntax == "" {
		return "Custom tool input format:\n" + definition
	}
	return "Custom tool input format (" + syntax + "):\n" + definition
}

func firstExisting(values ...gjson.Result) gjson.Result {
	for _, value := range values {
		if value.Exists() {
			return value
		}
	}
	return gjson.Result{}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func schemaContainsDefinitionsOrRefs(node any) bool {
	var visit func(any) bool
	visit = func(value any) bool {
		switch typed := value.(type) {
		case map[string]any:
			if _, ok := typed["$ref"]; ok {
				return true
			}
			if _, ok := typed["$defs"]; ok {
				return true
			}
			if _, ok := typed["definitions"]; ok {
				return true
			}
			for key, child := range typed {
				// Keys inside properties are user field names, not schema
				// keywords. Recurse into each property's schema instead.
				if key == "properties" {
					if properties, ok := child.(map[string]any); ok {
						for _, propertySchema := range properties {
							if visit(propertySchema) {
								return true
							}
						}
					}
					continue
				}
				if visit(child) {
					return true
				}
			}
		case []any:
			for _, child := range typed {
				if visit(child) {
					return true
				}
			}
		}
		return false
	}
	return visit(node)
}
