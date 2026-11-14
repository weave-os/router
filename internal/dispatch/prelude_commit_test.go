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

func TestPreludeBuffer_DiscardRestoresHeadersAndRunsHook(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Header().Set("X-Initial", "kept")
	buf := dispatch.NewPreludeBuffer(rec)
	discarded := 0
	buf.OnDiscard = func() { discarded++ }

	buf.Header().Set("X-Attempt", "primary")
	buf.Header().Del("X-Initial")
	buf.WriteHeader(http.StatusOK)
	_, err := buf.Write([]byte("prelude"))
	require.NoError(t, err)
	buf.Flush()

	buf.Discard()

	assert.Equal(t, 1, discarded)
	assert.Equal(t, "kept", rec.Header().Get("X-Initial"))
	assert.Empty(t, rec.Header().Get("X-Attempt"))
	assert.Empty(t, rec.Body.String())
	assert.False(t, rec.Flushed)
	assert.False(t, buf.Committed())
}

func TestPreludeBuffer_DiscardAfterCommitIsNoOp(t *testing.T) {
	rec := httptest.NewRecorder()
	buf := dispatch.NewPreludeBuffer(rec)
	discarded := 0
	buf.OnDiscard = func() { discarded++ }

	buf.Seal()
	buf.WriteHeader(http.StatusCreated)
	_, err := buf.Write([]byte("output"))
	require.NoError(t, err)

	buf.Discard()

	assert.Zero(t, discarded)
	assert.True(t, buf.Committed())
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "output", rec.Body.String())
	assert.True(t, rec.Flushed)
}
