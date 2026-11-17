package credentials_test

import (
	"strings"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/credentials"

	"github.com/stretchr/testify/assert"
)

func TestKeyParts_LongKeyKeepsOnlyEnds(t *testing.T) {
	key := "sk-" + strings.Repeat("x", 40) + "tail"
	prefix, suffix := credentials.KeyParts([]byte(key))
	assert.Equal(t, key[:auth.APITokenPrefixLen], prefix)
	assert.Equal(t, key[len(key)-auth.APITokenSuffixLen:], suffix)
	assert.NotContains(t, prefix+suffix, strings.Repeat("x", 40))
}

func TestKeyParts_ShortKeyIsReturnedWholeAsPrefix(t *testing.T) {
	prefix, suffix := credentials.KeyParts([]byte("short"))
	assert.Equal(t, "short", prefix)
	assert.Empty(t, suffix)
}
