package proxy

import (
	"context"
	_ "embed"
	"net/http"

	"weave-os/router/internal/observability"
)

//go:embed codex_bundled_models.json
var codexBundledCatalog []byte

func (s *Service) CodexSelectionCatalog(ctx context.Context, headers http.Header, clientVersion string) ([]byte, error) {
	catalog, err := s.CodexModelCatalog(ctx, headers, clientVersion)
	if err == nil {
		return catalog, nil
	}
	observability.FromContext(ctx).Debug("Serving bundled Codex model catalog", "err", err)
	return addCodexRoutingModels(codexBundledCatalog, internalBetaSession(ctx))
}
