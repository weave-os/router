package translate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"weave-os/router/internal/sse"

	"github.com/tidwall/gjson"
)

// EscalationResponseFormat identifies the client-facing bytes, not the upstream provider.
type EscalationResponseFormat string

const (
	EscalationResponseAnthropic EscalationResponseFormat = "anthropic_messages"
	EscalationResponseChat      EscalationResponseFormat = "chat"
	EscalationResponseResponses EscalationResponseFormat = "responses"
	EscalationResponseGemini    EscalationResponseFormat = "gemini"
)

// EscalationResponse supplies the completed assistant events for a continuation.
type EscalationResponse struct {
	ResponseID string              `json:"response_id,omitempty"`
	Messages   []EscalationMessage `json:"messages"`
}

type escalationResponseEvent string

const (
	escalationResponseCompleted  escalationResponseEvent = "response.completed"
	escalationResponseFailed     escalationResponseEvent = "response.failed"
	escalationResponseIncomplete escalationResponseEvent = "response.incomplete"
	escalationResponseError      escalationResponseEvent = "error"
	escalationMessageStart       escalationResponseEvent = "message_start"
	escalationMessageDelta       escalationResponseEvent = "message_delta"
	escalationMessageStop        escalationResponseEvent = "message_stop"
	escalationContentStart       escalationResponseEvent = "content_block_start"
	escalationContentDelta       escalationResponseEvent = "content_block_delta"
	escalationTextDelta          escalationResponseEvent = "text_delta"
	escalationInputJSONDelta     escalationResponseEvent = "input_json_delta"
)

type escalationResponseStatus string

const escalationResponseStatusCompleted escalationResponseStatus = "completed"

type escalationFinishReason string

const (
	escalationAnthropicEndTurn      escalationFinishReason = "end_turn"
	escalationAnthropicToolUse      escalationFinishReason = "tool_use"
	escalationAnthropicStopSequence escalationFinishReason = "stop_sequence"
	escalationChatStop              escalationFinishReason = "stop"
	escalationChatToolCalls         escalationFinishReason = "tool_calls"
	escalationChatFunctionCall      escalationFinishReason = "function_call"
	escalationGeminiStop            escalationFinishReason = "STOP"
)

func escalationAnthropicCompleted(reason escalationFinishReason) bool {
	switch reason {
	case escalationAnthropicEndTurn, escalationAnthropicToolUse, escalationAnthropicStopSequence:
		return true
	default:
		return false
	}
}

// ParseEscalationResponse accepts JSON or SSE and rejects incomplete streams so
// partial tool arguments never become completed history for a later request.
func ParseEscalationResponse(body []byte, format EscalationResponseFormat) (EscalationResponse, error) {
	frames, streaming, done, err := escalationResponseFrames(body)
	if err != nil {
		return EscalationResponse{}, err
	}
	switch format {
	case EscalationResponseResponses:
		return escalationResponsesOutput(frames, streaming)
	case EscalationResponseAnthropic:
		return escalationAnthropicOutput(frames, streaming)
	case EscalationResponseChat:
		return escalationChatOutput(frames, streaming, done)
	case EscalationResponseGemini:
		return escalationGeminiOutput(frames)
	default:
		return EscalationResponse{}, fmt.Errorf("unsupported escalation response format %q", format)
	}
}

func escalationResponseFrames(body []byte) ([]gjson.Result, bool, bool, error) {
	trimmed := bytes.TrimSpace(body)
	if json.Valid(trimmed) {
		root := gjson.ParseBytes(trimmed)
		if root.IsArray() {
			return root.Array(), false, false, nil
		}
		return []gjson.Result{root}, false, false, nil
	}
	frames := make([]gjson.Result, 0)
	done := false
	remaining := body
	for len(remaining) > 0 {
		event, consumed := sse.SplitNext(remaining)
		if consumed == 0 {
			if len(bytes.TrimSpace(remaining)) > 0 {
				return nil, true, done, fmt.Errorf("unterminated escalation response event")
			}
			break
		}
		remaining = remaining[consumed:]
		// ParseEvent intentionally supports only the first data line. Reject
		// multiline input instead of silently discarding unparsed evidence.
		dataLines := bytes.Count(event, []byte("\ndata:"))
		if bytes.HasPrefix(event, []byte("data:")) {
			dataLines++
		}
		if dataLines > 1 {
			return nil, true, done, fmt.Errorf("multiline escalation response event is unsupported")
		}
		_, payload := sse.ParseEvent(event)
		if len(payload) == 0 {
			continue
		}
		if bytes.Equal(payload, []byte("[DONE]")) {
			done = true
			continue
		}
		if !json.Valid(payload) {
			return nil, true, done, fmt.Errorf("invalid JSON in escalation response event")
		}
		frames = append(frames, gjson.ParseBytes(payload))
	}
	if len(frames) == 0 {
		return nil, true, done, fmt.Errorf("response contains no JSON events")
	}
	return frames, true, done, nil
}

