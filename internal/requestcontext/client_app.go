package requestcontext

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

func ClientIdentityFromHeaders(h http.Header) ClientIdentity {
	xApp, eval := splitEvalClientApp(h.Get("X-App"))
	id := ClientIdentity{
		SessionID:   SessionIDFromHeaders(h),
		Email:       NormalizeEmail(h.Get("X-Weave-User-Email")),
		DisplayName: NormalizeDisplayName(h.Get("X-Weave-User-Name")),
		UserAgent:   h.Get("User-Agent"),
		ClientApp:   NormalizeClientApp(xApp, h.Get("User-Agent")),
		Eval:        eval,
		RolloutID:   NormalizeRolloutID(h.Get(RolloutIDHeader)),
	}
	if id.ClientApp == ClientAppOpencode {
		id.OpenCodeSubagent = IsOpenCodeSubagent(h)
		if id.SessionID == "" {
			id.SessionID = OpenCodeSessionIDFromHeaders(h)
		}
	}
	return id
}

func ChatClientIdentity(h http.Header, body []byte) ClientIdentity {
	id := ClientIdentityFromHeaders(h)
	if id.ClientApp == ClientAppCursor && id.SessionID == "" {
		id.SessionID = CursorAgentSessionID(body)
	}
	return id
}

func splitEvalClientApp(xApp string) (string, bool) {
	trimmed := strings.ToLower(strings.TrimSpace(xApp))
	if !strings.HasPrefix(trimmed, EvalClientAppPrefix) || len(trimmed) > MaxClientAppLen {
		return xApp, false
	}
	return strings.TrimPrefix(trimmed, EvalClientAppPrefix), true
}

const MaxEmailLen = 254

const MaxRolloutIDLen = 256

func NormalizeRolloutID(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > MaxRolloutIDLen {
		return ""
	}
	return s
}

func NormalizeEmail(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || len(s) > MaxEmailLen {
		return ""
	}
	at := strings.IndexByte(s, '@')
	if at <= 0 || at == len(s)-1 {
		return ""
	}
	if strings.IndexByte(s[at+1:], '@') >= 0 {
		return ""
	}
	return s
}

const (
	ClientAppClaudeCode = "claude-code"
	ClientAppCodex      = "codex"
	ClientAppCursor     = "cursor"
	ClientAppGeminiCLI  = "gemini-cli"
	ClientAppOpencode   = "opencode"
)

var clientAppAliases = map[string]string{
	"cli":    ClientAppClaudeCode,
	"cli-bg": ClientAppClaudeCode,
}

const MaxClientAppLen = 32

func NormalizeClientApp(xApp, userAgent string) string {
	xApp = strings.ToLower(strings.TrimSpace(xApp))
	if xApp != "" && len(xApp) <= MaxClientAppLen {
		if canonical, ok := clientAppAliases[xApp]; ok {
			return canonical
		}
		return xApp
	}
	ua := strings.ToLower(userAgent)
	switch {
	case ua == "":
		return ""
	case strings.Contains(ua, "claude-cli"):
		return ClientAppClaudeCode
	case strings.Contains(ua, "codex_cli") || strings.Contains(ua, "codex-cli") || strings.HasPrefix(ua, "codex/"):
		return ClientAppCodex
	case strings.Contains(ua, "cursor"):
		return ClientAppCursor
	case strings.Contains(ua, "gemini-cli") || strings.Contains(ua, "google-genai"):
		return ClientAppGeminiCLI
	case strings.Contains(ua, "opencode"):
		return ClientAppOpencode
	}
	return ""
}

const MaxDisplayNameLen = 128

func NormalizeDisplayName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > MaxDisplayNameLen {
		return ""
	}
	if !utf8.ValidString(s) {
		if decoded, err := charmap.ISO8859_1.NewDecoder().String(s); err == nil {
			s = decoded
		}
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return ""
	}
	return out
}
