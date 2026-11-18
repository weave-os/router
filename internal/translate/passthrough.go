package translate

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
)

func (e *RequestEnvelope) PrepareAnthropicPassthrough(in http.Header) (providers.PreparedRequest, error) {
	ov, _ := resolvePassthroughOverrides(e.body)
	ov.SanitizeAnthropicToolSchemas = true
	body, err := e.emitSameFormat(ov)
	if err != nil {
		return providers.PreparedRequest{}, err
	}
	body, err = normalizeAnthropicSystemOnlyContentBlocks(body)
	if err != nil {
		return providers.PreparedRequest{}, fmt.Errorf("normalize system-only content blocks: %w", err)
	}
	body, err = placeAnthropicSystemMessagesBytes(body)
	if err != nil {
		return providers.PreparedRequest{}, fmt.Errorf("place system messages: %w", err)
	}
	return providers.PreparedRequest{Body: body, Headers: AnthropicPassthroughHeadersForBody(in, body)}, nil
}

func AnthropicPassthroughHeaders(in http.Header) http.Header {
	return AnthropicPassthroughHeadersForBody(in, nil)
}

func AnthropicPassthroughHeadersForBody(in http.Header, body []byte) http.Header {
	h := make(http.Header)
	if v := in.Get("anthropic-version"); v != "" {
		h.Set("anthropic-version", v)
	} else {
		h.Set("anthropic-version", "2023-06-01")
	}
	beta := stripThinkingBetas(in.Get("anthropic-beta"))
	if containsAnthropicSystemOnlyContentBlocks(body) {
		beta = ensureBetaToken(beta, anthropicMidConversationToolChangesBeta)
	}
	if beta != "" {
		h.Set("anthropic-beta", beta)
	}
	return h
}

func stripThinkingBetas(beta string) string {
	return joinKept(beta, func(token string) bool {
		return !strings.Contains(token, "thinking")
	})
}

func (e *RequestEnvelope) PrepareAnthropicVerbatim(in http.Header, opts EmitOptions) (providers.PreparedRequest, error) {
	h := make(http.Header)
	h.Set("anthropic-version", in.Get("anthropic-version"))
	if h.Get("anthropic-version") == "" {
		h.Set("anthropic-version", "2023-06-01")
	}
	beta := in.Get("anthropic-beta")
	body := e.body
	if verbatimServerSideFallback(body, opts) {
		var err error
		if body, err = sjson.SetBytes(body, "fallbacks", "default"); err != nil {
			return providers.PreparedRequest{}, fmt.Errorf("set fallbacks: %w", err)
		}
		beta = ensureBetaToken(beta, serverSideFallbackBeta)
	}
	if beta != "" {
		h.Set("anthropic-beta", beta)
	}
	return providers.PreparedRequest{Body: body, Headers: h}, nil
}

func ExtendedContextBeta(beta string, opts EmitOptions) string {
	if opts.EnableExtendedContext && opts.Capabilities.Supports(router.CapExtendedContext) {
		return ensureBetaToken(beta, context1MBeta)
	}
	return beta
}

func ServerSideFallbackBeta(beta string, body []byte) string {
	if gjson.GetBytes(body, "fallbacks").Exists() {
		return ensureBetaToken(beta, serverSideFallbackBeta)
	}
	return beta
}

func verbatimServerSideFallback(body []byte, opts EmitOptions) bool {
	return opts.EnableServerSideFallback &&
		opts.TargetProvider == providers.ProviderAnthropic &&
		opts.Capabilities.Supports(router.CapServerSideFallback) &&
		!gjson.GetBytes(body, "fallbacks").Exists()
}

func (e *RequestEnvelope) PrepareOpenAIVerbatim(opts EmitOptions) (providers.PreparedRequest, bool) {
	if e.format != FormatOpenAI || opts.TargetModel != e.Model() || opts.StripPromptCacheKey && gjson.GetBytes(e.body, "prompt_cache_key").Exists() {
		return providers.PreparedRequest{}, false
	}
	if e.Stream() && opts.IncludeStreamUsage && !gjson.GetBytes(e.body, "stream_options.include_usage").Bool() {
		return providers.PreparedRequest{}, false
	}
	return providers.PreparedRequest{Body: e.body, Headers: make(http.Header)}, true
}