func escalationResponsesOutput(frames []gjson.Result, streaming bool) (EscalationResponse, error) {
	for _, frame := range frames {
		response := frame
		if streaming {
			switch escalationResponseEvent(frame.Get("type").String()) {
			case escalationResponseFailed, escalationResponseIncomplete, escalationResponseError:
				return EscalationResponse{}, fmt.Errorf("Responses stream did not complete")
			case escalationResponseCompleted:
				response = frame.Get("response")
			default:
				continue
			}
		}
		if escalationResponseStatus(response.Get("status").String()) != escalationResponseStatusCompleted {
			return EscalationResponse{}, fmt.Errorf("Responses response is not completed")
		}
		output := response.Get("output")
		if !output.IsArray() {
			return EscalationResponse{}, fmt.Errorf("Responses completion has no output array")
		}
		observation, err := ParseResponsesEscalationObservation([]byte(`{"input":` + output.Raw + `}`))
		if err != nil {
			return EscalationResponse{}, err
		}
		if !observation.HistoryComplete {
			return EscalationResponse{}, fmt.Errorf("Responses output has unresolved references")
		}
		return EscalationResponse{ResponseID: response.Get("id").String(), Messages: observation.Messages}, nil
	}
	return EscalationResponse{}, fmt.Errorf("Responses stream has no completed event")
}

func escalationAnthropicOutput(frames []gjson.Result, streaming bool) (EscalationResponse, error) {
	if !streaming {
		if len(frames) != 1 || frames[0].Get("error").Exists() || !frames[0].Get("content").IsArray() {
			return EscalationResponse{}, fmt.Errorf("invalid Anthropic response")
		}
		if !escalationAnthropicCompleted(escalationFinishReason(frames[0].Get("stop_reason").String())) {
			return EscalationResponse{}, fmt.Errorf("Anthropic response did not complete")
		}
		blocks, hasOmittedMedia, err := escalationContentBlocks(frames[0].Get("content"))
		if err != nil {
			return EscalationResponse{}, err
		}
		return EscalationResponse{ResponseID: frames[0].Get("id").String(), Messages: []EscalationMessage{{Role: EscalationRoleAssistant, Blocks: blocks, HasOmittedMedia: hasOmittedMedia}}}, nil
	}
	responseID := ""
	complete := false
	finishReason := escalationFinishReason("")
	hasOmittedMedia := false
	blocks := make(map[int]EscalationBlock)
	textBuilders := make(map[int]*strings.Builder)
	arguments := make(map[int]*strings.Builder)
	for _, frame := range frames {
		index := int(frame.Get("index").Int())
		switch escalationResponseEvent(frame.Get("type").String()) {
		case escalationResponseError:
			return EscalationResponse{}, fmt.Errorf("Anthropic stream failed")
		case escalationMessageStart:
			responseID = frame.Get("message.id").String()
		case escalationMessageDelta:
			if reason := frame.Get("delta.stop_reason"); reason.Exists() && reason.Type != gjson.Null {
				finishReason = escalationFinishReason(reason.String())
				if !escalationAnthropicCompleted(finishReason) {
					return EscalationResponse{}, fmt.Errorf("Anthropic stream did not complete")
				}
			}
		case escalationMessageStop:
			complete = true
		case escalationContentStart:
			content := frame.Get("content_block")
			parsed, omittedMedia, err := escalationContentBlocks(gjson.Parse("[" + content.Raw + "]"))
			if err != nil {
				return EscalationResponse{}, err
			}
			hasOmittedMedia = hasOmittedMedia || omittedMedia
			if len(parsed) > 0 {
				blocks[index] = parsed[0]
				delete(textBuilders, index)
				if parsed[0].Type == EscalationBlockText {
					textBuilder := &strings.Builder{}
					textBuilder.WriteString(parsed[0].Text)
					textBuilders[index] = textBuilder
				}
			}
		case escalationContentDelta:
			_, found := blocks[index]
			if !found {
				continue
			}
			delta := frame.Get("delta")
			switch escalationResponseEvent(delta.Get("type").String()) {
			case escalationTextDelta:
				escalationFragmentBuilder(textBuilders, index).WriteString(delta.Get("text").String())
			case escalationInputJSONDelta:
				escalationFragmentBuilder(arguments, index).WriteString(delta.Get("partial_json").String())
			}
		}
	}
	if !complete || !escalationAnthropicCompleted(finishReason) {
		return EscalationResponse{}, fmt.Errorf("Anthropic stream has no terminal event")
	}
	for index, textBuilder := range textBuilders {
		block := blocks[index]
		block.Text = textBuilder.String()
		blocks[index] = block
	}
	ordered, err := escalationOrderedBlocks(blocks, arguments)
	if err != nil {
		return EscalationResponse{}, err
	}
	return EscalationResponse{ResponseID: responseID, Messages: []EscalationMessage{{Role: EscalationRoleAssistant, Blocks: ordered, HasOmittedMedia: hasOmittedMedia}}}, nil
}

