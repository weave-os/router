package httputil

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// WarmTransport exercises DNS/TCP/TLS through retained serving clients. It sends
// no credential or inference and makes no authentication or model warmth claim.
// A nonempty requiredOrigin limits warmup to an exactly matching configured origin.
func WarmTransport(ctx context.Context, baseURL, requiredOrigin string, clients ...*http.Client) (bool, error) {
	if baseURL == "" && requiredOrigin != "" {
		return false, nil
	}
	base, err := url.Parse(baseURL)
	if err != nil || base.Host == "" || base.User != nil || (base.Scheme != "https" && base.Scheme != "http") {
		return false, fmt.Errorf("invalid provider warmup base URL")
	}
	origin := base.Scheme + "://" + base.Host
	if requiredOrigin != "" && requiredOrigin != origin {
		return false, nil
	}
	for _, client := range clients {
		request, err := http.NewRequestWithContext(ctx, http.MethodHead, origin, nil)
		if err != nil {
			return true, err
		}
		warmClient := *client
		warmClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		response, err := warmClient.Do(request)
		if err != nil {
			return true, err
		}
		_, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		response.Body.Close()
		if readErr != nil {
			return true, readErr
		}
	}
	return true, nil
}
