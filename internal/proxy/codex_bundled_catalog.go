package proxy

import (
	"context"
	_ "embed"
	"net/http"

	"weave-os/router/internal/observability"
)

// codexBundledCatalog is `codex debug models --bundled`; refresh it with
// scripts/refresh_codex_bundled_models.sh when Codex ships new models.
//
//go:embed codex_bundled_models.json
var codexBundledCatalog []byte

// CodexSelectionCatalog serves Codex's model_catalog_url. Codex replaces its
// whole model list with this response, so a caller without a paired ChatGPT
// login (API-key auth) gets the bundled catalog instead of a partial one.
func (s *Service) CodexSelectionCatalog(ctx context.Context, headers http.Header, clientVersion string) ([]byte, error) {
	catalog, err := s.CodexModelCatalog(ctx, headers, clientVersion)
	if err == nil {
		return catalog, nil
	}
	observability.FromContext(ctx).Debug("Serving bundled Codex model catalog", "err", err)
	return addCodexRoutingModels(codexBundledCatalog, internalBetaSession(ctx))
}
