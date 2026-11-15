package iofreehttp

import (
	"context"
	"net/http"
	"os"
)

func fetch(ctx context.Context, client *http.Client, header http.Header) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, os.Getenv("FIXTURE_URL"), nil)
	if err != nil {
		return nil, err
	}
	request.Header = header
	return client.Do(request)
}
