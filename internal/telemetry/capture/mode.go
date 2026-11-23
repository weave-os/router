package capture

import "strings"

type Mode int

const (
	Off Mode = iota
	Hashed
	Full
)

func StricterMode(a, b Mode) Mode {
	if b < a {
		return b
	}
	return a
}

type ContentKind int

const (
	ContentKindRequest ContentKind = iota
	ContentKindResponse
)

type Redactor func(content string, kind ContentKind) string

func ParseMode(raw string) Mode {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "full":
		return Full
	case "hashed":
		return Hashed
	default:
		return Off
	}
}

func (m Mode) String() string {
	switch m {
	case Full:
		return "full"
	case Hashed:
		return "hashed"
	default:
		return "off"
	}
}
