package requestcontext

import (
	"encoding/json"
	"net/http"
	"regexp"

	"github.com/tidwall/gjson"
)

// ConversationSurface identifies ingress semantics, not the selected upstream protocol.
type ConversationSurface string

const (
	ConversationAnthropic ConversationSurface = "anthropic_messages"
	ConversationChat      ConversationSurface = "openai_chat"
	ConversationResponses ConversationSurface = "openai_responses"
	ConversationGemini    ConversationSurface = "gemini"
)

// MaxRequestBodyBytes matches Anthropic's 32 MiB limit across gateway and worker ingress.
const MaxRequestBodyBytes = 32 * 1024 * 1024

// MaxClientIdentifierLen preserves the existing header/Claude Code overlay bound.
const MaxClientIdentifierLen = 128

// NormalizeClientIdentifier rejects oversized identifiers rather than changing their identity.
func NormalizeClientIdentifier(identifier string) string {
	if len(identifier) > MaxClientIdentifierLen {
		return ""
	}
	return identifier
}

// SessionIDFromHeaders preserves first-usable-header precedence across all ingress surfaces.
func SessionIDFromHeaders(headers http.Header) string {
	for _, name := range [...]string{ClaudeCodeSessionHeader, "Session-Id", "Thread-Id"} {
		if id := NormalizeClientIdentifier(headers.Get(name)); id != "" {
			return id
		}
	}
	return ""
}

// ClaudeCodeMetadata is caller-asserted attribution, never authenticated account ownership.
type ClaudeCodeMetadata struct {
	DeviceID  string `json:"device_id"`
	AccountID string `json:"account_uuid"`
	SessionID string `json:"session_id"`
	Email     string `json:"email"`
}

// ParseClaudeCodeMetadata retains the legacy all-or-nothing JSON overlay semantics.
func ParseClaudeCodeMetadata(raw string) ClaudeCodeMetadata {
	var metadata ClaudeCodeMetadata
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
		return ClaudeCodeMetadata{}
	}
	return metadata
}

// AnthropicHeaderSessionID applies the Messages handler's body overlay before envelope fallback.
func AnthropicHeaderSessionID(headers http.Header, metadata ClaudeCodeMetadata) string {
	if metadata.SessionID != "" {
		return NormalizeClientIdentifier(metadata.SessionID)
	}
	return SessionIDFromHeaders(headers)
}

var clientSessionEmbeddedUUID = regexp.MustCompile(`(?i)session[_\-]?(?:id[=:])?([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`)
var clientSessionTrailingUUID = regexp.MustCompile(`([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})$`)

// MetadataSessionID preserves legacy envelope extraction, including its raw-value fallback.
// Its bounds intentionally differ from the header overlay; changing them would break stored pins.
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

// BodySessionID reads only conversation metadata; it never incorporates mutable message content.
func BodySessionID(body []byte, surface ConversationSurface) string {
	if surface == ConversationChat {
		if raw := gjson.GetBytes(body, "user").String(); raw != "" {
			return MetadataSessionID(raw)
		}
	}
	// Responses conversion retains metadata but does not project the top-level user field.
	return MetadataSessionID(gjson.GetBytes(body, "metadata.user_id").String())
}

// CanonicalConversationID applies header precedence and the Anthropic body overlay,
// then falls back to envelope metadata without using mutable message content.
// Callers validate/bound the body before calling; ordinary dispatch retains its original bytes.
func CanonicalConversationID(headers http.Header, body []byte, surface ConversationSurface) string {
	id := SessionIDFromHeaders(headers)
	if surface == ConversationAnthropic {
		id = AnthropicHeaderSessionID(headers, ParseClaudeCodeMetadata(gjson.GetBytes(body, "metadata.user_id").String()))
	}
	if id != "" {
		return id
	}
	return BodySessionID(body, surface)
}
