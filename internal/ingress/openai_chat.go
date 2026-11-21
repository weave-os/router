package ingress

import (
	"context"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/translate"
)

type OpenAIChat struct{}

var _ Adapter = OpenAIChat{}

func (OpenAIChat) Format() translate.Format { return translate.FormatOpenAI }

func (OpenAIChat) TraceBody(body []byte) []byte { return body }

func (OpenAIChat) CheckTarget(string) error { return nil }

func (OpenAIChat) Parse(ctx context.Context, body []byte) (*translate.RequestEnvelope, []byte, error) {
	if strippedBody, ferr := translate.StripFeedbackFooterFromMessages(body); ferr != nil {
		observability.FromContext(ctx).Error("Failed to strip feedback footer from OpenAI messages", "err", ferr)
	} else {
		body = strippedBody
	}
	env, err := translate.ParseOpenAI(body)
	return env, body, err
}
