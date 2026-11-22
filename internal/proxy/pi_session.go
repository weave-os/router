package proxy

import (
	"context"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

func piSessionMatches(ctx context.Context, claims *conversation.PiSessionClaims, env *translate.RequestEnvelope) bool {
	identity, _ := requestcontext.ServingIdentityFromContext(ctx)
	return env != nil && claims.InstallationID == requestcontext.InstallationIDFromContext(ctx) &&
		claims.ServingStateNamespace == identity.StateNamespace &&
		claims.SessionID == clientSessionIDForRequest(ctx, env) && claims.MetadataUserID == env.MetadataUserID()
}

func mintPiSession(ctx context.Context, handoff conversation.PiHandoff, env *translate.RequestEnvelope, sessionKey [sessionpin.SessionKeyLen]byte) (string, error) {
	apiKeyID, _ := ctx.Value(requestcontext.APIKeyIDContextKey{}).(string)
	identity, _ := requestcontext.ServingIdentityFromContext(ctx)
	return handoff.MintSession(conversation.PiSessionClaims{
		APIKeyID: apiKeyID, InstallationID: requestcontext.InstallationIDFromContext(ctx),
		SessionID: clientSessionIDForRequest(ctx, env), MetadataUserID: env.MetadataUserID(), SessionKey: sessionKey,
		ServingStateNamespace: identity.StateNamespace,
	})
}
