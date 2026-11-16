package proxy

import (
	"context"
	"net/http"
	"strings"

	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"

	"github.com/tidwall/gjson"
)

type codexSelectedModelContextKey struct{}

const CodexNativeModelPinHeader = "X-Weave-Codex-Native-Model-Pin"

const CodexModelCatalogHeader = "X-Weave-Codex-Model-Catalog"

func codexModelSwitched(header http.Header, conversion translate.ResponsesConversion) bool {
	if header.Get(CodexNativeModelPinHeader) != "1" {
		return false
	}
	return conversion.CodexModelSwitch ||
		(conversion.CodexModelInstructions && header.Get(CodexModelCatalogHeader) == "1")
}

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
	return router.IsRoutingModel(model) || strings.HasPrefix(model, "codex-auto-") || strings.HasPrefix(model, "gpt-daybreak-")
}
