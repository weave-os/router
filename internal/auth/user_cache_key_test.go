package auth_test

import (
	"testing"
	"time"

	"weave-os/router/internal/auth"

	"github.com/stretchr/testify/assert"
)

func TestLRUUserCache_KeySeparatesInstallationFromIdentity(t *testing.T) {
	cache := auth.NewLRUUserCache(16, time.Hour)
	cache.Set("inst-ab", "c", "user-1")

	_, ok := cache.Get("inst-a", "bc")

	assert.False(t, ok, "installation and identity must not concatenate into the same cache key")
}
