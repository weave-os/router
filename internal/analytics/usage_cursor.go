package analytics

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
)

var ErrUsageCursorInvalid = errors.New("malformed usage cursor")

type UsageCursor struct {
	Offset int
}

func (c UsageCursor) IsZero() bool { return c.Offset == 0 }

func (c UsageCursor) String() string {
	if c.IsZero() {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte("o:" + strconv.Itoa(c.Offset)))
}

func ParseUsageCursor(token string) (UsageCursor, error) {
	if token == "" {
		return UsageCursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return UsageCursor{}, ErrUsageCursorInvalid
	}
	body, ok := strings.CutPrefix(string(raw), "o:")
	if !ok {
		return UsageCursor{}, ErrUsageCursorInvalid
	}
	offset, err := strconv.Atoi(body)
	if err != nil || offset < 0 {
		return UsageCursor{}, ErrUsageCursorInvalid
	}
	return UsageCursor{Offset: offset}, nil
}
