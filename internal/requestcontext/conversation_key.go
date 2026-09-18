package requestcontext

import (
	"crypto/hmac"
	"crypto/sha256"
)

// ConversationKeyDomain preserves the byte namespaces of existing conversation-wide preferences.
type ConversationKeyDomain string

const (
	// ForceModelConversationKey is independent of thread-first-message model pins.
	ForceModelConversationKey ConversationKeyDomain = "force_model_session:"
	// LegacyBetaConversationKey remains the migration identity even after managed beta retirement.
	LegacyBetaConversationKey ConversationKeyDomain = "beta_session:"
)

// ConversationKey derives the existing 16-byte preference identity without protocol or runtime dependencies.
// Legacy preferences retain their thread fallback; release admission must reject an empty client ID first.
func ConversationKey(credentialIdentity, clientSessionID string, domain ConversationKeyDomain, threadKey [16]byte) [16]byte {
	hash := hmac.New(sha256.New, []byte(credentialIdentity))
	hash.Write([]byte(domain))
	hash.Write([]byte{0})
	if clientSessionID != "" {
		hash.Write([]byte("client_session_id:"))
		hash.Write([]byte(clientSessionID))
	} else {
		hash.Write([]byte("thread_key:"))
		hash.Write(threadKey[:])
	}
	var digest [16]byte
	copy(digest[:], hash.Sum(nil)[:16])
	return digest
}
