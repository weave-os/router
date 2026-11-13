package escalationdashboard

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCursorRoundTrip(t *testing.T) {
	snapshotID := "236d9b20-02c0-4422-b0d9-a8b1b31049f8"
	cursor, err := EncodeCursor(snapshotID, 150)
	require.NoError(t, err)

	decodedSnapshotID, position, err := DecodeCursor(cursor)

	require.NoError(t, err)
	require.Equal(t, snapshotID, decodedSnapshotID)
	require.Equal(t, int32(150), position)
}

func TestCursorRejectsMalformedValues(t *testing.T) {
	_, err := EncodeCursor("not-a-uuid", 0)
	require.ErrorIs(t, err, ErrInvalidCursor)
	_, err = EncodeCursor("236d9b20-02c0-4422-b0d9-a8b1b31049f8", -1)
	require.ErrorIs(t, err, ErrInvalidCursor)
	_, _, err = DecodeCursor("not-a-cursor")
	require.ErrorIs(t, err, ErrInvalidCursor)
}
