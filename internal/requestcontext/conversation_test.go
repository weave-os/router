package requestcontext_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/requestcontext"
)

func TestCanonicalConversationIdentityGolden(t *testing.T) {
	const sessionUUID = "12345678-1234-1234-1234-123456789abc"
	for _, test := range []struct {
		name    string
		surface requestcontext.ConversationSurface
		headers map[string]string
		body    string
		want    string
	}{
		{"claude header precedence", requestcontext.ConversationAnthropic, map[string]string{"X-Claude-Code-Session-Id": "claude", "Session-Id": "codex", "Thread-Id": "thread"}, `{}`, "claude"},
		{"claude body overlay", requestcontext.ConversationAnthropic, map[string]string{"X-Claude-Code-Session-Id": "header"}, `{"metadata":{"user_id":"{\"session_id\":\"body\",\"account_uuid\":\"untrusted\"}"}}`, "body"},
		{"claude bundled uuid", requestcontext.ConversationAnthropic, nil, `{"metadata":{"user_id":"user_account_session_` + sessionUUID + `"}}`, sessionUUID},
		{"claude alternate json key keeps header", requestcontext.ConversationAnthropic, map[string]string{"Session-Id": "header"}, `{"metadata":{"user_id":"{\"conversationId\":\"body\"}"}}`, "header"},
		{"claude alternate json fallback", requestcontext.ConversationAnthropic, nil, `{"metadata":{"user_id":"{\"conversationId\":\"body\"}"}}`, "body"},
		{"chat header before user", requestcontext.ConversationChat, map[string]string{"Session-Id": "header"}, `{"user":"body"}`, "header"},
		{"chat user before metadata", requestcontext.ConversationChat, nil, `{"user":"body","metadata":{"user_id":"other"}}`, "body"},
		{"chat legacy raw bound", requestcontext.ConversationChat, nil, `{"user":"` + strings.Repeat("a", 65) + `"}`, strings.Repeat("a", 64)},
		{"oversized header falls through", requestcontext.ConversationResponses, map[string]string{"X-Claude-Code-Session-Id": strings.Repeat("a", 129), "Session-Id": "codex"}, `{}`, "codex"},
		{"responses thread fallback", requestcontext.ConversationResponses, map[string]string{"Thread-Id": "thread"}, `{"input":"compacted text"}`, "thread"},
		{"responses does not project user", requestcontext.ConversationResponses, nil, `{"user":"ignored","metadata":{"user_id":"body"}}`, "body"},
		{"gemini header before metadata", requestcontext.ConversationGemini, map[string]string{"Session-Id": "header"}, `{"metadata":{"user_id":"body"}}`, "header"},
		{"gemini metadata", requestcontext.ConversationGemini, nil, `{"metadata":{"user_id":"prefix_` + sessionUUID + `"}}`, sessionUUID},
		{"no first-message fallback", requestcontext.ConversationAnthropic, nil, `{"messages":[{"role":"user","content":"hello"}]}`, ""},
		{"opencode session", requestcontext.ConversationResponses, map[string]string{"X-OpenCode-Session": "ses_opencode"}, `{}`, "ses_opencode"},
		{"opencode subagent binds parent", requestcontext.ConversationResponses, map[string]string{"X-OpenCode-Session": "ses_child", "X-Parent-Session-Id": "ses_parent"}, `{}`, "ses_parent"},
		{"standard header before opencode", requestcontext.ConversationResponses, map[string]string{"Session-Id": "plugin", "X-OpenCode-Session": "ses_opencode"}, `{}`, "plugin"},
		{"opencode before metadata", requestcontext.ConversationResponses, map[string]string{"X-OpenCode-Session": "ses_opencode"}, `{"metadata":{"user_id":"body"}}`, "ses_opencode"},
	} {
		t.Run(test.name, func(t *testing.T) {
			headers := make(http.Header)
			for name, value := range test.headers {
				headers.Set(name, value)
			}
			assert.Equal(t, test.want, requestcontext.CanonicalConversationID(headers, []byte(test.body), test.surface))
		})
	}
}

func TestOpenCodeSessionIDFromHeaders(t *testing.T) {
	for _, test := range []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{"own session", map[string]string{"X-OpenCode-Session": "ses_opencode"}, "ses_opencode"},
		{"subagent uses parent session", map[string]string{"X-OpenCode-Session": "ses_child", "X-Parent-Session-Id": "ses_parent"}, "ses_parent"},
		{"parent session alone is ignored", map[string]string{"X-Parent-Session-Id": "ses_parent"}, ""},
		{"oversized parent falls back to own session", map[string]string{"X-OpenCode-Session": "ses_child", "X-Parent-Session-Id": strings.Repeat("a", 129)}, "ses_child"},
		{"oversized session", map[string]string{"X-OpenCode-Session": strings.Repeat("a", 129)}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			headers := make(http.Header)
			for name, value := range test.headers {
				headers.Set(name, value)
			}
			assert.Equal(t, test.want, requestcontext.OpenCodeSessionIDFromHeaders(headers))
		})
	}
}

func TestConversationIdentitySurvivesCompactionAndSubagents(t *testing.T) {
	headers := make(http.Header)
	headers.Set("Session-Id", "conversation")
	for _, body := range []string{`{"messages":[{"role":"user","content":"first"}]}`, `{"messages":[{"role":"user","content":"compacted"}]}`, `{"messages":[{"role":"user","content":"subagent"}]}`} {
		assert.Equal(t, "conversation", requestcontext.CanonicalConversationID(headers, []byte(body), requestcontext.ConversationChat))
	}
}
