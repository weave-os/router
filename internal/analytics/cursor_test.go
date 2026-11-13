package analytics_test

import (
	"encoding/base64"
	"testing"
	"time"

	"weave-os/router/internal/analytics"

	"github.com/stretchr/testify/require"
)

func TestCursorRoundTripsWithSubSecondPrecision(t *testing.T) {
	// Ingest timestamps collide at second granularity under load, so a cursor
	// that loses sub-second precision would replay or skip rows.
	original := analytics.Cursor{
		RecordedAt: time.Date(2026, 3, 4, 5, 6, 7, 123456789, time.UTC),
		ID:         "8f4c1b2e-0000-4000-8000-000000000001",
	}

	parsed, err := analytics.ParseCursor(original.String())

	require.NoError(t, err)
	require.True(t, parsed.RecordedAt.Equal(original.RecordedAt))
	require.Equal(t, original.ID, parsed.ID)
}

func TestCursorIsOpaque(t *testing.T) {
	token := analytics.Cursor{RecordedAt: time.Now(), ID: "row-1"}.String()

	require.NotContains(t, token, "row-1")
	require.NotContains(t, token, "|")
}

func TestZeroCursorEncodesEmpty(t *testing.T) {
	require.Empty(t, analytics.Cursor{}.String())
}

func TestParseEmptyCursorIsFirstPage(t *testing.T) {
	c, err := analytics.ParseCursor("")

	require.NoError(t, err)
	require.True(t, c.IsZero())
}

func TestParseCursorRejectsGarbage(t *testing.T) {
	cases := map[string]string{
		"not base64":       "!!!!",
		"missing id":       base64.RawURLEncoding.EncodeToString([]byte("2026-01-01T00:00:00Z")),
		"empty id":         base64.RawURLEncoding.EncodeToString([]byte("2026-01-01T00:00:00Z|")),
		"unparseable time": base64.RawURLEncoding.EncodeToString([]byte("yesterday|row-1")),
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := analytics.ParseCursor(token)
			require.ErrorIs(t, err, analytics.ErrInvalidCursor)
		})
	}
}
