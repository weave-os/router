package requestcontext

func CompletionLogClientApp(id ClientIdentity) string {
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

func SupportsResponsesTerminalSurfaces(clientApp string) bool {
	return clientApp == ClientAppCodex || clientApp == ClientAppOpencode
}

func DirectivePrefix(clientApp string) string {
	if clientApp == ClientAppCodex {
		return "$"
	}
	return "/"
}
