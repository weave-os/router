package proxy

import (
	"context"

	"weave-os/router/internal/router"
	"weave-os/router/internal/translate"
	"weave-os/router/internal/translate/toolcheck"
)

// toolValidatorForRequest compiles the request's tool schemas and applies the
// per-request x-weave-toolcheck mode. Nil when the request has no tools and
// the mode is the default, which the translators treat as syntax-check-only.
func toolValidatorForRequest(ctx context.Context, env *translate.RequestEnvelope) *toolcheck.Validator {
	return env.ToolValidator().WithMode(toolcheck.Mode(router.ToolCheckModeFromContext(ctx)))
}
