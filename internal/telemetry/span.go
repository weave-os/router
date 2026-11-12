package telemetry

import (
	"time"
)

// Span is a lightweight telemetry record converted to an adapter wire format
// at flush time.
type Span struct {
	Name  string
	Start time.Time
	End   time.Time
	Attrs []Attr
}
