package translate

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"weave-os/router/internal/providers"
)

type openAIReasoningSignatureEnvelope struct {
	Version  int    `json:"v"`
	Provider string `json:"provider"`
	ID       string `json:"id"`
	Enc      string `json:"enc"`
	// Scope identifies the upstream account+model the encrypted reasoning was
	// minted for. An upstream decrypts its own reasoning only under the issuing
	// account and model, so a replay outside the minting scope is a 400
	// ("encrypted reasoning was created for a different account or model").
	// Empty on envelopes minted before scoping; those replay nowhere.
	Scope string `json:"scope,omitempty"`
}

func encodeOpenAIReasoningSignature(id, enc, scope string) string {
	if id == "" || enc == "" {
		return ""
	}
	b, err := json.Marshal(openAIReasoningSignatureEnvelope{
		Version:  1,
		Provider: providers.ProviderOpenAI,
		ID:       id,
		Enc:      enc,
		Scope:    scope,
	})
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(b)
}

// decodeOpenAIReasoningSignature recognizes a router-minted envelope. Use it to
// detect one (e.g. to strip it); replaying its payload additionally requires a
// scope match, see openAIReasoningSignatureForScope.
func decodeOpenAIReasoningSignature(sig string) (openAIReasoningSignatureEnvelope, bool) {
	if sig == "" {
		return openAIReasoningSignatureEnvelope{}, false
	}
	b, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return openAIReasoningSignatureEnvelope{}, false
	}
	var env openAIReasoningSignatureEnvelope
	if err := json.Unmarshal(b, &env); err != nil {
		return openAIReasoningSignatureEnvelope{}, false
	}
	if env.Version != 1 || env.Provider != providers.ProviderOpenAI || env.ID == "" || env.Enc == "" {
		return openAIReasoningSignatureEnvelope{}, false
	}
	return env, true
}

func openAIReasoningSignatureReplayable(sig, scope string) bool {
	_, _, ok := openAIReasoningSignatureForScope(sig, scope)
	return ok
}

func openAIReasoningSignatureForScope(sig, scope string) (id, enc string, ok bool) {
	env, decoded := decodeOpenAIReasoningSignature(sig)
	if !decoded || scope == "" || env.Scope != scope {
		return "", "", false
	}
	return env.ID, env.Enc, true
}

const openAIReasoningSignatureIDDelimiter = "__openai_reasoning__"

func embedOpenAIReasoningSignatureInID(id, sig string) string {
	if id == "" || sig == "" || strings.Contains(id, openAIReasoningSignatureIDDelimiter) {
		return id
	}
	return id + openAIReasoningSignatureIDDelimiter + base64.RawURLEncoding.EncodeToString([]byte(sig))
}

// stripOpenAIReasoningCarrier removes a router-minted reasoning carrier from id.
// A suffix that does not decode as a router envelope is caller data and is kept,
// so distinct ids never collapse onto one.
func stripOpenAIReasoningCarrier(id string) (cleanID string, stripped bool) {
	cleanID, sig := extractOpenAIReasoningSignatureFromID(id)
	if _, ok := decodeOpenAIReasoningSignature(sig); !ok {
		return id, false
	}
	return cleanID, true
}

func extractOpenAIReasoningSignatureFromID(id string) (cleanID, sig string) {
	i := strings.Index(id, openAIReasoningSignatureIDDelimiter)
	if i < 0 {
		return id, ""
	}
	encoded := id[i+len(openAIReasoningSignatureIDDelimiter):]
	b, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return id[:i], ""
	}
	return id[:i], string(b)
}
