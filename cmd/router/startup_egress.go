package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v5"
	"golang.org/x/sync/errgroup"
)

const startupEgressTimeout = 120 * time.Second

// startupEgressProbe gates boot, not ongoing readiness: a later provider outage
// must not remove an otherwise healthy worker from service.
type startupEgressProbe struct {
	origins []string
	client  *http.Client
}

func newStartupEgressProbe(rawOrigins string) (*startupEgressProbe, error) {
	probe := &startupEgressProbe{}
	if strings.TrimSpace(rawOrigins) == "" {
		return probe, nil
	}
	for _, origin := range strings.Split(rawOrigins, ",") {
		origin = strings.TrimSpace(origin)
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
			// Do not echo configuration that might mistakenly contain credentials.
			return nil, fmt.Errorf("startup egress entry %d must be an HTTPS origin without credentials, path, query or fragment", len(probe.origins)+1)
		}
		probe.origins = append(probe.origins, parsed.Scheme+"://"+parsed.Host)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Probe the worker's direct network path, not an ambient HTTP proxy or a
	// reused connection. No provider credentials or inference requests are sent.
	transport.Proxy = nil
	transport.DisableKeepAlives = true
	probe.client = &http.Client{
		Transport: transport,
		Timeout:   5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return probe, nil
}

func (p *startupEgressProbe) wait(ctx context.Context, logger *slog.Logger) error {
	if len(p.origins) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, startupEgressTimeout)
	defer cancel()
	defer p.client.CloseIdleConnections()
	started := time.Now()
	logger.Info("Waiting for startup outbound connectivity", "origins", p.origins, "timeout", startupEgressTimeout)
	probes, ctx := errgroup.WithContext(ctx)
	for _, origin := range p.origins {
		probes.Go(func() error {
			retryPolicy := backoff.NewExponentialBackOff()
			retryPolicy.MaxInterval = 5 * time.Second
			_, err := backoff.Retry(ctx, func() (struct{}, error) {
				if err := ctx.Err(); err != nil {
					return struct{}{}, err
				}
				request, err := http.NewRequestWithContext(ctx, http.MethodHead, origin, nil)
				if err != nil {
					return struct{}{}, backoff.Permanent(err)
				}
				response, err := p.client.Do(request)
				if err != nil {
					return struct{}{}, err
				}
				response.Body.Close()
				// Any HTTP status proves DNS/TCP/TLS egress, including an expected
				// unauthenticated 401/403. This is not a provider availability check.
				return struct{}{}, nil
			}, backoff.WithBackOff(retryPolicy), backoff.WithMaxElapsedTime(0), backoff.WithNotify(func(err error, delay time.Duration) {
				logger.Warn("Startup outbound connectivity not ready; retrying", "origin", origin, "retry_after", delay, "err", err)
			}))
			if err != nil {
				return fmt.Errorf("startup outbound connectivity to %s: %w", origin, err)
			}
			return nil
		})
	}
	if err := probes.Wait(); err != nil {
		return err
	}
	logger.Info("Startup outbound connectivity ready", "origins", p.origins, "elapsed", time.Since(started))
	return nil
}
