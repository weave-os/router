package proxy

import (
	"context"
	"testing"

	"weave-os/router/internal/telemetry"
)

type recordingTranslationMetrics struct {
	compatibilityCalls int
	transformCalls     int
}

func (m *recordingTranslationMetrics) RecordTranslationCompatibility(context.Context, string, string, string, string, bool) {
	m.compatibilityCalls++
}

func (m *recordingTranslationMetrics) RecordTranslationTransform(context.Context, string, string, string, string) {
	m.transformCalls++
}

type translationMetricsEmitter struct{}

func (translationMetricsEmitter) NewRecorder() telemetry.Recorder {
	return nil
}

func TestWithTranslationMetricsRecordsThroughPort(t *testing.T) {
	primaryMetrics := &recordingTranslationMetrics{}
	service := (&Service{emitter: translationMetricsEmitter{}}).
		WithTranslationMetrics(primaryMetrics)

	ctx := context.Background()
	service.translationMetrics.RecordTranslationCompatibility(ctx, "requirement", "source", "target", "mode", true)
	service.translationMetrics.RecordTranslationTransform(ctx, "code", "action", "source", "mode")

	if primaryMetrics.compatibilityCalls != 1 || primaryMetrics.transformCalls != 1 {
		t.Fatalf("primary metrics calls = %d/%d, want 1/1", primaryMetrics.compatibilityCalls, primaryMetrics.transformCalls)
	}
}
