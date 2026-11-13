package rawinferencehttp

import (
	"context"
	"net/http"
	"time"
)

var upstreamClient = &http.Client{Timeout: 30 * time.Second}

func call(ctx context.Context) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://provider.example/v1/messages", nil)
	if err != nil {
		return nil, err
	}
	return upstreamClient.Do(request)
}
