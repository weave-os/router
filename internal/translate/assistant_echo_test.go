package translate_test

import (
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
)

func TestEchoedInAssistantText(t *testing.T) {
	const shown = "✦ **Weave Router** → shown warning\n\n"
	const notShown = "✦ **Weave Router** → other warning\n\n"
	needles := []string{shown, notShown}

	for name, body := range map[string]string{
		"anthropic text block":  `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"text","text":"✦ **Weave Router** → shown warning\n\nhello"}]}]}`,
		"chat string content":   `{"messages":[{"role":"assistant","content":"✦ **Weave Router** → shown warning"}]}`,
		"responses output_text": `{"input":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"⁣⁠⁣⁠✦ **Weave Router** → shown warning\n\nhello"}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			echoed := translate.EchoedInAssistantText([]byte(body), needles)
			assert.True(t, echoed[shown])
			assert.False(t, echoed[notShown])
		})
	}

	t.Run("user text does not count", func(t *testing.T) {
		body := `{"messages":[{"role":"user","content":"✦ **Weave Router** → shown warning"}]}`
		assert.False(t, translate.EchoedInAssistantText([]byte(body), needles)[shown])
	})
}
