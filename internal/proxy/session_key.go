package proxy

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"

	"weave-os/router/internal/observability"
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
	key := deriveSessionKey(env, apiKeyID, clientSessionID)
	log := observability.FromContext(ctx).With(
		"session_key", shortKey(key),
		"api_key_id", apiKeyID,
		"ingress", ingress,
	)
	// Re-bind request_id only when the caller reached this without passing
	// through the HTTP middleware (background paths, tests).
	if observability.RequestIDFromContext(ctx) == "" {
		log = log.With("request_id", requestID)
	}
	// The client's own session id, bound when present so operators can grep
	// by the id the user actually sees. Envelope first (Claude Code packs it
	// in metadata.user_id); header identity covers Codex, which sends
	// Session-Id and never a Chat Completions `user` field.
	cs := ""
	if env != nil {
		cs = env.ClientSessionID()
	}
	if cs == "" {
		cs = ClientIdentityFrom(ctx).SessionID
	}
	if cs != "" {
		log = log.With("client_session_id", cs)
	}
	// Promote rather than merely attach: the access log and any FromGin caller
	// read the gin-bound logger, so session_key would otherwise never reach
	// the one line guaranteed to exist per request.
	return observability.PromoteRequestLogger(ctx, log), log, key
}

// DeriveSessionKey produces a 16-byte session digest from apiKeyID, the
// client's session identifier when present, and the first user message.
//
// HTTP request paths should use deriveSessionKeyForRequest so header-only
// identifiers (Codex Session-Id, Claude Code X-Claude-Code-Session-Id) are
// included. Envelope-only callers still pick up metadata.user_id /
// ClientSessionID from the body.
//
// The first user message remains load-bearing: Claude Code's session id
// identifies the parent conversation, not a Task/Explore sub-agent. Keying on
// session id alone would collapse concurrent threads onto one pin. Each
// thread's first user message is stable across turns but distinct per
// sub-agent, so it separates them while keeping each pin stable.
//
// System text substitutes for an empty first user message only when no client
// session id is present, because it is per-turn volatile on the harnesses that
// lead with a system message.
func DeriveSessionKey(env *translate.RequestEnvelope, apiKeyID string) [sessionpin.SessionKeyLen]byte {
	var clientSessionID string
	if env != nil {
		clientSessionID = env.ClientSessionID()
	}
	return deriveSessionKey(env, apiKeyID, clientSessionID)
}

func deriveSessionKeyForRequest(ctx context.Context, env *translate.RequestEnvelope, apiKeyID string) [sessionpin.SessionKeyLen]byte {
	return deriveSessionKey(env, apiKeyID, clientSessionIDForRequest(ctx, env))
}

const (
	forceModelSessionKeyDomain = "force_model_session:"
	betaSessionKeyDomain       = "beta_session:"
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
	domain string,
) [sessionpin.SessionKeyLen]byte {
	h := hmac.New(sha256.New, []byte(apiKeyID))
	h.Write([]byte(domain))
	h.Write([]byte{0x00})

	if clientSessionID := clientSessionIDForRequest(ctx, env); clientSessionID != "" {
		h.Write([]byte("client_session_id:"))
		h.Write([]byte(clientSessionID))
	} else {
		// Without a client session identifier there is no safe parent scope.
		h.Write([]byte("thread_key:"))
		h.Write(threadSessionKey[:])
	}

	sum := h.Sum(nil)
	var key [sessionpin.SessionKeyLen]byte
	copy(key[:], sum[:sessionpin.SessionKeyLen])
	return key
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
	// Domain separator prevents cross-tier collisions from caller-controlled strings.
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
		// First user message still splits Claude Code sub-agents that share one
		// parent session id. The system-text fallback covers OpenAI-format
		// bodies whose leading system message leaves that empty, but only when
		// nothing else identifies the conversation: a client session id already
		// separates them, and the harnesses that lead with system rewrite it
		// per turn (cwd, timestamps), so folding it in there rerolls the key —
		// and with it the upstream prompt_cache_key — on every turn.
		disc := env.FirstUserMessageText()
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
