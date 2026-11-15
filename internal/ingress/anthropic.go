package ingress

import (
	"context"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/translate"
)

// AnthropicPingFrame keeps a client-facing stream byte-alive during long
// upstream reasoning phases that produce no translatable frames.
var AnthropicPingFrame = []byte("event: ping\ndata: {\"type\":\"ping\"}\n\n")

// MessagesRequest is a parsed Anthropic Messages request.
type MessagesRequest struct {
	Envelope *translate.RequestEnvelope
	Body     []byte
	// ModelVariant1M reports that the client named a 1M-context model variant.
	ModelVariant1M bool
}

// Anthropic is the native Anthropic Messages adapter. Its committed-stream
// error frame is rendered from the dispatch error's class, which the
// orchestrator owns.
type Anthropic struct{}

var _ Adapter = Anthropic{}

// Format implements Adapter.
func (Anthropic) Format() translate.Format { return translate.FormatAnthropic }

// TraceBody implements Adapter; a Messages body is forwarded as received.
func (Anthropic) TraceBody(body []byte) []byte { return body }

// CheckTarget implements Adapter; Messages requests translate to every provider.
func (Anthropic) CheckTarget(string) error { return nil }

// Parse implements Adapter.
func (a Anthropic) Parse(ctx context.Context, body []byte) (*translate.RequestEnvelope, []byte, error) {
	req, err := a.ParseMessages(ctx, body)
	return req.Envelope, req.Body, err
}

// ParseMessages strips the router's echoed feedback footer, canonicalizes the
// model and parses the Messages body.
func (Anthropic) ParseMessages(ctx context.Context, body []byte) (MessagesRequest, error) {
	log := observability.FromContext(ctx)
	// Same for the one-click thumbs footer (and its signed rate URLs), which
	// would otherwise shift assistant prefixes off the prompt cache.
	// Best-effort: log-and-continue on failure rather than abort over cosmetic
	// cleanup, matching the OpenAI chat path.
	if strippedBody, ferr := translate.StripFeedbackFooterFromMessages(body); ferr != nil {
		log.Error("Failed to strip feedback footer from inbound messages", "err", ferr)
	} else {
		body = strippedBody
	}

	// Strip Claude Code's 1M-context model variant tag (e.g.
	// "claude-opus-4-8[1m]") to the canonical id before parsing, so routing/pins/
	// telemetry key off the real model and it never reaches a native Anthropic
	// upstream (which 404s on it). The 1M window is enabled separately via the
	// context-1m beta.
	var modelVariant1M bool
	if canon, hadVariant, modelErr := translate.CanonicalizeModelInBody(body); modelErr != nil {
		log.Error("Failed to canonicalize inbound model", "err", modelErr)
	} else {
		body = canon
		modelVariant1M = hadVariant
	}

	env, err := translate.ParseAnthropic(body)
	return MessagesRequest{Envelope: env, Body: body, ModelVariant1M: modelVariant1M}, err
}
