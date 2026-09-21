package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
)

// reasoningReplayScope fingerprints the upstream an attempt dispatches to:
// provider, wire model, endpoint, and credential. An upstream decrypts a
// reasoning item only under the account and model that produced it — Snowflake
// Cortex answers "encrypted reasoning was created for a different account or
// model", xAI "Encrypted content could not be decrypted or parsed" — and a
// rejected turn ends the client session rather than degrading it. Carrying the
// fingerprint inside the signature the router mints lets the next turn replay
// the reasoning only where it still decrypts.
//
// The credential is hashed, never carried: the signature travels through the
// client.
func reasoningReplayScope(ctx context.Context, d router.Decision) string {
	h := sha256.New()
	for _, part := range []string{
		d.Provider,
		requestcontext.EffectiveUpstreamModel(ctx, d.Model),
		requestcontext.EffectiveBaseURL(ctx, ""),
	} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	if creds := requestcontext.CredentialsFromContext(ctx); creds != nil {
		key := sha256.Sum256(creds.APIKey)
		account := sha256.Sum256(creds.AccountID)
		h.Write([]byte(creds.Source))
		h.Write([]byte{0})
		h.Write(key[:])
		h.Write(account[:])
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}
