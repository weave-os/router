package analytics

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

var ErrUsageCursorInvalid = errors.New("malformed usage cursor")

const usageCursorVersion = "v1"

type UsageCursor struct {
	Offset         int
	IngestedBefore time.Time
}

func (c UsageCursor) IsZero() bool { return c.Offset == 0 && c.IngestedBefore.IsZero() }

func (c UsageCursor) String() string {
	if c.IsZero() {
		return ""
	}
	token := usageCursorVersion + ":" + strconv.Itoa(c.Offset) + ":" + strconv.FormatInt(c.IngestedBefore.UnixNano(), 10)
	return base64.RawURLEncoding.EncodeToString([]byte(token))
}

func ParseUsageCursor(token string) (UsageCursor, error) {
	if token == "" {
		return UsageCursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return UsageCursor{}, ErrUsageCursorInvalid
	}
	parts := strings.Split(string(raw), ":")
	if len(parts) != 3 || parts[0] != usageCursorVersion {
		return UsageCursor{}, ErrUsageCursorInvalid
	}
	offset, err := strconv.ParseInt(parts[1], 10, 32)
	if err != nil || offset < 0 {
		return UsageCursor{}, ErrUsageCursorInvalid
	}
	ingestedBeforeNanos, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || ingestedBeforeNanos <= 0 {
		return UsageCursor{}, ErrUsageCursorInvalid
	}
	return UsageCursor{Offset: int(offset), IngestedBefore: time.Unix(0, ingestedBeforeNanos).UTC()}, nil
}
