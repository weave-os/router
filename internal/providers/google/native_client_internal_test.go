package google

import (
	"net/http"
	"testing"

	"workweave/router/internal/providers/httputil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The unary/streaming split IS the fix — a regression back to one shared
// transport must fail here, not only in the slower behavioral tests.
func TestNewNativeClient_HeaderGuardWiring(t *testing.T) {
	c := NewNativeClient("k", "")

	unary, ok := c.unaryHTTP.Transport.(*http.Transport)
	require.True(t, ok)
	stream, ok := c.http.Transport.(*http.Transport)
	require.True(t, ok)

	assert.Equal(t, httputil.DefaultUnaryResponseHeaderTimeout, unary.ResponseHeaderTimeout)
	assert.Equal(t, httputil.DefaultResponseHeaderTimeout, stream.ResponseHeaderTimeout)
}
