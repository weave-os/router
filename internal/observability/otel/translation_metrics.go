package otel

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"weave-os/router/internal/telemetry"
)

var _ telemetry.TranslationMetrics = (*translationMetrics)(nil)

// NewTranslationMetrics returns the OTel implementation of the translation
// metrics port. It is independent of the per-request span emitter.
func NewTranslationMetrics() telemetry.TranslationMetrics {
	return &translationMetrics{}
}

type translationMetrics struct {
	once                 sync.Once
	compatibilityCounter metric.Int64Counter
	transformCounter     metric.Int64Counter
}

func (m *translationMetrics) init() {
	m.once.Do(func() {
		meter := otel.Meter("weave-os/router/translation")
		m.compatibilityCounter, _ = meter.Int64Counter(
			"router.translation.compatibility.exclusions",
			metric.WithDescription("Translation compatibility exclusions observed during routing."),
		)
		m.transformCounter, _ = meter.Int64Counter(
			"router.translation.transforms",
			metric.WithDescription("Translation transforms observed while adapting an inbound request."),
		)
	})
}

func (m *translationMetrics) RecordTranslationCompatibility(ctx context.Context, requirement, sourceFormat, targetFamily, mode string, enforced bool) {
	if m == nil {
		return
	}
	m.init()
	if m.compatibilityCounter == nil {
		return
	}
	m.compatibilityCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("translation.requirement", requirement),
		attribute.String("translation.source_format", sourceFormat),
		attribute.String("translation.target_family", targetFamily),
		attribute.String("translation.mode", mode),
		attribute.Bool("translation.enforced", enforced),
	))
}

func (m *translationMetrics) RecordTranslationTransform(ctx context.Context, code, action, sourceFormat, mode string) {
	if m == nil {
		return
	}
	m.init()
	if m.transformCounter == nil {
		return
	}
	m.transformCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("translation.code", code),
		attribute.String("translation.action", action),
		attribute.String("translation.source_format", sourceFormat),
		attribute.String("translation.mode", mode),
	))
}
