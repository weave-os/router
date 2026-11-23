package proxy

import (
	"context"

	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/translate"
)

func codexSubagentPath(ctx context.Context) string {
	if requestcontext.ClientIdentityFrom(ctx).ClientApp != requestcontext.ClientAppCodex {
		return ""
	}
	native, _ := ctx.Value(nativeResponsesBodyContextKey{}).([]byte)
	return translate.CodexSubagentPath(native)
}

func subAgentHintForRequest(ctx context.Context, hint string) string {
	if hint == "" && codexSubagentPath(ctx) != "" {
		return requestcontext.ClientAppCodex
	}
	return hint
}
