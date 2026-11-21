package proxy

import (
	"context"
	"net/http"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"

	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/turntype"
)

type ClientIdentity = requestcontext.ClientIdentity

type ClientIdentityContextKey = requestcontext.ClientIdentityContextKey

func ClientIdentityFrom(ctx context.Context) ClientIdentity {
	return requestcontext.ClientIdentityFrom(ctx)
}

func ClientIdentityFromHeaders(h http.Header) ClientIdentity {
	xApp, eval := splitEvalClientApp(h.Get("X-App"))
	id := ClientIdentity{
		SessionID:   requestcontext.SessionIDFromHeaders(h),
		Email:       NormalizeEmail(h.Get("X-Weave-User-Email")),
		DisplayName: NormalizeDisplayName(h.Get("X-Weave-User-Name")),
		UserAgent:   h.Get("User-Agent"),
		ClientApp:   NormalizeClientApp(xApp, h.Get("User-Agent")),
		Eval:        eval,
		RolloutID:   NormalizeRolloutID(h.Get(RolloutIDHeader)),
	}
	if id.ClientApp == ClientAppOpencode {
		id.OpenCodeSubagent = requestcontext.IsOpenCodeSubagent(h)
		if id.SessionID == "" {
			id.SessionID = requestcontext.OpenCodeSessionIDFromHeaders(h)
		}
	}
	return id
}

func ChatClientIdentity(h http.Header, body []byte) ClientIdentity {
	id := ClientIdentityFromHeaders(h)
	if id.ClientApp == ClientAppCursor && id.SessionID == "" {
		id.SessionID = requestcontext.CursorAgentSessionID(body)
	}
	return id
}

func openCodeCaller(id ClientIdentity) turntype.OpenCodeCaller {
	isClient := id.ClientApp == ClientAppOpencode
	return turntype.OpenCodeCaller{IsClient: isClient, IsSubagent: isClient && id.OpenCodeSubagent}
}

const EvalClientAppPrefix = requestcontext.EvalClientAppPrefix

func splitEvalClientApp(xApp string) (string, bool) {
	trimmed := strings.ToLower(strings.TrimSpace(xApp))
	if !strings.HasPrefix(trimmed, EvalClientAppPrefix) || len(trimmed) > MaxClientAppLen {
		return xApp, false
	}
	return strings.TrimPrefix(trimmed, EvalClientAppPrefix), true
}

type ClaudeCodeMetadata = requestcontext.ClaudeCodeMetadata

func ParseClaudeCodeMetadata(raw string) ClaudeCodeMetadata {
	return requestcontext.ParseClaudeCodeMetadata(raw)
}

const MaxEmailLen = 254

const MaxClientIdentifierLen = requestcontext.MaxClientIdentifierLen

func NormalizeClientIdentifier(s string) string {
	return requestcontext.NormalizeClientIdentifier(s)
}

const RolloutIDHeader = "X-Weave-Rollout-Id"

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

func completionLogClientApp(id ClientIdentity) string {
	if id.ClientApp == "" {
		return ""
	}
	if id.Eval {
		return "eval"
	}
	switch id.ClientApp {
	case ClientAppClaudeCode:
		return ClientAppClaudeCode
	case ClientAppCodex:
		return ClientAppCodex
	case ClientAppCursor:
		return ClientAppCursor
	case ClientAppGeminiCLI:
		return ClientAppGeminiCLI
	case ClientAppOpencode:
		return ClientAppOpencode
	default:
		return "other"
	}
}

func supportsResponsesTerminalSurfaces(clientApp string) bool {
	return clientApp == ClientAppCodex || clientApp == ClientAppOpencode
}

func directivePrefix(clientApp string) string {
	if clientApp == ClientAppCodex {
		return "$"
	}
	return "/"
}

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
