package credentials

import (
	"context"

	"weave-os/router/internal/requestcontext"
)

type ExternalKeyUsageRecorder interface {
	MarkExternalAPIKeyUsed(ctx context.Context, id string)
}

func (b *Brokerage) MarkServedExternalKeyUsed(ctx context.Context, creds *requestcontext.Credentials) {
	if b.ExternalKeyUsage == nil {
		return
	}
	if creds == nil || creds.Source != requestcontext.SourceBYOK || creds.ExternalKeyID == "" {
		return
	}
	b.ExternalKeyUsage.MarkExternalAPIKeyUsed(ctx, creds.ExternalKeyID)
}
