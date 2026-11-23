package providers_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/providers"
)

func TestUpstreamStatusReadsWrappedStatus(t *testing.T) {
	assert.Equal(t, http.StatusOK, providers.UpstreamStatus(nil))
	assert.Equal(t, http.StatusBadGateway, providers.UpstreamStatus(&providers.UpstreamErrorResponse{Status: http.StatusBadGateway}))
	assert.Zero(t, providers.UpstreamStatus(context.Canceled))
}
