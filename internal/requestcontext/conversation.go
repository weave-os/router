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

// OpenCodeSessionIDFromHeaders reads OpenCode's native session headers. The
// worker consults it only once the client is known to be OpenCode; the gateway
// has no client detection and relies on the header name alone. A subagent resolves
// to its parent session, as Claude Code and Codex subagents share their
// parent's session id; the first user message still separates the subagent's
// pin. OpenCode names only the immediate parent, so a nested subagent resolves
// to its spawning subagent.
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

// IsOpenCodeSubagent reports whether OpenCode's native headers name a child
// session with a parent, which OpenCode sends only on subagent requests.
func IsOpenCodeSubagent(headers http.Header) bool {
	return NormalizeClientIdentifier(headers.Get(OpenCodeSessionHeader)) != "" &&
		NormalizeClientIdentifier(headers.Get(OpenCodeParentSessionHeader)) != ""
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
// then OpenCode's native session headers, then envelope metadata without using
// mutable message content.
// Callers validate/bound the body before calling; ordinary dispatch retains its original bytes.
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

// ConversationFingerprint identifies a conversation whose client sends no session ID.
// It prefers the client's prompt_cache_key, then a hash of the first user message's
// text, which session-less harnesses resend unchanged on every turn. Callers scope the
// result by credential, so identical openers only collide within one caller.
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

// isUserMessage applies the role defaults the Responses and Gemini parsers use:
// a Responses message item and a Gemini content without a role are user turns.
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
	}
	return false
}

// contentText ignores per-turn annotations such as cache_control, and a lone text
// block matches the same text sent as a plain string.
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
