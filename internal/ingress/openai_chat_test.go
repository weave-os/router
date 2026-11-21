package ingress_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/ingress"
)

const chatFeedbackFooter = "\n\n_Weave Router feedback:_ `/rf +` good experience · `/rf -` poor experience · note optional, e.g. `/rf - too slow`"

func TestOpenAIChatParseStripsTheEchoedFeedbackFooter(t *testing.T) {
	body := `{"model":"gpt-5","stream":true,"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"real response` + chatFeedbackFooter + `"},{"role":"user","content":"again"}]}`
	env, forwarded, err := ingress.OpenAIChat{}.Parse(context.Background(), []byte(body))
	require.NoError(t, err)
	assert.Equal(t, "real response", gjson.GetBytes(forwarded, "messages.1.content").String())
	assert.Equal(t, "gpt-5", env.Model())
	assert.True(t, env.Stream())
}

func TestOpenAIChatParseRejectsAnInvalidBody(t *testing.T) {
	_, _, err := ingress.OpenAIChat{}.Parse(context.Background(), []byte(`{"model":`))
	require.Error(t, err)
}