func escalationChatOutput(frames []gjson.Result, streaming, done bool) (EscalationResponse, error) {
	responseID := ""
	var text strings.Builder
	toolCalls := make(map[int]EscalationBlock)
	toolNames := make(map[int]*strings.Builder)
	arguments := make(map[int]*strings.Builder)
	complete := false
	legacyCall := false
	legacyCallExpected := false
	for _, frame := range frames {
		if frame.Get("error").Exists() {
			return EscalationResponse{}, fmt.Errorf("chat response failed")
		}
		if frame.Get("id").Exists() {
			responseID = frame.Get("id").String()
		}
		choices := frame.Get("choices").Array()
		if len(choices) > 1 {
			return EscalationResponse{}, fmt.Errorf("multiple chat choices cannot form one continuation")
		}
		if len(choices) == 0 {
			continue
		}
		choice := choices[0]
		if finish := choice.Get("finish_reason"); finish.Exists() && finish.Type != gjson.Null {
			switch escalationFinishReason(finish.String()) {
			case escalationChatStop, escalationChatToolCalls, escalationChatFunctionCall:
				complete = true
				legacyCallExpected = escalationFinishReason(finish.String()) == escalationChatFunctionCall
			default:
				return EscalationResponse{}, fmt.Errorf("chat response did not complete")
			}
		}
		message := choice.Get("message")
		if streaming {
			message = choice.Get("delta")
		}
		text.WriteString(message.Get("content").String())
		for position, call := range message.Get("tool_calls").Array() {
			if legacyCall {
				return EscalationResponse{}, fmt.Errorf("chat response mixes legacy and current tool calls")
			}
			index := position
			if streaming {
				index = int(call.Get("index").Int())
			}
			block := toolCalls[index]
			block.Type = EscalationBlockToolCall
			if call.Get("id").Exists() {
				block.ID = call.Get("id").String()
			}
			escalationFragmentBuilder(toolNames, index).WriteString(call.Get("function.name").String())
			escalationFragmentBuilder(arguments, index).WriteString(call.Get("function.arguments").String())
			toolCalls[index] = block
		}
		if function := message.Get("function_call"); function.Exists() {
			if !function.IsObject() || (!legacyCall && len(toolCalls) > 0) {
				return EscalationResponse{}, fmt.Errorf("invalid legacy chat function call")
			}
			name, input := function.Get("name"), function.Get("arguments")
			if (name.Exists() && name.Type != gjson.String) || (input.Exists() && input.Type != gjson.String) {
				return EscalationResponse{}, fmt.Errorf("invalid legacy chat function call fields")
			}
			legacyCall = true
			block := toolCalls[0]
			block.Type = EscalationBlockToolCall
			escalationFragmentBuilder(toolNames, 0).WriteString(name.String())
			escalationFragmentBuilder(arguments, 0).WriteString(input.String())
			toolCalls[0] = block
		}
	}
	if !complete || (streaming && !done) {
		return EscalationResponse{}, fmt.Errorf("chat response has no terminal event")
	}
	for index, nameBuilder := range toolNames {
		block := toolCalls[index]
		block.Name = nameBuilder.String()
		toolCalls[index] = block
	}
	if (legacyCallExpected && !legacyCall) || (legacyCall && strings.TrimSpace(toolCalls[0].Name) == "") {
		return EscalationResponse{}, fmt.Errorf("chat response has no completed legacy function call")
	}
	blocks := make([]EscalationBlock, 0, len(toolCalls)+1)
	if text.Len() > 0 {
		blocks = append(blocks, EscalationBlock{Type: EscalationBlockText, Text: text.String()})
	}
	ordered, err := escalationOrderedBlocks(toolCalls, arguments)
	if err != nil {
		return EscalationResponse{}, err
	}
	blocks = append(blocks, ordered...)
	return EscalationResponse{ResponseID: responseID, Messages: []EscalationMessage{{Role: EscalationRoleAssistant, Blocks: blocks}}}, nil
}

