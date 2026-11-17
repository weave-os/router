package requestcontext

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
)

type ConversationSurface string

const (
	ConversationAnthropic ConversationSurface = "anthropic_messages"
	ConversationChat      ConversationSurface = "openai_chat"
	ConversationResponses ConversationSurface = "openai_responses"
	ConversationGemini    ConversationSurface = "gemini"
)

const MaxRequestBodyBytes = 64 * 1024 * 1024

const MaxClientIdentifierLen = 128

func NormalizeClientIdentifier(identifier string) string {
	if len(identifier) > MaxClientIdentifierLen {
		return ""
	}
	return identifier
}

func SessionIDFromHeaders(headers http.Header) string {
	for _, name := range [...]string{ClaudeCodeSessionHeader, "Session-Id", "Thread-Id"} {
		if id := NormalizeClientIdentifier(headers.Get(name)); id != "" {
			return id
		}
	}
	return ""
}

func OpenCodeSessionIDFromHeaders(headers http.Header) string {
	session := NormalizeClientIdentifier(headers.Get(OpenCodeSessionHeader))
	if session == "" {
		return ""
	}
	if parent := NormalizeClientIdentifier(headers.Get(OpenCodeParentSessionHeader)); parent != "" {
		return parent
	}
	return session
}

func IsOpenCodeSubagent(headers http.Header) bool {
	return NormalizeClientIdentifier(headers.Get(OpenCodeSessionHeader)) != "" &&
		NormalizeClientIdentifier(headers.Get(OpenCodeParentSessionHeader)) != ""
}

type ClaudeCodeMetadata struct {
	DeviceID  string `json:"device_id"`
	AccountID string `json:"account_uuid"`
	SessionID string `json:"session_id"`
	Email     string `json:"email"`
}

func ParseClaudeCodeMetadata(raw string) ClaudeCodeMetadata {
	var metadata ClaudeCodeMetadata
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
		return ClaudeCodeMetadata{}
	}
	return metadata
}

func AnthropicHeaderSessionID(headers http.Header, metadata ClaudeCodeMetadata) string {
	if metadata.SessionID != "" {
		return NormalizeClientIdentifier(metadata.SessionID)
	}
	return SessionIDFromHeaders(headers)
}

var clientSessionEmbeddedUUID = regexp.MustCompile(`(?i)session[_\-]?(?:id[=:])?([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`)
var clientSessionTrailingUUID = regexp.MustCompile(`([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})$`)

func MetadataSessionID(raw string) string {
	if raw == "" {
		return ""
	}
	if raw[0] == '{' && gjson.Valid(raw) {
		for _, key := range [...]string{"session_id", "sessionId", "conversation_id", "conversationId"} {
			if id := gjson.Get(raw, key).String(); id != "" {
				return id
			}
		}
	}
	if match := clientSessionEmbeddedUUID.FindStringSubmatch(raw); match != nil {
		return match[1]
	}
	if match := clientSessionTrailingUUID.FindStringSubmatch(raw); match != nil {
		return match[1]
	}
	const legacyRawLimit = 64
	if len(raw) > legacyRawLimit {
		return raw[:legacyRawLimit]
	}
	return raw
}

func BodySessionID(body []byte, surface ConversationSurface) string {
	if surface == ConversationChat {
		if raw := gjson.GetBytes(body, "user").String(); raw != "" {
			return MetadataSessionID(raw)
		}
	}
	return MetadataSessionID(gjson.GetBytes(body, "metadata.user_id").String())
}

func CanonicalConversationID(headers http.Header, body []byte, surface ConversationSurface) string {
	id := SessionIDFromHeaders(headers)
	if surface == ConversationAnthropic {
		id = AnthropicHeaderSessionID(headers, ParseClaudeCodeMetadata(gjson.GetBytes(body, "metadata.user_id").String()))
	}
	if id != "" {
		return id
	}
	if id := OpenCodeSessionIDFromHeaders(headers); id != "" {
		return id
	}
	return BodySessionID(body, surface)
}

func ConversationFingerprint(body []byte, surface ConversationSurface) string {
	if surface == ConversationResponses || surface == ConversationChat {
		if key := NormalizeClientIdentifier(gjson.GetBytes(body, "prompt_cache_key").String()); key != "" {
			return "prompt_cache_key:" + key
		}
	}
	opener := firstUserMessageText(body, surface)
	if opener == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(opener))
	return "first_user_message:" + hex.EncodeToString(sum[:])
}

func firstUserMessageText(body []byte, surface ConversationSurface) string {
	messages, contentField := gjson.GetBytes(body, "messages"), "content"
	switch surface {
	case ConversationResponses:
		messages = gjson.GetBytes(body, "input")
		if messages.Type == gjson.String {
			return messages.String()
		}
	case ConversationGemini:
		messages, contentField = gjson.GetBytes(body, "contents"), "parts"
	case ConversationAnthropic, ConversationChat:
	}
	var text string
	messages.ForEach(func(_, message gjson.Result) bool {
		if !isUserMessage(message, surface) {
			return true
		}
		text = contentText(message.Get(contentField))
		return false
	})
	return text
}

var cursorAgentStorePattern = regexp.MustCompile(`Current agent's store: [^\n]*?cursor_agent_stores/([0-9a-fA-F-]{36})/`)

func CursorAgentSessionID(body []byte) string {
	if m := cursorAgentStorePattern.FindStringSubmatch(firstUserMessageText(body, ConversationChat)); m != nil {
		return NormalizeClientIdentifier(m[1])
	}
	return ""
}

func isUserMessage(message gjson.Result, surface ConversationSurface) bool {
	role := message.Get("role").String()
	if role == "user" {
		return true
	}
	if role != "" {
		return false
	}
	switch surface {
	case ConversationResponses:
		return message.Get("type").String() == "message"
	case ConversationGemini:
		return true
	case ConversationAnthropic, ConversationChat:
	}
	return false
}

func contentText(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	var parts []string
	content.ForEach(func(_, part gjson.Result) bool {
		if value := part.Get("text"); value.Type == gjson.String {
			parts = append(parts, value.String())
		}
		return true
	})
	return strings.Join(parts, "\x00")
}
