package translate

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// contextWindowVariantTag is the suffix Claude Code appends to a model id to
// mark its 1M-context variant in the picker and status line (e.g.
// "claude-opus-4-8[1m]"). It is a client-side display convention, not a real
// Anthropic model id: forwarding it verbatim to the native Anthropic API
// returns 404 not_found_error ("the selected model may not exist"). The router
// strips it back to the canonical id before dispatch; the 1M window itself is
// enabled via the context-1m-2025-08-07 beta header (size-triggered in the
// proxy), never via the model name.
const contextWindowVariantTag = "[1m]"

// CanonicalModel strips a Claude Code context-window variant tag from model
// and reports whether one was present. Models without the tag pass through
// unchanged.
func CanonicalModel(model string) (canonical string, hadVariantTag bool) {
	if strings.HasSuffix(model, contextWindowVariantTag) {
		return model[:len(model)-len(contextWindowVariantTag)], true
	}
	return model, false
}

// StripProviderPrefix removes a leading "<provider>/" qualifier from model and
// reports whether one was present. Router ingress deliberately accepts
// provider-qualified ids — eval harnesses and gateway clients send
// "anthropic/claude-opus-4-8" — and the routing path resolves them, but the
// native Anthropic API does not know them.
func StripProviderPrefix(model, provider string) (stripped string, hadPrefix bool) {
	prefix := provider + "/"
	if provider != "" && strings.HasPrefix(model, prefix) {
		return model[len(prefix):], true
	}
	return model, false
}

// StripProviderPrefixInBody rewrites the request body's top-level "model" field
// to drop a leading "<provider>/" qualifier, reporting whether one was present.
//
// This exists for the metadata passthrough (count_tokens, model list), which
// forwards the body to the native provider without a routing decision. A
// provider-qualified id survives that hop and the upstream 404s: the
// 2026-08-23 SWE-Interact runs, launched with
// `--model anthropic/claude-opus-4-8`, produced 198 consecutive
// `POST /v1/messages/count_tokens` 404s ("not_found_error: model:
// anthropic/claude-opus-4-8"), leaving Claude Code unable to measure its own
// context for the whole run. Routing, pins, and telemetry keep the qualified
// id; only the outbound passthrough body is rewritten.
func StripProviderPrefixInBody(body []byte, provider string) (out []byte, hadPrefix bool, err error) {
	stripped, had := StripProviderPrefix(gjson.GetBytes(body, "model").String(), provider)
	if !had {
		return body, false, nil
	}
	out, err = sjson.SetBytes(body, "model", stripped)
	if err != nil {
		return body, true, err
	}
	return out, true, nil
}

// CanonicalizeModelInBody rewrites the request body's top-level "model" field
// to its canonical form (stripping a Claude Code context-window variant tag)
// and reports whether the tag was present. A body whose model carries no tag —
// or has no model field at all — is returned unchanged with hadVariantTag
// false. This is the single normalization seam for inbound requests so the
// tag never reaches a native Anthropic upstream, and so routing, pins, and
// telemetry all key off the canonical id.
func CanonicalizeModelInBody(body []byte) (out []byte, hadVariantTag bool, err error) {
	canonical, had := CanonicalModel(gjson.GetBytes(body, "model").String())
	if !had {
		return body, false, nil
	}
	out, err = sjson.SetBytes(body, "model", canonical)
	if err != nil {
		return body, true, err
	}
	return out, true, nil
}
