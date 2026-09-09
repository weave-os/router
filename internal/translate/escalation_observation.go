package translate

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

type EscalationSchemaVersion string

// EscalationObservationSchema identifies the lossless visible-event contract.
const EscalationObservationSchema EscalationSchemaVersion = "escalation_observation_v1"

// EscalationRole is a semantic role, independent of the client protocol.
type EscalationRole string

const (
	EscalationRoleSystem    EscalationRole = "system"
	EscalationRoleUser      EscalationRole = "user"
	EscalationRoleAssistant EscalationRole = "assistant"
	EscalationRoleTool      EscalationRole = "tool"
	escalationRoleDeveloper EscalationRole = "developer"
	escalationRoleModel     EscalationRole = "model"
	escalationRoleFunction  EscalationRole = "function"
)

// EscalationBlockType distinguishes ordered visible events within a message.
type EscalationBlockType string

const (
	EscalationBlockText       EscalationBlockType = "text"
	EscalationBlockToolCall   EscalationBlockType = "tool_call"
	EscalationBlockToolResult EscalationBlockType = "tool_result"
)

// EscalationTurnType describes the final inbound decision boundary.
type EscalationTurnType string

const (
	EscalationTurnMainLoop   EscalationTurnType = "main_loop"
	EscalationTurnToolResult EscalationTurnType = "tool_result"
)

// EscalationObservation preserves visible order without recording reasoning or media.
// A continuation or item reference requires history reconstruction before inference.
type EscalationObservation struct {
	SchemaVersion    EscalationSchemaVersion `json:"schema_version"`
	Messages         []EscalationMessage     `json:"messages"`
	TurnType         EscalationTurnType      `json:"turn_type"`
	HistoryComplete  bool                    `json:"history_complete"`
	ContinuationID   string                  `json:"continuation_id,omitempty"`
	ItemReferenceIDs []string                `json:"item_reference_ids,omitempty"`
}

// EscalationMessage keeps text and tool events in their original relative order.
type EscalationMessage struct {
	Role   EscalationRole    `json:"role"`
	Blocks []EscalationBlock `json:"blocks"`
}

// EscalationBlock retains raw JSON for arguments/results, including custom-tool strings.
// IsError is nil when the protocol supplied no explicit error verdict.
type EscalationBlock struct {
	Type          EscalationBlockType `json:"type"`
	Text          string              `json:"text,omitempty"`
	ID            string              `json:"id,omitempty"`
	Name          string              `json:"name,omitempty"`
	Namespace     string              `json:"namespace,omitempty"`
	ArgumentsJSON string              `json:"arguments_json,omitempty"`
	CallID        string              `json:"call_id,omitempty"`
	ContentJSON   string              `json:"content_json,omitempty"`
	IsError       *bool               `json:"is_error,omitempty"`
}

type escalationWireType string

type escalationToolStatus string

const (
	escalationToolStatusError  escalationToolStatus = "error"
	escalationToolStatusFailed escalationToolStatus = "failed"
)

const (
	escalationWireText             escalationWireType = "text"
	escalationWireInputText        escalationWireType = "input_text"
	escalationWireOutputText       escalationWireType = "output_text"
	escalationWireToolUse          escalationWireType = "tool_use"
	escalationWireToolResult       escalationWireType = "tool_result"
	escalationWireMessage          escalationWireType = "message"
	escalationWireAgentMessage     escalationWireType = "agent_message"
	escalationWireFunctionCall     escalationWireType = "function_call"
	escalationWireCustomCall       escalationWireType = "custom_tool_call"
	escalationWireFunctionOutput   escalationWireType = "function_call_output"
	escalationWireCustomOutput     escalationWireType = "custom_tool_call_output"
	escalationWireReference        escalationWireType = "item_reference"
	escalationWireThinking         escalationWireType = "thinking"
	escalationWireRedactedThinking escalationWireType = "redacted_thinking"
	escalationWireReasoning        escalationWireType = "reasoning"
	escalationWireImage            escalationWireType = "image"
	escalationWireImageURL         escalationWireType = "image_url"
	escalationWireInputImage       escalationWireType = "input_image"
	escalationWireInputAudio       escalationWireType = "input_audio"
	escalationWireDocument         escalationWireType = "document"
	escalationWireFile             escalationWireType = "file"
	escalationWireInputFile        escalationWireType = "input_file"
	escalationWireAdditionalTools  escalationWireType = "additional_tools"
)

