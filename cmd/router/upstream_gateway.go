package main

import (
	"log/slog"
	"net/http"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/config"
	"weave-os/router/internal/oidcjwks"
)

func upstreamGatewayIssuers(logger *slog.Logger) ([]auth.UpstreamGatewayIssuer, auth.OIDCKeySet) {
	issuers, err := auth.ParseUpstreamGatewayIssuers(config.GetOr("ROUTER_UPSTREAM_GATEWAY_ISSUERS", ""))
	if err != nil {
		logger.Error("Refusing to boot with invalid upstream gateway issuer configuration", "err", err)
		panic(err)
	}
	if len(issuers) == 0 {
		return nil, nil
	}
	jwksClient := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	logger.Info("Upstream gateway service authentication enabled", "issuers", len(issuers))
	return issuers, oidcjwks.New(jwksClient, time.Now)
}
