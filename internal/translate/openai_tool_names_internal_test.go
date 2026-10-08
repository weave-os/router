package translate

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeResponsesToolAliasReservesGeneratedAliasShape(t *testing.T) {
	body := []byte(`{"tools":[{"function":{"name":"a.b"}},{"function":{"name":"a_b_2e7336dc8e"}}]}`)

	aliases := openAIToolNameAliases(body, openAIRequestToolNamePaths)

	assert.Equal(t, map[string]string{"a_b_2e7336dc8e": "a.b", "a_b_2e7336dc8e_8e74278dba": "a_b_2e7336dc8e"}, aliases)
}
