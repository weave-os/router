package proxy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/translate"
)

// The portable projection drops web_search_call, so a Responses turn that just
// searched must still report that use from its native body.
func TestSearchUseEnvelope_ReadsNativeResponsesHistory(t *testing.T) {
	native := []byte(`{"model":"auto","tools":[{"type":"web_search"}],"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"find it"}]},{"type":"web_search_call","id":"ws_1","status":"completed"}]}`)
	conversion, err := translate.ConvertResponsesToChatCompletionsWithOptions(native, translate.ResponsesConversionOptions{PortableCodex: true})
	require.NoError(t, err)
	projected, err := translate.ParseOpenAI(conversion.Body)
	require.NoError(t, err)
	require.Equal(t, -1, projected.SearchToolUseRecency(), "the projection carries no search call")

	ctx := context.WithValue(context.Background(), nativeResponsesBodyContextKey{}, native)
	assert.Equal(t, 0, searchUseEnvelope(ctx, projected).SearchToolUseRecency())
	assert.Same(t, projected, searchUseEnvelope(context.Background(), projected))
}
