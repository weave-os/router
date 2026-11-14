package dispatch_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/dispatch"
)

func TestPreludeBuffer_CommitsEagerPreludeAndBuffersProviderOutput(t *testing.T) {
	rec := httptest.NewRecorder()
	buf := dispatch.NewPreludeBuffer(rec)

	buf.Header().Set("Content-Type", "text/event-stream")
	buf.WriteHeader(http.StatusOK)
	_, err := buf.Write([]byte("marker"))
	require.NoError(t, err)
	require.NoError(t, buf.CommitPrelude())
	assert.True(t, buf.PreludeSent())
	assert.False(t, buf.Committed())
	assert.Equal(t, "marker", rec.Body.String())

	buf.Seal()
	buf.Discard()
	assert.Equal(t, "marker", rec.Body.String())

	_, err = buf.Write([]byte("fallback-marker"))
	require.NoError(t, err)
	require.NoError(t, buf.CommitPrelude())
	assert.Equal(t, "markerfallback-marker", rec.Body.String())

	buf.Seal()
	_, err = buf.Write([]byte("provider-output"))
	require.NoError(t, err)
	assert.True(t, buf.Committed())
	assert.Equal(t, "markerfallback-markerprovider-output", rec.Body.String())
}
