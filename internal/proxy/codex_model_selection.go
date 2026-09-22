package proxy

import (
	"context"
	"strings"

	"weave-os/router/internal/router"

	"github.com/tidwall/gjson"
)

type codexSelectedModelContextKey struct{}

// CodexNativeModelPinHeader opts installed Codex clients into interpreting
// their native model selection as an explicit force rather than a baseline.
const CodexNativeModelPinHeader = "X-Weave-Codex-Native-Model-Pin"

// The client repeats its selected model on every request, so a request-scoped
// force follows /model changes immediately without writing a separate pin.
func withCodexSelectedModel(ctx context.Context, model string, nativeBody []byte) context.Context {
	if isCodexAutomaticModel(model) {
		return ctx
	}
	selected := model
	effort := gjson.GetBytes(nativeBody, "reasoning.effort").String()
	if router.IsValidEffort(effort) {
		selected += ":" + router.CanonicalizeEffort(effort)
	}
	return context.WithValue(ctx, codexSelectedModelContextKey{}, selected)
}

func codexSelectedModel(ctx context.Context) string {
	selected, _ := ctx.Value(codexSelectedModelContextKey{}).(string)
	return selected
}

func isCodexAutomaticModel(model string) bool {
	return model == CodexAutomaticModel || strings.HasPrefix(model, "codex-auto-") || strings.HasPrefix(model, "gpt-daybreak-")
}
