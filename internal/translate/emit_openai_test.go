package translate

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSanitizeOpenAIToolSchema_ConflictingPatternsStayUnifierSafe covers the
// 2026-08-21 Fireworks "Conflict in schema definitions for key 'description'"
// lockout: two tools declaring the same property with different patterns must
// not, after sanitization, produce conflicting rendered definitions. Identical
// patterns must stay byte-identical (no false conflict); different patterns must
// differ; and the raw pattern must not leak verbatim into the description.
func TestSanitizeOpenAIToolSchema_ConflictingPatternsStayUnifierSafe(t *testing.T) {
	prop := func(pattern string) map[string]any {
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{"description": map[string]any{"type": "string", "description": "the description", "pattern": pattern}},
		}
	}
	a := prop(`^[^\n\r]*$`)
	b := prop(`^[\s\S]{0,300}$`)
	a2 := prop(`^[^\n\r]*$`)

	sanitizeOpenAIToolSchema(a)
	sanitizeOpenAIToolSchema(b)
	sanitizeOpenAIToolSchema(a2)

	descOf := func(m map[string]any) string {
		return m["properties"].(map[string]any)["description"].(map[string]any)["description"].(string)
	}
	da, db, da2 := descOf(a), descOf(b), descOf(a2)

	for _, d := range []string{da, db} {
		assert.NotContains(t, d, "pattern:", "pattern value must be hashed, not embedded verbatim")
		assert.NotContains(t, d, "[\\s\\S]", "raw regex must not leak into description")
		assert.Contains(t, d, "pattern constraint", "note keeps the constraint name")
	}
	assert.Equal(t, da, da2, "identical patterns must render identically — else a false 'conflict' 400")
	assert.NotEqual(t, da, db, "different patterns must render differently — else a real divergence is hidden")
}
