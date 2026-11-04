package telemetry

import (
	"time"

	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
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

// LogRecord is a lightweight high-fidelity event converted to an OTLP log
// record at flush time. Name becomes the log body (event name, matching the
// Claude Code plugin convention); structured fields live in Attrs.
type LogRecord struct {
	Name     string
	Time     time.Time
	Severity Severity
	Attrs    []*commonv1.KeyValue
}
