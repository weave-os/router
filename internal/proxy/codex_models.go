package proxy

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// CodexAutomaticModel is the native /model choice that keeps Weave routing automatic.
const CodexAutomaticModel = "weave-auto"

// CodexModelCatalog forwards Codex's account-specific catalog and adds the
// automatic Weave choice. A visible one-entry response would make Codex hide
// its native models, because visible remote catalogs replace its bundled list.
func (s *Service) CodexModelCatalog(ctx context.Context, headers http.Header, clientVersion string) ([]byte, error) {
	client, err := s.provider(providers.ProviderOpenAI)
	if err != nil {
		return nil, err
	}
	fetcher, ok := client.(interface {
		FetchCodexModelCatalog(context.Context, string) ([]byte, error)
	})
	if !ok {
		return nil, fmt.Errorf("OpenAI provider does not support Codex model discovery")
	}
	// Model-specific credential resolution deliberately rejects an empty model.
	// Discovery instead uses only the caller's paired ChatGPT credential.
	creds := codexSubscriptionFromContext(ctx)
	if creds == nil {
		creds = ExtractClientCredentials(providers.ProviderOpenAI, headers)
	}
	ctx = requestcontext.WithCredentials(ctx, creds)
	catalog, err := fetcher.FetchCodexModelCatalog(ctx, clientVersion)
	if err != nil {
		return nil, err
	}
	return addCodexAutomaticModel(catalog)
}

func addCodexAutomaticModel(catalog []byte) ([]byte, error) {
	models := gjson.GetBytes(catalog, "models")
	if !models.IsArray() {
		return nil, fmt.Errorf("Codex model catalog has no models array")
	}
	var template, solTemplate gjson.Result
	for _, model := range models.Array() {
		modelSlug := model.Get("slug").String()
		if modelSlug == CodexAutomaticModel {
			return catalog, nil
		}
		if !template.Exists() {
			template = model
		}
		if !solTemplate.Exists() && strings.HasSuffix(modelSlug, "-sol") {
			solTemplate = model
		}
	}
	if solTemplate.Exists() {
		template = solTemplate
	}
	if !template.Exists() {
		return nil, fmt.Errorf("Codex model catalog has no model to base automatic mode on")
	}
	automatic := []byte(template.Raw)
	for path, value := range map[string]any{
		"slug":         CodexAutomaticModel,
		"display_name": "Weave Router (automatic)",
		"description":  "Automatically choose the model through Weave Router",
		"visibility":   "list",
		"priority":     0,
	} {
		var err error
		automatic, err = sjson.SetBytes(automatic, path, value)
		if err != nil {
			return nil, fmt.Errorf("set automatic model %s: %w", path, err)
		}
	}
	withAutomatic, err := sjson.SetRawBytes(catalog, "models.-1", automatic)
	if err != nil {
		return nil, fmt.Errorf("append automatic model: %w", err)
	}
	return withAutomatic, nil
}
