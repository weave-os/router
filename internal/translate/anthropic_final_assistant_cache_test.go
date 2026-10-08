package translate_test

import (
	"net/http"
	"strings"
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAnthropicFinalAssistantRejectsInvalidCacheOnEmptySuffix(t *testing.T) {
	body := []byte(`{"model":"claude-fable-5-1","messages":[{"role":"user","content":"Question"},{"role":"assistant","content":[{"type":"text","text":" \n","cache_control":{"type":"ephemeral","ttl":"2h"}}]}]}`)
	envelope, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	_, err = envelope.PrepareAnthropic(http.Header{}, translate.EmitOptions{TargetModel: "claude-fable-5-1"})
	assert.ErrorIs(t, err, translate.ErrAnthropicCacheControlInvalid)
}

func TestAnthropicFinalAssistantRejectsOriginalCacheOverflow(t *testing.T) {
	block := `{"type":"text","text":"History","cache_control":{"type":"ephemeral"}}`
	body := []byte(`{"model":"claude-fable-5-1","messages":[{"role":"user","content":[` + strings.Join([]string{block, block, block, block}, ",") + `]},{"role":"assistant","content":[{"type":"text","text":" \n","cache_control":{"type":"ephemeral"}}]}]}`)
	envelope, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	_, err = envelope.PrepareAnthropic(http.Header{}, translate.EmitOptions{TargetModel: "claude-fable-5-1"})
	assert.ErrorIs(t, err, translate.ErrAnthropicCacheControlOverflow)
}

func TestAnthropicFinalAssistantPreservesExplicitCacheOnTrimmedText(t *testing.T) {
	for _, ttl := range []string{"1h", "5m"} {
		t.Run(ttl, func(t *testing.T) {
			body := []byte(`{"model":"claude-fable-5-1","messages":[{"role":"user","content":"Question"},{"role":"assistant","content":[{"type":"text","text":"  Answer\n \t","cache_control":{"type":"ephemeral","ttl":"` + ttl + `"}}]}]}`)
			prepared := prepareFinalAssistantAnthropic(t, body)
			assert.Equal(t, "  Answer", gjson.GetBytes(prepared, "messages.1.content.0.text").String())
			assert.Equal(t, "ephemeral", gjson.GetBytes(prepared, "messages.1.content.0.cache_control.type").String())
			assert.Equal(t, ttl, gjson.GetBytes(prepared, "messages.1.content.0.cache_control.ttl").String())
		})
	}
}
