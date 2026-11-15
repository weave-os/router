package translate

import (
	"bytes"
	"strings"

	"weave-os/router/internal/observability"

	"github.com/tidwall/gjson"
)

const previewMaxChars = 120

const contentBytesPerToken = 4

const fullBytesPerToken = 6

const imageTokenEstimate = 1600

var signatureFieldMarker = []byte(`"signature":"`)

type RoutingFeatures struct {
	Tokens              int
	Model               string
	HasTools            bool
	HasImages           bool
	ImageCount          int
	PromptText          string
	MessageCount        int
	MaxTokens           int
	TitleGenHint        bool
	LastKind            string
	LastPreview         string
	OnlyUserMessageText string
}

func (e *RequestEnvelope) FullTokenEstimate() int {
	imgBytes, imgCount := e.base64ImageStats()
	signatureBytes, idBytes := e.routerReasoningTransportBytes()
	return (len(e.body)-imgBytes-signatureBytes-idBytes)/fullBytesPerToken + imgCount*imageTokenEstimate
}

func (e *RequestEnvelope) ContextOverflowTokenEstimate() int {
	imgBytes, imgCount := e.base64ImageStats()
	signatureBytes, idBytes := e.routerReasoningTransportBytes()
	return (len(e.body)-imgBytes-signatureBytes-idBytes)/contentBytesPerToken + imgCount*imageTokenEstimate
}

func (e *RequestEnvelope) SignatureTokenSavings() int {
	if e.format != FormatAnthropic {
		return 0
	}
	routerSignatureBytes, _ := e.routerReasoningTransportBytes()
	return max(0, base64SignatureBytes(e.body)-routerSignatureBytes) / contentBytesPerToken
}

func (e *RequestEnvelope) routerReasoningTransportBytes() (signatureBytes, idBytes int) {
	if e.format != FormatAnthropic {
		return 0, 0
	}
	carrierBytes := func(id string) int {
		if clean, stripped := stripOpenAIReasoningCarrier(id); stripped {
			return len(id) - len(clean)
		}
		return 0
	}
	gjson.GetBytes(e.body, "messages").ForEach(func(_, message gjson.Result) bool {
		message.Get("content").ForEach(func(_, block gjson.Result) bool {
			switch block.Get("type").String() {
			case "thinking":
				signature := block.Get("signature").String()
				if _, ok := decodeOpenAIReasoningSignature(signature); ok {
					signatureBytes += len(signature)
				}
			case "tool_use":
				idBytes += carrierBytes(block.Get("id").String())
			case "tool_result":
				idBytes += carrierBytes(block.Get("tool_use_id").String())
			}
			return true
		})
		return true
	})
	return signatureBytes, idBytes
}

func base64SignatureBytes(body []byte) int {
	total := 0
	for i := 0; ; {
		rel := bytes.Index(body[i:], signatureFieldMarker)
		if rel < 0 {
			break
		}
		start := i + rel + len(signatureFieldMarker)
		end := bytes.IndexByte(body[start:], '"')
		if end < 0 {
			break
		}
		total += end
		i = start + end + 1
	}
	return total
}

func (e *RequestEnvelope) RoutingFeatures() RoutingFeatures {
	switch e.format {
	case FormatAnthropic:
		return e.anthropicRoutingFeatures()
	case FormatOpenAI:
		return e.openAIRoutingFeatures()
	case FormatGemini:
		return e.geminiRoutingFeatures()
	default:
		return RoutingFeatures{}
	}
}

func (e *RequestEnvelope) anthropicRoutingFeatures() RoutingFeatures {
	var b strings.Builder
	appendText(&b, systemTextGJSON(gjson.GetBytes(e.body, "system")))

	msgs := gjson.GetBytes(e.body, "messages")
	var (
		msgCount  int
		lastMsg   gjson.Result
		onlyUserB strings.Builder
	)
	msgs.ForEach(func(_, msg gjson.Result) bool {
		msgCount++
		appendText(&b, contentTextGJSON(msg.Get("content")))
		lastMsg = msg
		if msg.Get("role").String() == "user" {
			if text := userPromptTextGJSON(msg.Get("content")); text != "" {
				appendText(&onlyUserB, text)
			}
		}
		return true
	})
	text := b.String()

	imageCount := e.ImageCount()
	feats := RoutingFeatures{
		Tokens:       len(text) / 4,
		Model:        e.Model(),
		HasTools:     e.HasTools(),
		HasImages:    imageCount > 0,
		ImageCount:   imageCount,
		PromptText:   text,
		MessageCount: msgCount,
		MaxTokens:    intGJSON(gjson.GetBytes(e.body, "max_tokens")),
	}

	if msgCount > 0 {
		feats.LastKind = classifyLastMessageGJSON(lastMsg.Get("role").String(), lastMsg.Get("content"))
		feats.LastPreview = previewGJSON(lastMsg.Get("content"))
	}

	if onlyUserB.Len() > 0 {
		feats.OnlyUserMessageText = onlyUserB.String()
	}

	return feats
}