// EscalationObservation extracts every visible event without the HMM serializer's
// clipping or tool-argument stripping. Responses callers must use the original body.
func (e *RequestEnvelope) EscalationObservation() (EscalationObservation, error) {
	if e == nil {
		return EscalationObservation{}, fmt.Errorf("missing request envelope")
	}
	observation := newEscalationObservation()
	root := gjson.ParseBytes(e.body)
	switch e.format {
	case FormatAnthropic:
		if root.Get("system").Exists() {
			blocks, err := escalationContentBlocks(root.Get("system"))
			if err != nil {
				return observation, err
			}
			observation.Messages = append(observation.Messages, EscalationMessage{Role: EscalationRoleSystem, Blocks: blocks})
		}
		for _, message := range root.Get("messages").Array() {
			role, err := escalationSemanticRole(message.Get("role").String())
			if err != nil {
				return observation, err
			}
			blocks, err := escalationContentBlocks(message.Get("content"))
			if err != nil {
				return observation, err
			}
			observation.Messages = append(observation.Messages, EscalationMessage{Role: role, Blocks: blocks})
		}
	case FormatOpenAI:
		if root.Get("input").Exists() || root.Get("previous_response_id").Exists() {
			return ParseResponsesEscalationObservation(e.body)
		}
		for _, message := range root.Get("messages").Array() {
			role, err := escalationSemanticRole(message.Get("role").String())
			if err != nil {
				return observation, err
			}
			blocks := make([]EscalationBlock, 0)
			if role == EscalationRoleTool {
				blocks = append(blocks, EscalationBlock{Type: EscalationBlockToolResult, CallID: message.Get("tool_call_id").String(), Name: message.Get("name").String(), ContentJSON: escalationJSON(message.Get("content")), IsError: escalationErrorFlag(message)})
			} else {
				blocks, err = escalationContentBlocks(message.Get("content"))
				if err != nil {
					return observation, err
				}
				for _, call := range message.Get("tool_calls").Array() {
					function := call.Get("function")
					if !escalationNamedTool(function) {
						return observation, fmt.Errorf("unsupported chat tool call")
					}
					blocks = append(blocks, EscalationBlock{Type: EscalationBlockToolCall, ID: call.Get("id").String(), Name: function.Get("name").String(), ArgumentsJSON: escalationArguments(function.Get("arguments"))})
				}
				if function := message.Get("function_call"); function.Exists() {
					if !escalationNamedTool(function) {
						return observation, fmt.Errorf("invalid chat function call")
					}
					blocks = append(blocks, EscalationBlock{Type: EscalationBlockToolCall, Name: function.Get("name").String(), ArgumentsJSON: escalationArguments(function.Get("arguments"))})
				}
			}
			observation.Messages = append(observation.Messages, EscalationMessage{Role: role, Blocks: blocks})
		}
	case FormatGemini:
		system := root.Get("systemInstruction")
		if !system.Exists() {
			system = root.Get("system_instruction")
		}
		if system.Exists() {
			blocks, err := escalationGeminiBlocks(system.Get("parts"))
			if err != nil {
				return observation, err
			}
			observation.Messages = append(observation.Messages, EscalationMessage{Role: EscalationRoleSystem, Blocks: blocks})
		}
		for _, message := range root.Get("contents").Array() {
			roleName := message.Get("role").String()
			if roleName == "" {
				roleName = string(EscalationRoleUser)
			}
			role, err := escalationSemanticRole(roleName)
			if err != nil {
				return observation, err
			}
			blocks, err := escalationGeminiBlocks(message.Get("parts"))
			if err != nil {
				return observation, err
			}
			observation.Messages = append(observation.Messages, EscalationMessage{Role: role, Blocks: blocks})
		}
	default:
		return observation, fmt.Errorf("unsupported escalation request format %d", e.format)
	}
	observation.setTurnType()
	return observation, nil
}

