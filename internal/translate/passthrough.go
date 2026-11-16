package translate

import (
	"fmt"
	"net/http"
	"strings"

	"weave-os/router/internal/providers"
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
