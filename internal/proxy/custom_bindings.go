package proxy

import (
	"context"
	"net/url"
	"strings"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"
)

func (s *Service) customBindingsForRequest(ctx context.Context) map[string][]string {
	return credentials.CustomBindings(credentials.ExternalKeysFromContext(ctx))
}

func (s *Service) gatewayProvidersForRequest(ctx context.Context) map[string]struct{} {
	return credentials.GatewayProviders(credentials.ExternalKeysFromContext(ctx))
}

func (s *Service) gatewayUnservedModelsForRequest(ctx context.Context) map[string]struct{} {
	keys := credentials.ExternalKeysFromContext(ctx)
	if len(credentials.GatewayProviders(keys)) == 0 {
		return nil
	}
	declared := make(map[string]int)
	unserved := make(map[string]int)
	for _, key := range keys {
		if len(key.Plaintext) == 0 || !providers.IsGateway(key.Provider) {
			continue
		}
		for model := range key.ModelAliases {
			m, known := catalog.ByID(model)
			if !known {
				continue
			}
			declared[m.ID]++
			if s.engine.GatewayLacksModel(gatewayModelKey(key.BaseURL, key.Provider, m.ID)) {
				unserved[m.ID]++
			}
		}
	}
	var out map[string]struct{}
	for model, count := range unserved {
		if count < declared[model] {
			continue
		}
		if out == nil {
			out = make(map[string]struct{}, len(unserved))
		}
		out[model] = struct{}{}
	}
	return out
}

func cortexGatewayProvidersForRequest(ctx context.Context) map[string]struct{} {
	var out map[string]struct{}
	for _, key := range credentials.ExternalKeysFromContext(ctx) {
		if len(key.Plaintext) == 0 || !providers.IsGateway(key.Provider) || !isCortexHost(key.BaseURL) {
			continue
		}
		if out == nil {
			out = make(map[string]struct{}, 1)
		}
		out[key.Provider] = struct{}{}
	}
	return out
}

func isCortexHost(baseURL string) bool {
	u, err := url.Parse(baseURL)
	return err == nil && strings.HasSuffix(strings.ToLower(u.Hostname()), ".snowflakecomputing.com")
}

func gatewayContextWindow(cortexGateways map[string]struct{}, modelID, provider string) int {
	if _, measured := cortexGateways[provider]; measured {
		if w, ok := catalog.CortexGatewayContextWindow(modelID, provider); ok {
			return w
		}
	}
	return contextWindowForRequest(modelID, provider)
}
