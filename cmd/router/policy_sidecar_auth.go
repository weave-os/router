package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"weave-os/router/internal/policyclient"
)

const (
	policySidecarAuthNone          = "none"
	policySidecarAuthGoogleIDToken = "google-id-token"
)

type googleIDTokenClientFactory func(string, time.Duration, ...policyclient.Option) (*policyclient.Client, error)

func buildHMMPolicyClient(
	sidecarURL, authMode string,
	timeout time.Duration,
	opts ...policyclient.Option,
) (*policyclient.Client, error) {
	return buildHMMPolicyClientWithGoogleIDTokenFactory(
		sidecarURL,
		authMode,
		timeout,
		policyclient.NewGoogleIDToken,
		opts...,
	)
}

func buildHMMBetaPolicyClient(
	sidecarURL, authMode string,
	timeout time.Duration,
	opts ...policyclient.Option,
) (*policyclient.Client, error) {
	return buildPolicyClientWithGoogleIDTokenFactory(
		sidecarURL,
		authMode,
		timeout,
		nil,
		"ROUTER_HMM_BETA_SIDECAR_AUTH",
		policyclient.NewGoogleIDToken,
		opts...,
	)
}

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

func buildConfiguredPolicyClientWithGoogleIDTokenFactory(
	sidecarURL, authMode string,
	timeout time.Duration,
	httpClient *http.Client,
	newGoogleIDTokenClient googleIDTokenClientFactory,
	opts ...policyclient.Option,
) (*policyclient.Client, error) {
	return buildPolicyClientWithGoogleIDTokenFactory(
		sidecarURL,
		authMode,
		timeout,
		httpClient,
		"ROUTER_POLICY_SIDECAR_AUTH",
		newGoogleIDTokenClient,
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
