package telemetry

import (
	"context"
)

type TranslationMetrics interface {
	RecordTranslationCompatibility(ctx context.Context, requirement, sourceFormat, targetFamily, mode string, enforced bool)
	RecordTranslationTransform(ctx context.Context, code, action, sourceFormat, mode string)
}
