package requestcontext

import (
	"crypto/hmac"
	"crypto/sha256"
)

type ConversationKeyDomain string

const (
	ForceModelConversationKey ConversationKeyDomain = "force_model_session:"
	LegacyBetaConversationKey ConversationKeyDomain = "beta_session:"
)

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
