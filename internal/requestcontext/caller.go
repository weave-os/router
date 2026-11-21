package requestcontext

import (
	"context"

	"weave-os/router/internal/auth"
)

func CallerFrom(ctx context.Context) auth.Caller {
	id := ClientIdentityFrom(ctx)
	_, internalTest := InternalTestIdentityFrom(ctx)
	caller := auth.Caller{
		Email:        id.Email,
		AccountID:    id.AccountID,
		DisplayName:  id.DisplayName,
		SessionID:    id.SessionID,
		InternalTest: internalTest,
	}
	if serving, managed := ServingIdentityFromContext(ctx); managed {
		caller.CredentialIdentity = serving.CredentialIdentity
	}
	return caller
}
