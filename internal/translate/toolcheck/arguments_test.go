package toolcheck

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestArgumentDocumentPreservesCleanInput(t *testing.T) {
	raw := "  { \"value\":1e+06, \"nested\": {\"items\":[1,2]} }\n"
	document := newArgumentDocument(raw)

	assert.Equal(t, raw, document.materialize())
	assert.Equal(t, `1e+06`, document.lookup([]string{"value"}).raw)
}

func TestArgumentDocumentNormalizationPreservesDuplicateDeletionOrder(t *testing.T) {
	raw := `{"x":"","x":"keep","x":""}`
	got, actions := normalizeArgs(raw, nil)

	assert.Equal(t, `{"x":""}`, got)
	assert.Equal(t, []string{"drop_empty_optional", "drop_empty_optional"}, actions)
}

func TestArgumentDocumentPreservesReadAndWriteDuplicateParentBehavior(t *testing.T) {
	raw := `{"a":{"y":1},"a":{"x":"2"}}`
	document := newArgumentDocument(raw)

	value := document.lookup([]string{"a", "x"})
	require.NotNil(t, value)
	assert.Equal(t, `"2"`, value.raw, "reads may continue through a later duplicate parent")

	require.True(t, document.replace([]string{"a", "x"}, `2`))
	assert.Equal(t, `{"a":{"y":1,"x":2},"a":{"x":"2"}}`, document.materialize(),
		"writes target the first matching parent")
}

func TestArgumentDocumentPreservesDuplicateLeafMutation(t *testing.T) {
	document := newArgumentDocument(`{"n":"1","n":"2"}`)

	require.True(t, document.replace([]string{"n"}, `1`))
	assert.Equal(t, `{"n":1,"n":"2"}`, document.materialize())
}

func TestArgumentDocumentArrayIndexesFollowEvolvingDocument(t *testing.T) {
	document := newArgumentDocument(`{"items":[0,1,2]}`)

	changed, ok := document.delete([]string{"items", "0"})
	require.True(t, changed)
	require.True(t, ok)
	changed, ok = document.delete([]string{"items", "1"})
	require.True(t, changed)
	require.True(t, ok)
	assert.Equal(t, `{"items":[1]}`, document.materialize())
}

func TestArgumentDocumentPreservesSpecialPathKeys(t *testing.T) {
	document := newArgumentDocument(`{"a.b":{"*":"value"},"0":"zero",":n":"colon","n":"plain"}`)

	assert.Equal(t, `"value"`, document.lookup([]string{"a.b", "*"}).raw)
	changed, ok := document.delete([]string{"a.b", "*"})
	require.True(t, changed)
	require.True(t, ok)
	changed, ok = document.delete([]string{":n"})
	require.True(t, changed)
	require.True(t, ok)
	assert.JSONEq(t, `{"a.b":{},"0":"zero",":n":"colon"}`, document.materialize())
}

func TestArgumentDocumentEmptyPathFailsLikeSJSON(t *testing.T) {
	raw := `{"":"value"}`
	document := newArgumentDocument(raw)

	changed, ok := document.delete([]string{""})
	assert.False(t, changed)
	assert.False(t, ok)
	assert.Equal(t, raw, document.materialize())
}

func TestCheckDuplicateIntegerKeysDoesNotUseLastValueAsRepair(t *testing.T) {
	v := Compile([]byte(`[{"name":"Numbers","input_schema":{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"],"additionalProperties":false}}]`))

	for _, args := range []string{`{"n":"1","n":"2"}`, `{"n":"2","n":"1"}`} {
		verdict := v.Check("Numbers", args)
		assert.False(t, verdict.OK)
		require.NotNil(t, verdict.Issue)
		assert.False(t, verdict.Issue.Repaired)
		assert.Equal(t, args, verdict.Args)
	}
}
