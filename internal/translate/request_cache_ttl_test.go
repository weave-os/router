package translate_test

import (
	"testing"
	"time"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestEnvelopeCacheWriteTTL(t *testing.T) {
	cases := map[string]struct {
		body string
		want time.Duration
	}{
		"no cache control":     {`{"model":"m","messages":[{"role":"user","content":"hi"}]}`, 0},
		"default five minutes": {`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}]}`, 5 * time.Minute},
		"one hour":             {`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`, time.Hour},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env, err := translate.ParseAnthropic([]byte(tc.body))
			require.NoError(t, err)
			assert.Equal(t, tc.want, env.CacheWriteTTL())
		})
	}
}
