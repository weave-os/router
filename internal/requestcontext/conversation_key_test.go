package requestcontext_test

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/requestcontext"
)

func TestConversationKeyPreservesLegacyGoldenBytes(t *testing.T) {
	key := requestcontext.ConversationKey("api-key-a", "session-a", requestcontext.LegacyBetaConversationKey, [16]byte{})
	// Independently generated with OpenSSL HMAC-SHA256 before extracting the legacy implementation.
	assert.Equal(t, "89affb3ee96a73d61673b4ec87f1b4fc", hex.EncodeToString(key[:]))
	assert.NotEqual(t, key, requestcontext.ConversationKey("api-key-b", "session-a", requestcontext.LegacyBetaConversationKey, [16]byte{}))
	assert.NotEqual(t, key, requestcontext.ConversationKey("api-key-a", "session-a", requestcontext.ForceModelConversationKey, [16]byte{}))
}

func TestSiblingPreferenceConversationIdentitySurvivesCompaction(t *testing.T) {
	before := [16]byte{1}
	after := [16]byte{2}
	key := requestcontext.ConversationKey("credential-a", "session-a", requestcontext.SiblingPreferenceConversationKey, before)
	assert.Equal(t, key, requestcontext.ConversationKey("credential-a", "session-a", requestcontext.SiblingPreferenceConversationKey, after))
	assert.NotEqual(t, key, requestcontext.ConversationKey("credential-a", "session-b", requestcontext.SiblingPreferenceConversationKey, before))
	assert.NotEqual(t, key, requestcontext.ConversationKey("credential-b", "session-a", requestcontext.SiblingPreferenceConversationKey, before))
	assert.NotEqual(t, key, requestcontext.ConversationKey("credential-a", "session-a", requestcontext.ForceModelConversationKey, before))
	assert.NotEqual(t,
		requestcontext.ConversationKey("credential-a", "", requestcontext.SiblingPreferenceConversationKey, before),
		requestcontext.ConversationKey("credential-a", "", requestcontext.SiblingPreferenceConversationKey, after),
		"sessionless callers remain isolated by thread")
}
