package auth

import (
	"crypto/rand"
	"strings"
)

// APIKeyPrefix fronts a routing (data-plane) bearer token: "rk_<24 random chars>".
const APIKeyPrefix = "rk"

// AnalyticsAPIKeyPrefix fronts a read-only analytics export token: "ra_<24 random chars>".
// A distinct prefix makes the credential's authority visible in logs and configs
// without requiring a DB lookup on the token alone.
const AnalyticsAPIKeyPrefix = "ra"

const (
	idLength               = 24
	alphaNumericCharacters = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
)

func GenerateID(prefix string) string {
	if len(prefix) > 10 {
		panic("prefix must be 10 characters or less")
	}
	var b strings.Builder
	b.WriteString(prefix)
	b.WriteString("_")
	// Rejection-sample to avoid modulo bias: 256 % 62 != 0, so a plain
	// mod would skew toward the first characters of the alphabet.
	limit := byte(256 - (256 % len(alphaNumericCharacters)))
	buf := make([]byte, idLength)
	for written := 0; written < idLength; {
		if _, err := rand.Read(buf); err != nil {
			panic(err)
		}
		for _, x := range buf {
			if x >= limit {
				continue
			}
			b.WriteByte(alphaNumericCharacters[int(x)%len(alphaNumericCharacters)])
			written++
			if written == idLength {
				break
			}
		}
	}
	return b.String()
}

// HasAPIKeyPrefix reports whether the token carries any router-issued prefix (rk_ or ra_).
// Scope enforcement happens in VerifyAPIKey / VerifyAnalyticsAPIKey, not here.
func HasAPIKeyPrefix(token string) bool {
	return strings.HasPrefix(token, APIKeyPrefix+"_") ||
		strings.HasPrefix(token, AnalyticsAPIKeyPrefix+"_")
}
