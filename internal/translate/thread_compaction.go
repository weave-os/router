package translate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func (e *RequestEnvelope) anthropicMessages() []gjson.Result {
	if e == nil || e.format != FormatAnthropic {
		return nil
	}
	return gjson.GetBytes(e.body, "messages").Array()
}

func (e *RequestEnvelope) MessageCount() int {
	return len(e.anthropicMessages())
}

func (e *RequestEnvelope) MessageRole(i int) string {
	msgs := e.anthropicMessages()
	if i < 0 || i >= len(msgs) {
		return ""
	}
	return msgs[i].Get("role").String()
}

func (e *RequestEnvelope) MessageDigest(i int) string {
	msgs := e.anthropicMessages()
	if i < 0 || i >= len(msgs) {
		return ""
	}
	h := sha256.New()
	h.Write([]byte(msgs[i].Get("role").String()))
	content := msgs[i].Get("content")
	if !content.IsArray() {
		h.Write([]byte(content.Raw))
		return hex.EncodeToString(h.Sum(nil))
	}
	for _, block := range content.Array() {
		stripped, err := sjson.Delete(block.Raw, "cache_control")
		if err != nil {
			stripped = block.Raw
		}
		h.Write([]byte(stripped))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (e *RequestEnvelope) ReplacePrefixWithSummary(n int, summary string) bool {
	msgs := e.anthropicMessages()
	if n <= 0 || n >= len(msgs) {
		return false
	}
	text, _ := json.Marshal(HandoverSummaryTag + summary)
	rebuilt := make([]string, 0, len(msgs)-n+1)
	rebuilt = append(rebuilt, `{"role":"user","content":[{"type":"text","text":`+string(text)+`}]}`)
	for _, m := range msgs[n:] {
		rebuilt = append(rebuilt, m.Raw)
	}
	return e.setMessages(rebuilt, 1) == 1
}

func (e *RequestEnvelope) MessagesShareFrom(i int) float64 {
	msgs := e.anthropicMessages()
	if len(e.body) == 0 || i < 0 || i > len(msgs) {
		return 1
	}
	kept := len(e.body) - len(gjson.GetBytes(e.body, "messages").Raw)
	for _, m := range msgs[i:] {
		kept += len(m.Raw)
	}
	return float64(kept) / float64(len(e.body))
}
