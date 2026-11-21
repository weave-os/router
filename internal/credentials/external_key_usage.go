package credentials

import "context"

type ExternalKeyUsageRecorder interface {
	MarkExternalAPIKeyUsed(ctx context.Context, id string)
}
