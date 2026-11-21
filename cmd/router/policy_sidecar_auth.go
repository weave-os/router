package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"weave-os/router/internal/policyclient"
	"weave-os/router/internal/router"
)

const (
	policySidecarAuthNone          = "none"
	policySidecarAuthGoogleIDToken = "google-id-token"
	policySidecarAuthBearer        = "bearer"
)

func configuredPolicyTokens(
	raw string,
	endpoints map[router.Strategy]policySidecarEndpoint,
	configured map[router.Strategy]struct{},
	modes map[router.Strategy]string,
) (map[router.Strategy]string, error) {
	tokens, err := parseConfiguredPolicySidecarSettings(raw, "ROUTER_POLICY_SIDECAR_BEARER_TOKENS", configured)
	if err != nil {
		return nil, err
	}
	for strategy, endpoint := range endpoints {
		useBearer := strings.EqualFold(strings.TrimSpace(modes[strategy]), policySidecarAuthBearer)
		token, exists := tokens[strategy]
		if useBearer != exists || (exists && strings.TrimSpace(token) == "") {
			return nil, fmt.Errorf("%s policy sidecar requires exactly one nonempty token when auth mode is bearer", strategy)
		}
		if useBearer && !strings.HasPrefix(strings.ToLower(endpoint.Origin), "https:") && !endpoint.AllowHTTPBearer {
			return nil, fmt.Errorf("%s policy sidecar requires HTTPS for bearer auth unless allow_http_bearer is enabled", strategy)
		}
	}
	return tokens, nil
}

type googleIDTokenClientFactory func(string, time.Duration, ...policyclient.Option) (*policyclient.Client, error)

func buildHMMPolicyClientWithGoogleIDTokenFactory(
	sidecarURL, authMode string,
	timeout time.Duration,
	newGoogleIDTokenClient googleIDTokenClientFactory,
	opts ...policyclient.Option,
) (*policyclient.Client, error) {
	return buildPolicyClientWithGoogleIDTokenFactory(
		sidecarURL,
		authMode,
		timeout,
		nil,
		"ROUTER_HMM_SIDECAR_AUTH",
		newGoogleIDTokenClient,
		opts...,
	)
}

func buildConfiguredPolicyClient(
	sidecarURL, authMode string,
	timeout time.Duration,
	httpClient *http.Client,
	opts ...policyclient.Option,
) (*policyclient.Client, error) {
	return buildPolicyClientWithGoogleIDTokenFactory(
		sidecarURL,
		authMode,
		timeout,
		httpClient,
		"ROUTER_POLICY_SIDECAR_AUTH",
		policyclient.NewGoogleIDToken,
		opts...,
	)
}

func buildPolicyClientWithGoogleIDTokenFactory(
	sidecarURL, authMode string,
	timeout time.Duration,
	httpClient *http.Client,
	authSetting string,
	newGoogleIDTokenClient googleIDTokenClientFactory,
	opts ...policyclient.Option,
) (*policyclient.Client, error) {
	switch strings.ToLower(strings.TrimSpace(authMode)) {
	case "", policySidecarAuthNone:
		return policyclient.New(sidecarURL, httpClient, timeout, opts...), nil
	case policySidecarAuthGoogleIDToken:
		return newGoogleIDTokenClient(sidecarURL, timeout, opts...)
	default:
		return nil, fmt.Errorf(
			"unsupported %s %q (expected %q or %q)",
			authSetting,
			authMode,
			policySidecarAuthNone,
			policySidecarAuthGoogleIDToken,
		)
	}
}
