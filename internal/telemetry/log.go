package telemetry

import (
	"time"
)

type Severity int

const (
	SeverityInfo Severity = iota
	SeverityError
)

type LogRecord struct {
	Name     string
	Time     time.Time
	Severity Severity
	Attrs    []Attr
}
