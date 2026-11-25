package analytics_test

import (
	"encoding/base64"
	"testing"
	"time"

	"weave-os/router/internal/analytics"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUsageCursorRoundTrip(t *testing.T) {
	cursor := analytics.UsageCursor{Offset: 2000, IngestedBefore: time.Date(2026, 11, 25, 11, 59, 0, 0, time.UTC)}
	parsed, err := analytics.ParseUsageCursor(cursor.String())
	require.NoError(t, err)
	assert.Equal(t, cursor, parsed)
}

func TestParseUsageCursorRejectsMalformedTokens(t *testing.T) {
	cases := map[string]string{
		"not base64":          "!!",
		"wrong version":       base64.RawURLEncoding.EncodeToString([]byte("v0:10:1795000000000000000")),
		"missing cutoff":      base64.RawURLEncoding.EncodeToString([]byte("v1:10")),
		"negative offset":     base64.RawURLEncoding.EncodeToString([]byte("v1:-1:1795000000000000000")),
		"offset over int32":   base64.RawURLEncoding.EncodeToString([]byte("v1:2147483648:1795000000000000000")),
		"non-positive cutoff": base64.RawURLEncoding.EncodeToString([]byte("v1:10:0")),
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := analytics.ParseUsageCursor(token)
			assert.ErrorIs(t, err, analytics.ErrUsageCursorInvalid)
		})
	}
}
