package proxy

import (
	"context"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/requestcontext"
)

func CredentialsFromContext(ctx context.Context) *requestcontext.Credentials {
	return credentials.CredentialsFromContext(ctx)
}