// ParseResponsesEscalationObservation reads original Responses input independently
// of client identity and dispatch projections, retaining custom tools and references.
func ParseResponsesEscalationObservation(body []byte) (EscalationObservation, error) {
	observation := newEscalationObservation()
	if err := validateResponsesRequest(body); err != nil {
		return observation, err
	}
	root := gjson.ParseBytes(body)
	observation.ContinuationID = root.Get("previous_response_id").String()
	observation.HistoryComplete = observation.ContinuationID == ""
	if instructions := root.Get("instructions"); instructions.Type == gjson.String {
		observation.Messages = append(observation.Messages, EscalationMessage{Role: EscalationRoleSystem, Blocks: []EscalationBlock{{Type: EscalationBlockText, Text: instructions.String()}}})
	}
	input := root.Get("input")
	if input.Type == gjson.String {
		observation.Messages = append(observation.Messages, EscalationMessage{Role: EscalationRoleUser, Blocks: []EscalationBlock{{Type: EscalationBlockText, Text: input.String()}}})
	} else if input.Exists() && !input.IsArray() {
		return observation, fmt.Errorf("Responses input must be a string or array")
	} else {
		for _, item := range input.Array() {
			kind := escalationWireType(item.Get("type").String())
			if kind == "" && item.Get("role").Exists() {
				kind = escalationWireMessage
			}
			switch kind {
			case escalationWireMessage, escalationWireAgentMessage:
				roleName := item.Get("role").String()
				if roleName == "" {
					roleName = string(EscalationRoleUser)
					if kind == escalationWireAgentMessage {
						roleName = string(EscalationRoleAssistant)
					}
				}
				role, err := escalationSemanticRole(roleName)
				if err != nil {
					return observation, err
				}
				blocks, err := escalationContentBlocks(item.Get("content"))
				if err != nil {
					return observation, err
				}
				observation.Messages = append(observation.Messages, EscalationMessage{Role: role, Blocks: blocks})
			case escalationWireFunctionCall, escalationWireCustomCall:
				if !escalationNamedTool(item) {
					return observation, fmt.Errorf("invalid Responses tool call")
				}
				arguments := escalationArguments(item.Get("arguments"))
				if kind == escalationWireCustomCall {
					arguments = escalationJSON(item.Get("input"))
				}
				callID := item.Get("call_id").String()
				if callID == "" {
					callID = item.Get("id").String()
				}
				observation.Messages = append(observation.Messages, EscalationMessage{Role: EscalationRoleAssistant, Blocks: []EscalationBlock{{Type: EscalationBlockToolCall, ID: callID, Name: item.Get("name").String(), Namespace: item.Get("namespace").String(), ArgumentsJSON: arguments}}})
			case escalationWireFunctionOutput, escalationWireCustomOutput:
				observation.Messages = append(observation.Messages, EscalationMessage{Role: EscalationRoleTool, Blocks: []EscalationBlock{{Type: EscalationBlockToolResult, CallID: item.Get("call_id").String(), ContentJSON: escalationJSON(item.Get("output")), IsError: escalationErrorFlag(item)}}})
			case escalationWireReference:
				referenceID := item.Get("id").String()
				if referenceID == "" {
					return observation, fmt.Errorf("Responses item reference has no id")
				}
				observation.ItemReferenceIDs = append(observation.ItemReferenceIDs, referenceID)
				observation.HistoryComplete = false
			case escalationWireReasoning, escalationWireAdditionalTools:
			default:
				return observation, fmt.Errorf("unsupported Responses observation item %q", kind)
			}
		}
	}
	observation.setTurnType()
	return observation, nil
}

func newEscalationObservation() EscalationObservation {
	return EscalationObservation{SchemaVersion: EscalationObservationSchema, Messages: make([]EscalationMessage, 0), HistoryComplete: true, TurnType: EscalationTurnMainLoop}
}

func (o *EscalationObservation) setTurnType() {
	if len(o.Messages) == 0 {
		return
	}
	for _, block := range o.Messages[len(o.Messages)-1].Blocks {
		if block.Type == EscalationBlockToolResult {
			o.TurnType = EscalationTurnToolResult
			return
		}
	}
}

func escalationSemanticRole(role string) (EscalationRole, error) {
	switch EscalationRole(role) {
	case EscalationRoleSystem, escalationRoleDeveloper:
		return EscalationRoleSystem, nil
	case EscalationRoleUser:
		return EscalationRoleUser, nil
	case EscalationRoleAssistant, escalationRoleModel:
		return EscalationRoleAssistant, nil
	case EscalationRoleTool, escalationRoleFunction:
		return EscalationRoleTool, nil
	default:
		return "", fmt.Errorf("unsupported observation role %q", role)
	}
}

