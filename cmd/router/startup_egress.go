package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v5"
	"golang.org/x/sync/errgroup"
	"weave-os/router/internal/providers"
)

const startupEgressTimeout = 120 * time.Second

type startupTransportWarmer interface {
	WarmTransport(context.Context, string) (bool, error)
}

type startupEgressProbe struct{ origins []string }

func newStartupEgressProbe(rawOrigins string) (*startupEgressProbe, error) {
	probe := &startupEgressProbe{}
	if strings.TrimSpace(rawOrigins) == "" {
		return probe, nil
	}
	for _, origin := range strings.Split(rawOrigins, ",") {
		parsed, err := url.Parse(strings.TrimSpace(origin))
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
			return nil, fmt.Errorf("startup egress entry %d must be an HTTPS origin without credentials, path, query or fragment", len(probe.origins)+1)
		}
		probe.origins = append(probe.origins, parsed.Scheme+"://"+parsed.Host)
	}
	return probe, nil
}

func (p *startupEgressProbe) wait(ctx context.Context, log *slog.Logger, clients map[string]providers.Client, enabled map[string]struct{}, initializedOrigins map[string]struct{}) error {
	ctx, cancel := context.WithTimeout(ctx, startupEgressTimeout)
	defer cancel()
	workers, ctx := errgroup.WithContext(ctx)
	workers.SetLimit(4)
	for name := range enabled {
		warmer, ok := clients[name].(startupTransportWarmer)
		if !ok {
			return fmt.Errorf("provider %s has no startup transport initializer", name)
		}
		workers.Go(func() error {
			return retryStartupTransport(ctx, log, name, func(ctx context.Context) error { _, err := warmer.WarmTransport(ctx, ""); return err })
		})
	}
	for _, origin := range p.origins {
		if _, initialized := initializedOrigins[origin]; initialized {
			continue
		}
		workers.Go(func() error {
			return retryStartupTransport(ctx, log, origin, func(ctx context.Context) error {
				matched := false
				for _, client := range clients {
					warmer, ok := client.(startupTransportWarmer)
					if !ok {
						continue
					}
					warmed, err := warmer.WarmTransport(ctx, origin)
					if err != nil {
						return err
					}
					matched = matched || warmed
				}
				if !matched {
					return backoff.Permanent(fmt.Errorf("startup origin %s has no retained serving client", origin))
				}
				return nil
			})
		})
	}
	return workers.Wait()
}

func retryStartupTransport(ctx context.Context, log *slog.Logger, name string, warm func(context.Context) error) error {
	started := time.Now()
	retry := backoff.NewExponentialBackOff()
	retry.MaxInterval = 5 * time.Second
	_, err := backoff.Retry(ctx, func() (struct{}, error) {
		attemptCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return struct{}{}, warm(attemptCtx)
	}, backoff.WithBackOff(retry), backoff.WithMaxElapsedTime(0), backoff.WithNotify(func(err error, delay time.Duration) {
		log.Warn("Startup transport is not ready; retrying", "dependency", name, "retry_after", delay, "err", err)
	}))
	if err != nil {
		return fmt.Errorf("initialize startup transport %s: %w", name, err)
	}
	log.Info("Startup transport initialized", "dependency", name, "elapsed", time.Since(started))
	return nil
}
