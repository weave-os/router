package openai

import (
	"bytes"
	"context"
	"net/http"
)

func newInferenceRequest(ctx context.Context, url string, body []byte) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	return request, nil
}