func (e *RequestEnvelope) openAIRoutingFeatures() RoutingFeatures {
	msgs := gjson.GetBytes(e.body, "messages")

	var b strings.Builder
	var (
		msgCount  int
		lastMsg   gjson.Result
		onlyUserB strings.Builder
	)
	msgs.ForEach(func(_, msg gjson.Result) bool {
		msgCount++
		appendText(&b, openAIContentTextGJSON(msg.Get("content")))
		lastMsg = msg
		if msg.Get("role").String() == "user" {
			if text := openAIContentTextGJSON(msg.Get("content")); text != "" {
				appendText(&onlyUserB, text)
			}
		}
		return true
	})
	text := b.String()

	imageCount := e.ImageCount()
	feats := RoutingFeatures{
		Tokens:       len(text) / 4,
		Model:        e.Model(),
		HasTools:     e.HasTools(),
		HasImages:    imageCount > 0,
		ImageCount:   imageCount,
		PromptText:   text,
		MessageCount: msgCount,
		MaxTokens:    openAIMaxTokens(e.body),
	}

	if msgCount > 0 {
		feats.LastKind = classifyLastMessageOpenAI(lastMsg.Get("role").String())
		feats.LastPreview = previewText(openAIContentTextGJSON(lastMsg.Get("content")))
	}

	if onlyUserB.Len() > 0 {
		feats.OnlyUserMessageText = onlyUserB.String()
	}

	return feats
}

func intGJSON(v gjson.Result) int {
	if !v.Exists() || v.Type != gjson.Number {
		return 0
	}
	n := v.Int()
	if n < 0 {
		return 0
	}
	return int(n)
}

func openAIMaxTokens(body []byte) int {
	if n := intGJSON(gjson.GetBytes(body, "max_completion_tokens")); n > 0 {
		return n
	}
	return intGJSON(gjson.GetBytes(body, "max_tokens"))
}

func classifyLastMessageOpenAI(role string) string {
	switch role {
	case "assistant":
		return "assistant"
	case "tool":
		return "tool_result"
	default:
		return "user_prompt"
	}
}

func previewText(text string) string {
	if text == "" {
		return ""
	}
	return observability.Preview(strings.Join(strings.Fields(text), " "), previewMaxChars)
}

func anthropicLastUserMessage(body []byte) LastUserMessageInfo {
	msgs := gjson.GetBytes(body, "messages")
	if !msgs.IsArray() {
		return LastUserMessageInfo{}
	}
	var lastUser gjson.Result
	msgs.ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() == "user" {
			lastUser = msg
		}
		return true
	})
	if !lastUser.Exists() {
		return LastUserMessageInfo{}
	}
	content := lastUser.Get("content")
	if content.Type == gjson.String {
		s := content.String()
		return LastUserMessageInfo{HasText: s != "", Text: s}
	}
	if !content.IsArray() {
		return LastUserMessageInfo{}
	}
	info := LastUserMessageInfo{}
	var b strings.Builder
	content.ForEach(func(_, block gjson.Result) bool {
		switch block.Get("type").String() {
		case "tool_result":
			info.HasToolResult = true
			info.ToolResultCount++
			info.ToolResultBytes += len(block.Get("content").Raw)
		case "text":
			text := block.Get("text").String()
			if text == "" {
				return true
			}
			info.HasText = true
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(text)
		}
		return true
	})
	if info.HasText {
		info.Text = b.String()
	}
	return info
}

func openAILastUserMessage(body []byte) LastUserMessageInfo {
	msgs := gjson.GetBytes(body, "messages")
	if !msgs.IsArray() {
		return LastUserMessageInfo{}
	}
	all := msgs.Array()
	if len(all) == 0 {
		return LastUserMessageInfo{}
	}
	info := LastUserMessageInfo{}
	for i := len(all) - 1; i >= 0; i-- {
		role := all[i].Get("role").String()
		switch role {
		case "tool":
			info.HasToolResult = true
			info.ToolResultCount++
			info.ToolResultBytes += len(all[i].Get("content").Raw)
			continue
		case "user":
			text := openAIContentTextGJSON(all[i].Get("content"))
			if text != "" {
				info.HasText = true
				info.Text = text
			}
		}
		break
	}
	return info
}

func openAISystemText(body []byte) string {
	msgs := gjson.GetBytes(body, "messages")
	if !msgs.IsArray() {
		return ""
	}
	var b strings.Builder
	msgs.ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() != "system" {
			return true
		}
		text := openAIContentTextGJSON(msg.Get("content"))
		if text == "" {
			return true
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(text)
		return true
	})
	return b.String()
}

func systemTextGJSON(v gjson.Result) string {
	if !v.Exists() {
		return ""
	}
	if v.Type == gjson.String {
		return v.String()
	}
	return contentTextGJSON(v)
}

