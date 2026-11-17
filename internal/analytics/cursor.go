package analytics

import (
	"encoding/base64"
	"errors"
	"strings"
	"time"
)

var ErrInvalidCursor = errors.New("malformed analytics cursor")

type Cursor struct {
	RecordedAt time.Time
	ID         string
}

func (c Cursor) IsZero() bool {
	return c.ID == "" && c.RecordedAt.IsZero()
}

func (c Cursor) String() string {
	if c.IsZero() {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(c.RecordedAt.UTC().Format(time.RFC3339Nano) + "|" + c.ID))
}

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
