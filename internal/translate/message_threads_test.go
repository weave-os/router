package translate_test

import (
	"net/http"
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
)

func TestStripMessageThreadsBeta(t *testing.T) {
	h := http.Header{}
	h.Add("anthropic-beta", "claude-code-20250219, message-threads-2026-08-12")
	h.Add("anthropic-beta", "fast-mode-2026-02-01")
	translate.StripMessageThreadsBeta(h)
	assert.Equal(t, []string{"claude-code-20250219,fast-mode-2026-02-01"}, h.Values("anthropic-beta"))

	only := http.Header{"Anthropic-Beta": {"message-threads-2026-08-12"}}
	translate.StripMessageThreadsBeta(only)
	assert.Empty(t, only.Values("anthropic-beta"))
}
