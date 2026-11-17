package proxy

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	modelcatalog "weave-os/router/internal/router/catalog"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const CodexAutomaticModel = string(router.ModelAutomatic)

const codexRoutingContextWindow = 1_000_000

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
	creds := ExtractClientCredentials(providers.ProviderOpenAI, headers)
	ctx = requestcontext.WithCredentials(ctx, creds)
	catalog, err := fetcher.FetchCodexModelCatalog(ctx, clientVersion)
	if err != nil {
		return nil, err
	}
	return addCodexRoutingModels(catalog, internalBetaSession(ctx))
}

func addCodexRoutingModels(catalog []byte, includeVariants bool) ([]byte, error) {
	models := gjson.GetBytes(catalog, "models")
	if !models.IsArray() {
		return nil, fmt.Errorf("codex model catalog has no models array")
	}
	var template, solTemplate gjson.Result
	seen := make(map[string]bool)
	for _, model := range models.Array() {
		modelSlug := model.Get("slug").String()
		seen[modelSlug] = true
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
		return nil, fmt.Errorf("codex model catalog has no model to base automatic mode on")
	}
	for _, choice := range modelcatalog.ModelChoices() {
		if choice.Kind != modelcatalog.ModelChoiceRouter || seen[choice.Model] || (!includeVariants && choice.Model != CodexAutomaticModel) {
			continue
		}
		displayName, description := choice.Model, choice.Description
		if choice.Model == CodexAutomaticModel {
			displayName, description = "Weave Router", "Automatically chooses the best model"
		}
		routingChoice := []byte(template.Raw)
		for path, value := range map[string]any{
			"slug": choice.Model, "display_name": displayName, "description": description,
			"visibility": "list", "priority": 0,
			"context_window": codexRoutingContextWindow, "max_context_window": codexRoutingContextWindow,
		} {
			var err error
			routingChoice, err = sjson.SetBytes(routingChoice, path, value)
			if err != nil {
				return nil, fmt.Errorf("set routing model %s: %w", path, err)
			}
		}
		var err error
		catalog, err = sjson.SetRawBytes(catalog, "models.-1", routingChoice)
		if err != nil {
			return nil, fmt.Errorf("append routing model: %w", err)
		}
	}
	return catalog, nil
}
