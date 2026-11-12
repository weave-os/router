package telemetry

import (
	"time"
)

// Severity is the log-record level. Kept as a small local enum so the buffer /
// proxy API does not leak the OTLP proto severity type.
type Severity int

const (
	// SeverityInfo marks routine high-fidelity events (decision, call).
	SeverityInfo Severity = iota
	// SeverityError marks upstream/router failures.
	SeverityError
)

// LogRecord is a lightweight high-fidelity event converted to an adapter log
// record at flush time. Name becomes the log body; structured fields live in
// Attrs.
type LogRecord struct {
	Name     string
	Time     time.Time
	Severity Severity
	Attrs    []Attr
}
