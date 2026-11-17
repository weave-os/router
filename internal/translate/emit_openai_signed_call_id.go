package translate

import (
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func (e *RequestEnvelope) emitOpenAISameFormat(ov EmitOverrides) ([]byte, error) {
	body, err := e.emitSameFormat(ov)
	if err != nil {
		return nil, err
	}
	return clampSignedChatToolCallIDs(body)
}

func clampSignedChatToolCallIDs(body []byte) ([]byte, error) {
	var paths []string
	gjson.GetBytes(body, "messages").ForEach(func(mi, msg gjson.Result) bool {
		if hasThoughtCarrier(msg.Get("tool_call_id").String()) {
			paths = append(paths, fmt.Sprintf("messages.%d.tool_call_id", mi.Int()))
		}
		msg.Get("tool_calls").ForEach(func(ci, call gjson.Result) bool {
			if hasThoughtCarrier(call.Get("id").String()) {
				paths = append(paths, fmt.Sprintf("messages.%d.tool_calls.%d.id", mi.Int(), ci.Int()))
			}
			return true
		})
		return true
	})
	out := body
	for _, path := range paths {
		var err error
		out, err = sjson.SetBytes(out, path, clampOpenAIToolCallID(gjson.GetBytes(out, path).String()))
		if err != nil {
			return nil, fmt.Errorf("clamp signed tool call id at %s: %w", path, err)
		}
	}
	return out, nil
}

func hasThoughtCarrier(id string) bool {
	_, sig := extractSignatureFromID(id)
	return sig != ""
}
