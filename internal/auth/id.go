package auth

import (
	"crypto/rand"
	"strings"
)

const APIKeyPrefix = "rk"

const AnalyticsAPIKeyPrefix = "ra"

const SessionTokenPrefix = "rs"

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

func HasAPIKeyPrefix(token string) bool {
	return strings.HasPrefix(token, APIKeyPrefix+"_") ||
		strings.HasPrefix(token, AnalyticsAPIKeyPrefix+"_") ||
		strings.HasPrefix(token, SessionTokenPrefix+"_")
}