func escalationGeminiOutput(frames []gjson.Result) (EscalationResponse, error) {
	responseID := ""
	complete := false
	hasOmittedMedia := false
	blocks := make([]EscalationBlock, 0)
	var adjacentText *strings.Builder
	flushText := func() {
		if adjacentText == nil {
			return
		}
		blocks = append(blocks, EscalationBlock{Type: EscalationBlockText, Text: adjacentText.String()})
		adjacentText = nil
	}
	for _, frame := range frames {
		if frame.Get("error").Exists() {
			return EscalationResponse{}, fmt.Errorf("Gemini response failed")
		}
		if frame.Get("responseId").Exists() {
			responseID = frame.Get("responseId").String()
		}
		candidates := frame.Get("candidates").Array()
		if len(candidates) > 1 {
			return EscalationResponse{}, fmt.Errorf("multiple Gemini candidates cannot form one continuation")
		}
		if len(candidates) == 0 {
			continue
		}
		candidate := candidates[0]
		if reason := candidate.Get("finishReason"); reason.Exists() && reason.Type != gjson.Null {
			if escalationFinishReason(reason.String()) != escalationGeminiStop {
				return EscalationResponse{}, fmt.Errorf("Gemini response did not complete")
			}
			complete = true
		}
		parsed, omittedMedia, err := escalationGeminiBlocks(candidate.Get("content.parts"))
		if err != nil {
			return EscalationResponse{}, err
		}
		hasOmittedMedia = hasOmittedMedia || omittedMedia
		for _, block := range parsed {
			if block.Type == EscalationBlockText {
				if adjacentText == nil {
					adjacentText = &strings.Builder{}
				}
				adjacentText.WriteString(block.Text)
				continue
			}
			flushText()
			blocks = append(blocks, block)
		}
	}
	if !complete {
		return EscalationResponse{}, fmt.Errorf("Gemini response has no terminal candidate")
	}
	flushText()
	return EscalationResponse{ResponseID: responseID, Messages: []EscalationMessage{{Role: EscalationRoleAssistant, Blocks: blocks, HasOmittedMedia: hasOmittedMedia}}}, nil
}

func escalationFragmentBuilder(builders map[int]*strings.Builder, index int) *strings.Builder {
	fragmentBuilder := builders[index]
	if fragmentBuilder == nil {
		fragmentBuilder = &strings.Builder{}
		builders[index] = fragmentBuilder
	}
	return fragmentBuilder
}

func escalationOrderedBlocks(blocks map[int]EscalationBlock, arguments map[int]*strings.Builder) ([]EscalationBlock, error) {
	indexes := make([]int, 0, len(blocks))
	for index := range blocks {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	ordered := make([]EscalationBlock, 0, len(blocks))
	for _, index := range indexes {
		block := blocks[index]
		if inputBuilder, exists := arguments[index]; exists {
			input := inputBuilder.String()
			if !json.Valid([]byte(input)) {
				return nil, fmt.Errorf("completed tool call has invalid arguments")
			}
			block.ArgumentsJSON = input
		}
		ordered = append(ordered, block)
	}
	return ordered, nil
}
