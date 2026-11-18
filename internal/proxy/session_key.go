package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
)

func shortKey(key [sessionpin.SessionKeyLen]byte) string {
	var zero [sessionpin.SessionKeyLen]byte
	if key == zero {
		return ""
	}
	return hex.EncodeToString(key[:8])
}

func sessionKeyHex(key [sessionpin.SessionKeyLen]byte) string {
	var zero [sessionpin.SessionKeyLen]byte
	if key == zero {
		return ""
	}
	return hex.EncodeToString(key[:])
}

func sessionAffinityHint(key [sessionpin.SessionKeyLen]byte) string {
	return sessionKeyHex(key)
}

func requestIDFor(ctx context.Context) string {
	if id := observability.RequestIDFromContext(ctx); id != "" {
		return id
	}
	return uuid.New().String()
}

func bindRequestLogger(
	ctx context.Context,
	env *translate.RequestEnvelope,
	apiKeyID, requestID, ingress string,
) (context.Context, *slog.Logger, [sessionpin.SessionKeyLen]byte) {
	clientSessionID := clientSessionIDForRequest(ctx, env)
	ctx = observability.WithClientSessionID(ctx, clientSessionID)
	key := deriveSessionKeyForRequest(ctx, env, apiKeyID)
	log := observability.FromContext(ctx).With(
		"session_key", shortKey(key),
		"api_key_id", apiKeyID,
		"ingress", ingress,
	)
	if observability.RequestIDFromContext(ctx) == "" {
		log = log.With("request_id", requestID)
	}
	if clientSessionID != "" {
		log = log.With("client_session_id", clientSessionID)
	}
	return observability.PromoteRequestLogger(ctx, log), log, key
}

func DeriveSessionKey(env *translate.RequestEnvelope, apiKeyID string) [sessionpin.SessionKeyLen]byte {
	var clientSessionID string
	if env != nil {
		clientSessionID = env.ClientSessionID()
	}
	return deriveSessionKey(env, apiKeyID, clientSessionID)
}

func sessionCredentialIdentity(ctx context.Context, apiKeyID string) string {
	if identity, managed := requestcontext.ServingIdentityFromContext(ctx); managed && identity.CredentialIdentity != "" {
		return identity.CredentialIdentity
	}
	return apiKeyID
}

func deriveSessionKeyForRequest(ctx context.Context, env *translate.RequestEnvelope, apiKeyID string) [sessionpin.SessionKeyLen]byte {
	if thread, ok := ctx.Value(classifierThreadContextKey{}).(router.ClassifierThread); ok {
		digest := sha256.Sum256([]byte("classifier-thread:" + thread.InstallationID.String() + ":" + thread.ThreadID.String()))
		return [sessionpin.SessionKeyLen]byte(digest[:sessionpin.SessionKeyLen])
	}
	if claims := piSessionFromContext(ctx); claims != nil && claims.APIKeyID == apiKeyID && claims.matches(ctx, env) {
		return claims.SessionKey
	}
	key := deriveSessionKey(env, sessionCredentialIdentity(ctx, apiKeyID), clientSessionIDForRequest(ctx, env))
	if path := codexSubagentPath(ctx); path != "" {
		digest := sha256.Sum256([]byte("codex_agent:" + sessionKeyHex(key) + ":" + path))
		copy(key[:], digest[:sessionpin.SessionKeyLen])
	}
	copy(key[:], requestcontext.ServingStateKey(ctx, key[:]))
	return key
}

const (
	forceModelSessionKeyDomain = requestcontext.ForceModelConversationKey
	betaSessionKeyDomain       = requestcontext.LegacyBetaConversationKey
)

func deriveForceModelSessionKeyForRequest(
	ctx context.Context,
	env *translate.RequestEnvelope,
	apiKeyID string,
	threadSessionKey [sessionpin.SessionKeyLen]byte,
) [sessionpin.SessionKeyLen]byte {
	return deriveConversationSessionKeyForRequest(ctx, env, apiKeyID, threadSessionKey, forceModelSessionKeyDomain)
}

func deriveBetaSessionKeyForRequest(
	ctx context.Context,
	env *translate.RequestEnvelope,
	apiKeyID string,
	threadSessionKey [sessionpin.SessionKeyLen]byte,
) [sessionpin.SessionKeyLen]byte {
	return deriveConversationSessionKeyForRequest(ctx, env, apiKeyID, threadSessionKey, betaSessionKeyDomain)
}

func deriveConversationSessionKeyForRequest(
	ctx context.Context,
	env *translate.RequestEnvelope,
	apiKeyID string,
	threadSessionKey [sessionpin.SessionKeyLen]byte,
	domain requestcontext.ConversationKeyDomain,
) [sessionpin.SessionKeyLen]byte {
	return requestcontext.ConversationKey(sessionCredentialIdentity(ctx, apiKeyID), clientSessionIDForRequest(ctx, env), domain, threadSessionKey)
}

func clientSessionIDForRequest(ctx context.Context, env *translate.RequestEnvelope) string {
	if id := ClientIdentityFrom(ctx).SessionID; id != "" {
		return id
	}
	if env != nil {
		return env.ClientSessionID()
	}
	return ""
}

func deriveSessionKey(env *translate.RequestEnvelope, apiKeyID, clientSessionID string) [sessionpin.SessionKeyLen]byte {
	h := sha256.New()
	h.Write([]byte(apiKeyID))
	h.Write([]byte{0x00})

	if clientSessionID != "" {
		h.Write([]byte("client_session_id:"))
		h.Write([]byte(clientSessionID))
		h.Write([]byte{0x00})
	} else if env != nil {
		if uid := env.MetadataUserID(); uid != "" {
			h.Write([]byte("user_id:"))
			h.Write([]byte(uid))
			h.Write([]byte{0x00})
		}
	}
	if env != nil {
		disc := env.ThreadDiscriminatorText()
		if disc == "" && clientSessionID == "" {
			disc = env.SystemText()
		}
		h.Write([]byte("first_msg:"))
		h.Write([]byte(disc))
	}

	sum := h.Sum(nil)
	var key [sessionpin.SessionKeyLen]byte
	copy(key[:], sum[:sessionpin.SessionKeyLen])
	return key
}
