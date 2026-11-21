package proxy

import (
	"context"

	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/turntype"
)

type ClientIdentity = requestcontext.ClientIdentity

type ClientIdentityContextKey = requestcontext.ClientIdentityContextKey

func ClientIdentityFrom(ctx context.Context) ClientIdentity {
	return requestcontext.ClientIdentityFrom(ctx)
}

func openCodeCaller(id ClientIdentity) turntype.OpenCodeCaller {
	isClient := id.ClientApp == requestcontext.ClientAppOpencode
	return turntype.OpenCodeCaller{IsClient: isClient, IsSubagent: isClient && id.OpenCodeSubagent}
}

type ClaudeCodeMetadata = requestcontext.ClaudeCodeMetadata

func ParseClaudeCodeMetadata(raw string) ClaudeCodeMetadata {
	return requestcontext.ParseClaudeCodeMetadata(raw)
}

const MaxClientIdentifierLen = requestcontext.MaxClientIdentifierLen

func NormalizeClientIdentifier(s string) string {
	return requestcontext.NormalizeClientIdentifier(s)
}

func completionLogClientApp(id ClientIdentity) string {
	if id.ClientApp == "" {
		return ""
	}
	if id.Eval {
		return "eval"
	}
	switch id.ClientApp {
	case requestcontext.ClientAppClaudeCode:
		return requestcontext.ClientAppClaudeCode
	case requestcontext.ClientAppCodex:
		return requestcontext.ClientAppCodex
	case requestcontext.ClientAppCursor:
		return requestcontext.ClientAppCursor
	case requestcontext.ClientAppGeminiCLI:
		return requestcontext.ClientAppGeminiCLI
	case requestcontext.ClientAppOpencode:
		return requestcontext.ClientAppOpencode
	default:
		return "other"
	}
}

func supportsResponsesTerminalSurfaces(clientApp string) bool {
	return clientApp == requestcontext.ClientAppCodex || clientApp == requestcontext.ClientAppOpencode
}

func directivePrefix(clientApp string) string {
	if clientApp == requestcontext.ClientAppCodex {
		return "$"
	}
	return "/"
}
