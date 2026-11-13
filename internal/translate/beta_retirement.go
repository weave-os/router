package translate

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"weave-os/router/internal/requestcontext"
)

// BetaRetiredMessage is shared with historical-control stripping so replies never become prompts.
const BetaRetiredMessage = "Beta has been retired. Internal routing requires authenticated account enrollment."

// WriteRetiredBetaRequest recognizes only the legacy control shape and emits a protocol-local reply.
// Ordinary request bytes are never emitted from the temporary parsing envelope.
func WriteRetiredBetaRequest(w http.ResponseWriter, r *http.Request, body []byte, surface requestcontext.ConversationSurface) (bool, error) {
	var envelope *RequestEnvelope
	var err error
	switch surface {
	case requestcontext.ConversationAnthropic:
		envelope, err = ParseAnthropic(body)
	case requestcontext.ConversationChat:
		envelope, err = ParseOpenAI(body)
	case requestcontext.ConversationResponses:
		var converted ResponsesConversion
		converted, err = ConvertResponsesToChatCompletions(body)
		if err == nil {
			envelope, err = ParseOpenAI(converted.Body)
		}
	case requestcontext.ConversationGemini:
		contents := gjson.GetBytes(body, "contents").Array()
		if len(contents) == 0 || contents[len(contents)-1].Get("role").String() != "user" {
			return false, nil
		}
		last := geminiLastUserMessage(body)
		if last.HasToolResult {
			return false, nil
		}
		_, found, _ := parseBetaCommand(last.Text)
		if !found {
			return false, nil
		}
		return true, writeGeminiRetirement(w, strings.HasSuffix(r.URL.Path, ":streamGenerateContent"))
	default:
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, found := envelope.ExtractBetaCommand(); !found {
		return false, nil
	}
	text := "✦ **Weave Router** → " + BetaRetiredMessage + "\n\n"
	switch surface {
	case requestcontext.ConversationAnthropic:
		return true, WriteSyntheticAnthropicResponse(w, envelope, text, 0)
	case requestcontext.ConversationResponses:
		writer := NewResponsesWriter(w, "weave-router")
		if err := WriteSyntheticOpenAIResponse(writer, envelope, text, 0); err != nil {
			return true, err
		}
		return true, writer.Finalize()
	default:
		return true, WriteSyntheticOpenAIResponse(w, envelope, text, 0)
	}
}

func writeGeminiRetirement(w http.ResponseWriter, stream bool) error {
	reply := map[string]any{"candidates": []any{map[string]any{"index": 0, "content": map[string]any{"role": "model", "parts": []any{map[string]any{"text": BetaRetiredMessage}}}, "finishReason": "STOP"}}}
	payload, err := json.Marshal(reply)
	if err != nil {
		return err
	}
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		_, err = w.Write([]byte(commandOpenAISSEData(string(payload))))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	_, err = w.Write(payload)
	return err
}
