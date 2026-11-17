package translate

import (
	"encoding/base64"
	"strings"
)

const thoughtSignatureIDDelimiter = "__thought__"

func embedSignatureInID(id, sig string) string {
	if sig == "" || strings.Contains(id, thoughtSignatureIDDelimiter) {
		return id
	}
	return id + thoughtSignatureIDDelimiter + base64.RawURLEncoding.EncodeToString([]byte(sig))
}

func extractSignatureFromID(id string) (cleanID, signature string) {
	i := strings.Index(id, thoughtSignatureIDDelimiter)
	if i < 0 {
		return id, ""
	}
	encoded := id[i+len(thoughtSignatureIDDelimiter):]
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return id, ""
	}
	return id[:i], string(decoded)
}
