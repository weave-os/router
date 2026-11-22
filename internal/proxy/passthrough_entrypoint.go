package proxy

import (
	"context"
	"fmt"
	"net/http"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"
)

func (s *Service) PassthroughToProvider(ctx context.Context, body []byte, w http.ResponseWriter, r *http.Request) error {
	if isCountTokensRequest(r) {
		return s.countTokens(ctx, body, w, r)
	}
	return s.PassthroughToNamedProvider(ctx, providers.ProviderAnthropic, body, w, r)
}

func (s *Service) PassthroughToNamedProvider(ctx context.Context, providerName string, body []byte, w http.ResponseWriter, r *http.Request) error {
	log := observability.FromContext(ctx)
	p, err := s.engine.Clients().Upstream(providerName)
	if err != nil {
		return err
	}
	ctx = resolveAndInjectCredentials(ctx, providerName, "", r.Header)

	if providerName == providers.ProviderAnthropic && len(body) > 0 {
		if canon, had, cerr := translate.CanonicalizeModelInBody(body); cerr == nil && had {
			body = canon
		}
		if bare, had, cerr := translate.StripProviderPrefixInBody(body, providerName); cerr == nil && had {
			body = bare
		}
	}

	var prep providers.PreparedRequest
	if providerName == providers.ProviderAnthropic && len(body) > 0 {
		env, parseErr := translate.ParseAnthropic(body)
		if parseErr == nil {
			prep, err = env.PrepareAnthropicPassthrough(r.Header)
			if err != nil {
				return fmt.Errorf("prepare passthrough: %w", err)
			}
		} else {
			prep = providers.PreparedRequest{Body: body, Headers: translate.AnthropicPassthroughHeadersForBody(r.Header, body)}
		}
	} else if providerName == providers.ProviderAnthropic {
		prep = providers.PreparedRequest{Body: body, Headers: translate.AnthropicPassthroughHeadersForBody(r.Header, body)}
	} else {
		prep = providers.PreparedRequest{Body: body, Headers: make(http.Header)}
	}

	proxyStart := s.clock.Now()
	proxyErr := p.Passthrough(ctx, prep, w, r)
	proxyMs := s.clock.Now().Sub(proxyStart).Milliseconds()
	log.Info("PassthroughToProvider complete", "provider", providerName, "proxy_ms", proxyMs, "proxy_err", proxyErr)
	return proxyErr
}
