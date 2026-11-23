package requestcontext

import (
	"context"
	"strings"

	"weave-os/router/internal/router"

	"github.com/tidwall/gjson"
)

type codexSelectedModelContextKey struct{}

func WithCodexSelectedModel(ctx context.Context, model string, nativeBody []byte) context.Context {
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

func CodexSelectedModel(ctx context.Context) string {
	selected, _ := ctx.Value(codexSelectedModelContextKey{}).(string)
	return selected
}

func isCodexAutomaticModel(model string) bool {
	return router.IsRoutingModel(model) || strings.HasPrefix(model, "codex-auto-") || strings.HasPrefix(model, "gpt-daybreak-")
}
