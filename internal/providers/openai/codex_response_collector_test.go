package openai

import (
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexResponseCollector_MarksUpstreamProgress(t *testing.T) {
	c := newCodexResponseCollector(httptest.NewRecorder())
	var output, reasoning int
	require.True(t, c.ArmOutputProgress(func() { output++ }))
	require.True(t, c.ArmReasoningProgress(func() { reasoning++ }))

	_, err := c.Write([]byte(
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\",\"output\":[]}}\n\n" +
			"data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"reasoning\"}}\n\n" +
			"data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"hm\"}\n\n" +
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hi\"}\n\n" +
			"data: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"{}\"}\n\n",
	))
	require.NoError(t, err)

	assert.Equal(t, 2, output, "text and tool-argument deltas are output progress")
	assert.Equal(t, 2, reasoning, "reasoning item and summary delta are reasoning progress")
}

func TestCodexResponseCollector_MissingTerminalIsStreamIncomplete(t *testing.T) {
	c := newCodexResponseCollector(httptest.NewRecorder())
	_, err := c.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hi\"}\n\n"))
	require.NoError(t, err)

	assert.ErrorIs(t, c.finish(), translate.ErrStreamIncomplete)
}
