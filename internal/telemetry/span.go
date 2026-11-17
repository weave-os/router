package telemetry

import (
	"time"
)

type Span struct {
	Name  string
	Start time.Time
	End   time.Time
	Attrs []Attr
}
