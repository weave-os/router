package proxy_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/translate"
)

func anthropicEnv(t *testing.T, body string) *translate.RequestEnvelope {
	t.Helper()
	envelope, err := translate.ParseAnthropic([]byte(body))
	require.NoError(t, err)
	return envelope
}
