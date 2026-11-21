package main

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/policyclient"
)

func TestBuildHMMPolicyClientPreservesUnauthenticatedLocalMode(t *testing.T) {
	client, err := buildHMMPolicyClientWithGoogleIDTokenFactory("http://localhost:8093", "none", time.Second, policyclient.NewGoogleIDToken)

	require.NoError(t, err)
	assert.NotNil(t, client)
}

func TestBuildHMMPolicyClientRejectsUnknownAuthMode(t *testing.T) {
	client, err := buildHMMPolicyClientWithGoogleIDTokenFactory("https://sidecar.internal", "api-key", time.Second, policyclient.NewGoogleIDToken)

	require.Error(t, err)
	assert.Nil(t, client)
	assert.Contains(t, err.Error(), "unsupported ROUTER_HMM_SIDECAR_AUTH")
}

func TestBuildHMMPolicyClientFailsClosedWhenGoogleCredentialsCannotBuild(t *testing.T) {
	wantErr := errors.New("ADC unavailable")
	client, err := buildHMMPolicyClientWithGoogleIDTokenFactory(
		"https://sidecar.internal",
		"google-id-token",
		time.Second,
		func(string, time.Duration, ...policyclient.Option) (*policyclient.Client, error) { return nil, wantErr },
	)

	require.ErrorIs(t, err, wantErr)
	assert.Nil(t, client)
}

func TestBuildConfiguredPolicyClientPreservesProvidedLocalClient(t *testing.T) {
	httpClient := &http.Client{}
	client, err := buildConfiguredPolicyClient(
		"http://localhost:8094",
		policySidecarAuthNone,
		time.Second,
		httpClient,
	)

	require.NoError(t, err)
	assert.NotNil(t, client)
}

func TestBuildConfiguredPolicyClientUsesGoogleIDTokenAudience(t *testing.T) {
	var audience string
	client, err := buildPolicyClientWithGoogleIDTokenFactory(
		"https://sidecar.internal",
		policySidecarAuthGoogleIDToken,
		time.Second,
		nil,
		"ROUTER_POLICY_SIDECAR_AUTH",
		func(gotAudience string, _ time.Duration, opts ...policyclient.Option) (*policyclient.Client, error) {
			audience = gotAudience
			return policyclient.New(gotAudience, nil, time.Second, opts...), nil
		},
	)

	require.NoError(t, err)
	assert.NotNil(t, client)
	assert.Equal(t, "https://sidecar.internal", audience)
}

func TestBuildConfiguredPolicyClientRejectsUnknownAuthMode(t *testing.T) {
	client, err := buildConfiguredPolicyClient(
		"https://sidecar.internal",
		"api-key",
		time.Second,
		nil,
	)

	require.Error(t, err)
	assert.Nil(t, client)
	assert.Contains(t, err.Error(), "unsupported ROUTER_POLICY_SIDECAR_AUTH")
}
