package telemetry

import "net/http"

type Emitter interface {
	NewRecorder() Recorder
	NewUsageExtractor(http.ResponseWriter, string) (*UsageExtractor, http.ResponseWriter)
}
