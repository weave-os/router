package telemetry

import (
	"time"

	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
)

// Span is a lightweight telemetry record converted to OTLP protobuf at flush time.
type Span struct {
	Name  string
	Start time.Time
	End   time.Time
	Attrs []*commonv1.KeyValue
}
