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

type translationMetricsEmitter struct {
	fallback telemetry.TranslationMetrics
}

func (translationMetricsEmitter) NewRecorder() telemetry.Recorder {
	return nil
}

func (e translationMetricsEmitter) TranslationMetrics() telemetry.TranslationMetrics {
	return e.fallback
}

func TestWithTranslationMetricsTakesPrecedenceOverEmitter(t *testing.T) {
	emitterMetrics := &recordingTranslationMetrics{}
	primaryMetrics := &recordingTranslationMetrics{}
	service := (&Service{emitter: translationMetricsEmitter{fallback: emitterMetrics}}).
		WithTranslationMetrics(primaryMetrics)

	service.recordTranslationCompatibility(context.Background(), "requirement", "source", "target", "mode", true)
	service.recordTranslationTransform(context.Background(), "code", "action", "source", "mode")

	if primaryMetrics.compatibilityCalls != 1 || primaryMetrics.transformCalls != 1 {
		t.Fatalf("primary metrics calls = %d/%d, want 1/1", primaryMetrics.compatibilityCalls, primaryMetrics.transformCalls)
	}
	if emitterMetrics.compatibilityCalls != 0 || emitterMetrics.transformCalls != 0 {
		t.Fatalf("emitter metrics calls = %d/%d, want 0/0", emitterMetrics.compatibilityCalls, emitterMetrics.transformCalls)
	}
}
