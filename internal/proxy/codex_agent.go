package proxy

import (
	"context"

	"weave-os/router/internal/translate"
)

func codexSubagentPath(ctx context.Context) string {
	if ClientIdentityFrom(ctx).ClientApp != ClientAppCodex {
		return ""
	}
	native, _ := ctx.Value(nativeResponsesBodyContextKey{}).([]byte)
	return translate.CodexSubagentPath(native)
}

func subAgentHintForRequest(ctx context.Context, hint string) string {
	if hint == "" && codexSubagentPath(ctx) != "" {
		return ClientAppCodex
	}
	return hint
}
