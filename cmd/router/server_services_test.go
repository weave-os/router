package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/ingress"
	"weave-os/router/internal/proxy"
)

func TestServerServicesWiresTheAllowedModelsHeaderParser(t *testing.T) {
	services := serverServices(proxy.NewService(nil, nil, nil, nil, false, "", "", nil), ingress.Formats{})

	require.NotNil(t, services.ParseAllowedModelsHeader)
	parsed, err := services.ParseAllowedModelsHeader("gpt-5-6-sol", nil)
	require.NoError(t, err)
	require.IsType(t, proxy.RequestAllowedModels{}, parsed)
}
