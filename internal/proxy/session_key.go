package proxy

import (
	"context"
	"encoding/hex"
	"log/slog"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
)

// shortKey returns the first 16 hex chars of a session key for log
// correlation. Empty for the zero key to avoid a misleading prefix.
func shortKey(key [sessionpin.SessionKeyLen]byte) string {
	var zero [sessionpin.SessionKeyLen]byte
	if key == zero {
		return ""
	}
	return hex.EncodeToString(key[:8])
}

// sessionKeyHex returns the full 32-hex session digest for telemetry joins;
// unlike shortKey, it retains all 16 bytes so parallel threads don't collapse.
func sessionKeyHex(key [sessionpin.SessionKeyLen]byte) string {
	var zero [sessionpin.SessionKeyLen]byte
	if key == zero {
		return ""
	}
	return hex.EncodeToString(key[:])
}

// sessionAffinityHint returns the session key hex for upstream prompt-cache
// stickiness, or "" for the zero key so sessionless requests stay unbucketed.
func sessionAffinityHint(key [sessionpin.SessionKeyLen]byte) string {
	return sessionKeyHex(key)
}

// requestIDFor returns the correlation id already stamped by
// observability.Middleware, minting one only for callers that bypassed HTTP
// (background paths, tests). Reusing the middleware's id is what lets a
// telemetry row join to the log lines for the same request.
func requestIDFor(ctx context.Context) string {
	if id := observability.RequestIDFromContext(ctx); id != "" {
		return id
	}
	return uuid.New().String()
}

// bindRequestLogger derives the session key and returns a context carrying a
// logger pre-bound with session_key, request_id, api_key_id, and ingress, so
// a session's path can be filtered in Cloud Logging by session_key alone. It
// also returns the key so callers don't have to re-derive it.
//
// request_id is already bound by observability.Middleware at first touch, so
// only the fields that need the parsed envelope are added here.
func bindRequestLogger(
	ctx context.Context,
	env *translate.RequestEnvelope,
	apiKeyID, requestID, ingress string,
) (context.Context, *slog.Logger, [sessionpin.SessionKeyLen]byte) {
	clientSessionID := clientSessionIDForRequest(ctx, env)
	ctx = observability.WithClientSessionID(ctx, clientSessionID)
	scope := automaticStateScopeForRequest(ctx, env, apiKeyID)
	key := scope.Key
	log := observability.FromContext(ctx).With(
		"thread_identity_source", scope.Source,
		"thread_identity_status", scope.Status,
		"automatic_state_persistent", scope.Persistent,
		"session_key", shortKey(key),
		"api_key_id", apiKeyID,
		"ingress", ingress,
	)
	// Re-bind request_id only when the caller reached this without passing
	// through the HTTP middleware (background paths, tests).
	if observability.RequestIDFromContext(ctx) == "" {
		log = log.With("request_id", requestID)
	}
	// Promote rather than merely attach: the access log and any FromGin caller
	// read the gin-bound logger, so session_key would otherwise never reach
	// the one line guaranteed to exist per request.
	return observability.PromoteRequestLogger(ctx, log), log, key
}

func sessionCredentialIdentity(ctx context.Context, apiKeyID string) string {
	if identity, managed := requestcontext.ServingIdentityFromContext(ctx); managed && identity.CredentialIdentity != "" {
		return identity.CredentialIdentity
	}
	return apiKeyID
}

func deriveSessionKeyForRequest(ctx context.Context, env *translate.RequestEnvelope, apiKeyID string) [sessionpin.SessionKeyLen]byte {
	return automaticStateScopeForRequest(ctx, env, apiKeyID).Key
}

func automaticStateScopeForRequest(ctx context.Context, env *translate.RequestEnvelope, apiKeyID string) requestcontext.AutomaticStateScope {
	identity := requestcontext.ThreadIdentityFromContext(ctx)
	if identity.Status == "" {
		identity.Status = requestcontext.ThreadIdentityMissing
		identity.Source = requestcontext.ThreadSourceUnavailable
	}
	if identity.Status == requestcontext.ThreadIdentityInvalid || identity.Status == requestcontext.ThreadIdentityConflicting {
		return requestcontext.AutomaticStateScope{Source: identity.Source, Status: identity.Status}
	}
	if identity.ConversationID == "" {
		identity.ConversationID = clientSessionIDForRequest(ctx, env)
	}
	if thread, ok := ctx.Value(classifierThreadContextKey{}).(router.ClassifierThread); ok {
		identity = requestcontext.ThreadIdentity{ThreadID: thread.ThreadID.String(), Source: requestcontext.ThreadSourceClassifier, Status: requestcontext.ThreadIdentityAvailable}
	}
	if claims := piSessionFromContext(ctx); claims != nil && claims.APIKeyID == apiKeyID && claims.matches(ctx, env) {
		if claims.StateVersion == automaticThreadStateVersion {
			return requestcontext.AutomaticStateScope{Key: claims.SessionKey, Persistent: true, Source: requestcontext.ThreadSourcePiTicket, Status: requestcontext.ThreadIdentityAvailable}
		}
	}
	scope := identity.AutomaticScope(sessionCredentialIdentity(ctx, apiKeyID), installationIDFromContext(ctx).String())
	if scope.Persistent {
		copy(scope.Key[:], requestcontext.ServingStateKey(ctx, scope.Key[:]))
	}
	return scope
}

const (
	forceModelSessionKeyDomain = requestcontext.ForceModelConversationKey
	betaSessionKeyDomain       = requestcontext.LegacyBetaConversationKey
)

// deriveForceModelSessionKeyForRequest omits the first-message discriminator
// so an explicit force applies to every thread in one client session.
func deriveForceModelSessionKeyForRequest(
	ctx context.Context,
	env *translate.RequestEnvelope,
	apiKeyID string,
	threadSessionKey [sessionpin.SessionKeyLen]byte,
) [sessionpin.SessionKeyLen]byte {
	return deriveConversationSessionKeyForRequest(ctx, env, apiKeyID, threadSessionKey, forceModelSessionKeyDomain)
}

// deriveBetaSessionKeyForRequest scopes the /beta preference to the whole
// client session: compaction rewrites the first user message and sub-agents
// carry their own, so a thread-keyed preference would vanish on both.
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
	if clientSessionIDForRequest(ctx, env) == "" {
		return [sessionpin.SessionKeyLen]byte{}
	}
	return requestcontext.ConversationKey(sessionCredentialIdentity(ctx, apiKeyID), clientSessionIDForRequest(ctx, env), domain, threadSessionKey)
}

func clientSessionIDForRequest(ctx context.Context, env *translate.RequestEnvelope) string {
	if identity := requestcontext.ThreadIdentityFromContext(ctx); identity.ConversationID != "" || (identity.Source == requestcontext.ThreadSourceOpenCode && identity.ParentThreadID != "") {
		return identity.ConversationID
	}
	if id := ClientIdentityFrom(ctx).SessionID; id != "" {
		return id
	}
	if env != nil {
		return env.ClientSessionID()
	}
	return ""
}
