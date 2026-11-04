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

// CodexNativeModelPinHeader opts installed Codex clients into interpreting a
// /model switch made during the session as an explicit force. The model a
// session launches with (config.toml, SDK thread options) stays a baseline.
const CodexNativeModelPinHeader = "X-Weave-Codex-Native-Model-Pin"

// CodexModelCatalogHeader marks Codex clients that load the router's
// model_catalog_url, whose /model switches arrive as <model_instructions>.
const CodexModelCatalogHeader = "X-Weave-Codex-Model-Catalog"

func codexModelSwitched(header http.Header, conversion translate.ResponsesConversion) bool {
	if header.Get(CodexNativeModelPinHeader) != "1" {
		return false
	}
	return conversion.CodexModelSwitch ||
		(conversion.CodexModelInstructions && header.Get(CodexModelCatalogHeader) == "1")
}

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
	return router.IsRoutingModel(model) || strings.HasPrefix(model, "codex-auto-") || strings.HasPrefix(model, "gpt-daybreak-")
}
