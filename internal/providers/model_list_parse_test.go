package providers_test

import (
	"testing"

	"weave-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseModelIDs_AcceptsGatewayShapes(t *testing.T) {
	for name, body := range map[string]string{
		"openai list":  `{"object":"list","data":[{"id":"b"},{"id":"a"},{"id":"b"},{"id":""}]}`,
		"string array": `{"models":["b","a","b",""]}`,
		"object array": `{"models":[{"name":"b"},{"name":"a"},{"name":"b"}]}`,
		"model keyed":  `{"models":[{"model":"b"},{"model":"a"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			ids, err := providers.ParseModelIDs([]byte(body))
			require.NoError(t, err)
			assert.Equal(t, []string{"a", "b"}, ids)
		})
	}
}

func TestParseModelIDs_RejectsUnknownShape(t *testing.T) {
	_, err := providers.ParseModelIDs([]byte(`{"error":"nope"}`))
	assert.ErrorIs(t, err, providers.ErrUnknownModelListShape)
}

func TestModelListStatusError_DoesNotExposeUpstreamExplanation(t *testing.T) {
	err := providers.NewModelListStatusError(400)
	assert.EqualError(t, err, "model listing failed with status 400 (upstream_status)")
	assert.NotContains(t, err.Error(), "invalid token type")

	var statusErr *providers.ModelListHTTPStatusError
	require.ErrorAs(t, err, &statusErr)
	assert.Equal(t, 400, statusErr.Status)
	assert.Equal(t, providers.ModelListFailureUpstreamStatus, statusErr.Category)
}
