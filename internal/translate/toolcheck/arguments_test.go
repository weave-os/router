package toolcheck

import (
	"strings"
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

func TestCheckArgumentWhitespace(t *testing.T) {
	validator := compileRead(t)
	for _, whitespace := range []struct {
		name   string
		prefix string
		suffix string
	}{
		{name: "none"},
		{name: "leading", prefix: " \t"},
		{name: "trailing newline", suffix: "\n"},
		{name: "long trailing whitespace", suffix: strings.Repeat(" \t\r\n", 20)},
		{name: "both", prefix: "\r\n", suffix: " \t\n"},
	} {
		for _, fixture := range []struct {
			name     string
			args     string
			want     string
			repaired bool
		}{
			{name: "clean", args: `{"file_path":"/a.go"}`, want: `{"file_path":"/a.go"}`},
			{name: "normalize", args: `{"file_path":"/a.go","pages":""}`, want: `{"file_path":"/a.go"}`},
			{name: "repair", args: `{"file_path":"/a.go","limit":"2"}`, want: `{"file_path":"/a.go","limit":2}`, repaired: true},
		} {
			t.Run(whitespace.name+"/"+fixture.name, func(t *testing.T) {
				var verdict Verdict
				require.NotPanics(t, func() {
					verdict = validator.Check("Read", whitespace.prefix+fixture.args+whitespace.suffix)
				})
				assert.Equal(t, whitespace.prefix+fixture.want+whitespace.suffix, verdict.Args)
				if fixture.repaired {
					require.NotNil(t, verdict.Issue)
					assert.True(t, verdict.Issue.Repaired)
				} else {
					assert.True(t, verdict.OK)
					assert.Nil(t, verdict.Issue)
				}
			})
		}
	}
}

func TestCheckPreservesDeepUntouchedArguments(t *testing.T) {
	validator := Compile([]byte(`[{"name":"Nested","input_schema":{"type":"object"}}]`))
	for _, container := range []struct {
		name  string
		open  string
		close string
	}{
		{name: "objects", open: `{"child":`, close: `}`},
		{name: "arrays", open: `[`, close: `]`},
	} {
		t.Run(container.name, func(t *testing.T) {
			nested := strings.Repeat(container.open, 20000) + `1e+06` + strings.Repeat(container.close, 20000)
			want := `{"nested":` + nested + `}`
			for _, args := range []string{want, `{"optional":"","nested":` + nested + `}`} {
				verdict := validator.Check("Nested", args)
				assert.True(t, verdict.OK)
				assert.Nil(t, verdict.Issue)
				assert.Equal(t, want, verdict.Args)
			}
		})
	}
}

func TestCheckRepairsWrappedObjectAndArrayElements(t *testing.T) {
	validator := Compile([]byte(`[{"name":"Nested","input_schema":{"type":"object","properties":{"items":{"type":"array","items":{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"],"additionalProperties":false}}},"required":["items"]}}]`))
	for _, args := range []string{
		`{"items":{"n":"1","extra":true}}`,
		`{"items":[{"n":"1","extra":true}]}`,
	} {
		verdict := validator.Check("Nested", args)
		require.NotNil(t, verdict.Issue)
		assert.True(t, verdict.Issue.Repaired)
		assert.Equal(t, `{"items":[{"n":1}]}`, verdict.Args)
	}
}

func TestArgumentDocumentEscapesInsertedMemberName(t *testing.T) {
	document := newArgumentDocument(`{"parent":{}}`)
	require.True(t, document.replace([]string{"parent", "a\"\\\n\x00<"}, `1`))
	assert.JSONEq(t, `{"parent":{"a\"\\\n\u0000<":1}}`, document.materialize())
}
