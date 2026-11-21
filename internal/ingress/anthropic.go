package ingress

import (
	"context"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/translate"
)

var AnthropicPingFrame = []byte("event: ping\ndata: {\"type\":\"ping\"}\n\n")

type MessagesRequest struct {
	Envelope       *translate.RequestEnvelope
	Body           []byte
	ModelVariant1M bool
}

type Anthropic struct{}

var _ Adapter = Anthropic{}

func (Anthropic) Format() translate.Format { return translate.FormatAnthropic }

func (Anthropic) TraceBody(body []byte) []byte { return body }

func (Anthropic) CheckTarget(string) error { return nil }

func (a Anthropic) Parse(ctx context.Context, body []byte) (*translate.RequestEnvelope, []byte, error) {
	req, err := a.ParseMessages(ctx, body)
	return req.Envelope, req.Body, err
}

func (Anthropic) ParseMessages(ctx context.Context, body []byte) (MessagesRequest, error) {
	log := observability.FromContext(ctx)
	if strippedBody, ferr := translate.StripFeedbackFooterFromMessages(body); ferr != nil {
		log.Error("Failed to strip feedback footer from inbound messages", "err", ferr)
	} else {
		body = strippedBody
	}

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
