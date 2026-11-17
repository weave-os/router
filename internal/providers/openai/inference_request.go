package openai

import (
	"bytes"
	"context"
	"net/http"
	"weave-os/router/internal/requestcontext"
)

func newInferenceRequest(ctx context.Context, url string, body []byte) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if requestcontext.IncludedSubscriptionOnlyFrom(ctx) {
		request.GetBody = nil
		request.Close = true
	}
	return request, nil
}
