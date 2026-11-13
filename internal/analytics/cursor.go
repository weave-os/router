package analytics

import (
	"encoding/base64"
	"errors"
	"strings"
	"time"
)

// ErrInvalidCursor is returned when a cursor string is not one this router issued.
var ErrInvalidCursor = errors.New("malformed analytics cursor")

// Cursor is the position of the last row of a page: the keyset the next page
// resumes strictly after. Two components because ingest timestamps collide;
// the row id breaks the tie.
type Cursor struct {
	RecordedAt time.Time
	ID         string
}

// IsZero reports whether the cursor is the first-page sentinel.
func (c Cursor) IsZero() bool {
	return c.ID == "" && c.RecordedAt.IsZero()
}

// String encodes the cursor as an opaque token so the keyset shape can change
// without breaking stored cursors. Not a security boundary.
func (c Cursor) String() string {
	if c.IsZero() {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(c.RecordedAt.UTC().Format(time.RFC3339Nano) + "|" + c.ID))
}

// ParseCursor decodes a token produced by Cursor.String.
func ParseCursor(token string) (Cursor, error) {
	if token == "" {
		return Cursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	recordedAt, id, ok := strings.Cut(string(raw), "|")
	if !ok || id == "" {
		return Cursor{}, ErrInvalidCursor
	}
	ts, err := time.Parse(time.RFC3339Nano, recordedAt)
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	return Cursor{RecordedAt: ts, ID: id}, nil
}