func escalationContentBlocks(content gjson.Result) ([]EscalationBlock, error) {
	blocks := make([]EscalationBlock, 0)
	if content.Type == gjson.String {
		return append(blocks, EscalationBlock{Type: EscalationBlockText, Text: content.String()}), nil
	}
	if !content.Exists() || content.Type == gjson.Null {
		return blocks, nil
	}
	if !content.IsArray() {
		return nil, fmt.Errorf("observation content must be a string or array")
	}
	for _, block := range content.Array() {
		kind := escalationWireType(block.Get("type").String())
		switch kind {
		case escalationWireText, escalationWireInputText, escalationWireOutputText:
			blocks = append(blocks, EscalationBlock{Type: EscalationBlockText, Text: block.Get("text").String()})
		case escalationWireToolUse:
			if !escalationNamedTool(block) {
				return nil, fmt.Errorf("invalid observation tool use")
			}
			blocks = append(blocks, EscalationBlock{Type: EscalationBlockToolCall, ID: block.Get("id").String(), Name: block.Get("name").String(), ArgumentsJSON: escalationJSON(block.Get("input"))})
		case escalationWireToolResult:
			blocks = append(blocks, EscalationBlock{Type: EscalationBlockToolResult, CallID: block.Get("tool_use_id").String(), ContentJSON: escalationJSON(block.Get("content")), IsError: escalationErrorFlag(block)})
		case escalationWireThinking, escalationWireRedactedThinking, escalationWireReasoning, escalationWireImage, escalationWireImageURL, escalationWireInputImage, escalationWireInputAudio, escalationWireDocument, escalationWireFile, escalationWireInputFile:
		default:
			return nil, fmt.Errorf("unsupported observation content block %q", kind)
		}
	}
	return blocks, nil
}

func escalationGeminiBlocks(parts gjson.Result) ([]EscalationBlock, error) {
	blocks := make([]EscalationBlock, 0)
	for _, part := range parts.Array() {
		if part.Get("thought").Bool() {
			continue
		}
		if part.Get("text").Exists() {
			blocks = append(blocks, EscalationBlock{Type: EscalationBlockText, Text: part.Get("text").String()})
			continue
		}
		call := part.Get("functionCall")
		if !call.Exists() {
			call = part.Get("function_call")
		}
		if call.Exists() {
			if !escalationNamedTool(call) {
				return nil, fmt.Errorf("invalid Gemini function call")
			}
			arguments := call.Get("args")
			if !arguments.Exists() {
				arguments = call.Get("arguments")
			}
			blocks = append(blocks, EscalationBlock{Type: EscalationBlockToolCall, ID: call.Get("id").String(), Name: call.Get("name").String(), ArgumentsJSON: escalationJSON(arguments)})
			continue
		}
		response := part.Get("functionResponse")
		if !response.Exists() {
			response = part.Get("function_response")
		}
		if response.Exists() {
			if !escalationNamedTool(response) {
				return nil, fmt.Errorf("invalid Gemini function response")
			}
			isError := escalationErrorFlag(response)
			if isError == nil {
				isError = escalationErrorFlag(response.Get("response"))
			}
			blocks = append(blocks, EscalationBlock{Type: EscalationBlockToolResult, CallID: response.Get("id").String(), Name: response.Get("name").String(), ContentJSON: escalationJSON(response.Get("response")), IsError: isError})
			continue
		}
		if part.Get("inlineData").Exists() || part.Get("inline_data").Exists() || part.Get("fileData").Exists() || part.Get("file_data").Exists() {
			continue
		}
		return nil, fmt.Errorf("unsupported Gemini observation part")
	}
	return blocks, nil
}

func escalationNamedTool(value gjson.Result) bool {
	name := value.Get("name")
	return value.IsObject() && name.Type == gjson.String && strings.TrimSpace(name.String()) != ""
}

func escalationErrorFlag(value gjson.Result) *bool {
	if flag := value.Get("is_error"); flag.Type == gjson.True || flag.Type == gjson.False {
		verdict := flag.Bool()
		return &verdict
	}
	switch escalationToolStatus(value.Get("status").String()) {
	case escalationToolStatusError, escalationToolStatusFailed:
		verdict := true
		return &verdict
	}
	if value.Get("error").Exists() && value.Get("error").Type != gjson.Null {
		verdict := true
		return &verdict
	}
	return nil
}

func escalationJSON(value gjson.Result) string {
	if !value.Exists() {
		return "null"
	}
	return value.Raw
}

func escalationArguments(value gjson.Result) string {
	if value.Type != gjson.String {
		return escalationJSON(value)
	}
	if json.Valid([]byte(value.String())) {
		return value.String()
	}
	return value.Raw
}
