package requestcontext

import (
	"context"

	"weave-os/router/internal/auth"
)

func CallerFrom(ctx context.Context) auth.Caller {
	identity := ClientIdentityFrom(ctx)
	_, internalTest := InternalTestIdentityFrom(ctx)
	caller := auth.Caller{
		Email:        identity.Email,
		AccountID:    identity.AccountID,
		DisplayName:  identity.DisplayName,
		SessionID:    identity.SessionID,
		InternalTest: internalTest,
	}
	if serving, managed := ServingIdentityFromContext(ctx); managed {
		caller.CredentialIdentity = serving.CredentialIdentity
	}
	return caller
}
