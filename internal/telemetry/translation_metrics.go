package telemetry

import (
	"context"
)

// TranslationMetrics is the inward-facing port for bounded translation
// counters. Concrete metric instruments belong to an observability adapter.
type TranslationMetrics interface {
	RecordTranslationCompatibility(context.Context, string, string, string, string, bool)
	RecordTranslationTransform(context.Context, string, string, string, string)
}