func contentTextGJSON(v gjson.Result) string {
	if !v.Exists() {
		return ""
	}
	if v.Type == gjson.String {
		return v.String()
	}
	if !v.IsArray() {
		return ""
	}
	var b strings.Builder
	v.ForEach(func(_, block gjson.Result) bool {
		text := block.Get("text").String()
		if text == "" {
			return true
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(text)
		return true
	})
	return b.String()
}

func classifyLastMessageGJSON(role string, content gjson.Result) string {
	if role == "assistant" {
		return "assistant"
	}
	if !content.IsArray() {
		return "user_prompt"
	}
	hasToolResult := false
	content.ForEach(func(_, block gjson.Result) bool {
		if block.Get("type").String() == "tool_result" {
			hasToolResult = true
			return false
		}
		return true
	})
	if hasToolResult {
		return "tool_result"
	}
	return "user_prompt"
}

func previewGJSON(content gjson.Result) string {
	return previewText(contentTextGJSON(content))
}

func userPromptTextGJSON(content gjson.Result) string {
	return userPromptTextWithClientWrappers(content, false)
}

func userPromptTextWithClientWrappers(content gjson.Result, normalizeKnownWrappers bool) string {
	if !content.Exists() {
		return ""
	}
	if content.Type == gjson.String {
		if normalizeKnownWrappers {
			return WithoutLeadingClientInjectedText(content.String())
		}
		return content.String()
	}
	if !content.IsArray() {
		return ""
	}
	var b strings.Builder
	content.ForEach(func(_, block gjson.Result) bool {
		if block.Get("type").String() == "tool_result" {
			return true
		}
		text := block.Get("text").String()
		if text == "" {
			return true
		}
		if isClaudeCodeInjectedBlock(text) {
			if !normalizeKnownWrappers {
				return true
			}
			text = WithoutLeadingClientInjectedText(text)
			if text == "" {
				return true
			}
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(text)
		return true
	})
	return b.String()
}

var claudeCodeInjectedBlockPrefixes = []string{
	"<system-reminder>",
	"<command-name>",
	"<command-message>",
	"<command-args>",
	"<local-command-stdout>",
	"<local-command-stderr>",
	"<local-command-caveat>",
	"<task-notification>",
	"</summary>\n<result>",
}

func isClaudeCodeInjectedBlock(text string) bool {
	trimmed := strings.TrimLeft(text, " \t\r\n")
	for _, prefix := range claudeCodeInjectedBlockPrefixes {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

func leadingClaudeCodeInjectedBlockEnd(trimmed string) int {
	if strings.HasPrefix(trimmed, "</summary>\n<result>") {
		resultEnd := strings.Index(trimmed, "</result>")
		if resultEnd < 0 {
			return 0
		}
		blockEnd := resultEnd + len("</result>")
		remainder := strings.TrimLeft(trimmed[blockEnd:], " \t\r\n")
		if strings.HasPrefix(remainder, "<usage>") {
			usageEnd := strings.Index(remainder, "</usage>")
			if usageEnd < 0 {
				return 0
			}
			blockEnd = len(trimmed) - len(remainder) + usageEnd + len("</usage>")
		}
		return blockEnd
	}
	if !isClaudeCodeInjectedBlock(trimmed) {
		return 0
	}
	openingTagEnd := strings.IndexByte(trimmed, '>')
	if openingTagEnd < 0 {
		return 0
	}
	closingTag := "</" + trimmed[1:openingTagEnd] + ">"
	closingTagStart := strings.Index(trimmed[openingTagEnd+1:], closingTag)
	if closingTagStart < 0 {
		return 0
	}
	return openingTagEnd + 1 + closingTagStart + len(closingTag)
}

func openAIContentTextGJSON(v gjson.Result) string {
	if !v.Exists() {
		return ""
	}
	if v.Type == gjson.String {
		return v.String()
	}
	if !v.IsArray() {
		return ""
	}
	var b strings.Builder
	v.ForEach(func(_, part gjson.Result) bool {
		if part.Get("type").String() != "text" {
			return true
		}
		text := part.Get("text").String()
		if text == "" {
			return true
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(text)
		return true
	})
	return b.String()
}

func appendText(b *strings.Builder, s string) {
	if s == "" {
		return
	}
	if b.Len() > 0 {
		b.WriteByte('\n')
	}
	b.WriteString(s)
}

func anthropicImageCount(body []byte) int {
	count := 0
	gjson.GetBytes(body, "messages").ForEach(func(_, msg gjson.Result) bool {
		content := msg.Get("content")
		if !content.IsArray() {
			return true
		}
		content.ForEach(func(_, block gjson.Result) bool {
			switch block.Get("type").String() {
			case "image":
				count++
			case "tool_result":
				block.Get("content").ForEach(func(_, inner gjson.Result) bool {
					if inner.Get("type").String() == "image" {
						count++
					}
					return true
				})
			}
			return true
		})
		return true
	})
	return count
}

func openAIImageCount(body []byte) int {
	count := 0
	gjson.GetBytes(body, "messages").ForEach(func(_, msg gjson.Result) bool {
		content := msg.Get("content")
		if !content.IsArray() {
			return true
		}
		content.ForEach(func(_, part gjson.Result) bool {
			if part.Get("type").String() == "image_url" {
				count++
			}
			return true
		})
		return true
	})
	return count
}
