package telemetry

import (
	"context"
)

// TranslationMetrics is the inward-facing port for bounded translation
// counters. Concrete metric instruments belong to an observability adapter.
type TranslationMetrics interface {
	RecordTranslationCompatibility(ctx context.Context, requirement, sourceFormat, targetFamily, mode string, enforced bool)
	RecordTranslationTransform(ctx context.Context, code, action, sourceFormat, mode string)
}
