package main

import (
	"crypto/rand"
	"log/slog"
	"time"

	"weave-os/router/internal/config"
	"weave-os/router/internal/proxy/usage"
)

func newUsageObserver(logger *slog.Logger, seats usage.SeatUsageRecorder) *usage.Observer {
	subscriptionTTL := 10 * time.Minute
	if v, err := time.ParseDuration(config.GetOr("ROUTER_SUBSCRIPTION_OBSERVATION_TTL", "10m")); err == nil {
		subscriptionTTL = v
	}
	observerSalt := make([]byte, 16)
	if _, err := rand.Read(observerSalt); err != nil {
		logger.Error("Failed to seed subscription usage observer salt", "err", err)
		panic(err)
	}
	usageObserver := usage.NewObserver(observerSalt, subscriptionTTL, time.Now).WithSeatUsageRecorder(seats)
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for range t.C {
			usageObserver.Sweep()
		}
	}()
	logger.Info("Subscription quota observer configured", "observation_ttl", subscriptionTTL)
	return usageObserver
}
