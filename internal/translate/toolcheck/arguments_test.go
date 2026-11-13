package toolcheck

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/sjson"
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

func TestArgumentDocumentWrapRetainsCurrentEdits(t *testing.T) {
	document := newArgumentDocument(`{"x":{"n":"1","extra":true}}`)
	require.True(t, document.replace([]string{"x", "n"}, `1`))
	changed, ok := document.delete([]string{"x", "extra"})
	require.True(t, changed)
	require.True(t, ok)
	require.True(t, document.wrap([]string{"x"}, document.lookup([]string{"x"})))
	assert.Equal(t, `{"x":[{"n":1}]}`, document.materialize())
	require.True(t, document.replace([]string{"x", "0", "n"}, `2`))
	assert.Equal(t, `{"x":[{"n":2}]}`, document.materialize())
}

func TestArgumentDocumentDuplicateWrapDoesNotAliasLaterParent(t *testing.T) {
	document := newArgumentDocument(`{"a":{"y":1},"a":{"x":{"n":"2","extra":true}}}`)
	value := document.lookup([]string{"a", "x"})
	require.NotNil(t, value)
	require.True(t, document.wrap([]string{"a", "x"}, value))
	require.True(t, document.replace([]string{"a", "x", "0", "n"}, `2`))
	changed, ok := document.delete([]string{"a", "x", "0", "extra"})
	require.True(t, changed)
	require.True(t, ok)
	assert.Equal(t, `{"a":{"y":1,"x":[{"n":2}]},"a":{"x":{"n":"2","extra":true}}}`, document.materialize())
	assert.Equal(t, `"2"`, document.lookup([]string{"a", "x", "n"}).raw,
		"the read still reaches the untouched later parent when the first is an array")
}

func TestArgumentDocumentDuplicateIndexAfterInsertDeleteAndReplace(t *testing.T) {
	document := newArgumentDocument(`{"x":1,"y":0,"x":2,"x":3}`)
	for _, want := range []string{`2`, `3`} {
		changed, ok := document.delete([]string{"x"})
		require.True(t, changed)
		require.True(t, ok)
		assert.Equal(t, want, document.lookup([]string{"x"}).raw)
	}
	changed, ok := document.delete([]string{"x"})
	require.True(t, changed)
	require.True(t, ok)
	assert.Nil(t, document.lookup([]string{"x"}))
	require.True(t, document.replace([]string{"x"}, `{"child":1}`))
	require.True(t, document.replace([]string{"x", "child"}, `2`))
	assert.Equal(t, `{"y":0,"x":{"child":2}}`, document.materialize())
}

func TestArgumentDocumentDeepEditsPreserveUntouchedSpans(t *testing.T) {
	for _, depth := range []int{3, 128, 2048} {
		t.Run(strconv.Itoa(depth), func(t *testing.T) {
			path := make([]string, depth+1)
			for i := range path {
				path[i] = "child"
			}
			path[depth] = "n"
			open := strings.Repeat(`{"child":`, depth)
			close := strings.Repeat(`}`, depth)
			sibling := ` [ 1e+06, {"escaped":"a","value":[1,2,3]} ]`
			document := newArgumentDocument(" \n" + open + `{"n":"2","keep":` + sibling + `}` + close + "\r\n")
			require.True(t, document.replace(path, `2`))
			assert.Equal(t, " \n"+open+`{"n":2,"keep":`+sibling+`}`+close+"\r\n", document.materialize())
			require.True(t, document.replace(path, `3`))
			assert.Equal(t, " \n"+open+`{"n":3,"keep":`+sibling+`}`+close+"\r\n", document.materialize())
		})
	}
}

func TestArgumentDocumentArrayDeletionPreservesWhitespace(t *testing.T) {
	document := newArgumentDocument(`{"a":[ 1 , 2 , 3 ]}`)
	for _, want := range []string{`{"a":[  2 , 3 ]}`, `{"a":[   3 ]}`, `{"a":[    ]}`} {
		changed, ok := document.delete([]string{"a", "0"})
		require.True(t, changed)
		require.True(t, ok)
		assert.Equal(t, want, document.materialize())
	}
}

func TestArgumentDocumentDeletionPreservesCommaAdjacentWhitespace(t *testing.T) {
	document := newArgumentDocument(`{ "x":1 , "y":2 , "z":3 }`)
	changed, ok := document.delete([]string{"y"})
	require.True(t, changed)
	require.True(t, ok)
	assert.Equal(t, `{ "x":1  , "z":3 }`, document.materialize())
	changed, ok = document.delete([]string{"z"})
	require.True(t, changed)
	require.True(t, ok)
	assert.Equal(t, `{ "x":1   }`, document.materialize())
	changed, ok = document.delete([]string{"x"})
	require.True(t, changed)
	require.True(t, ok)
	assert.Equal(t, `{   }`, document.materialize())
}

func TestArgumentDocumentLargeNumericObjectKey(t *testing.T) {
	document := newArgumentDocument(`{"100000000000":"1"}`)
	require.True(t, document.replace([]string{"100000000000"}, `1`))
	assert.Equal(t, `{"100000000000":1}`, document.materialize())
}

func TestArgumentDocumentPreservesOverflowedArrayIndex(t *testing.T) {
	raw := `{"a":[{"n":"1"}],"a":{"18446744073709551616":{"n":"2"}}}`
	path := []string{"a", "18446744073709551616", "n"}
	document := newArgumentDocument(raw)
	assert.Equal(t, `"1"`, document.lookup(path).raw)
	require.True(t, document.replace(path, `1`))
	assert.Equal(t, `{"a":[{"n":1}],"a":{"18446744073709551616":{"n":"2"}}}`, document.materialize())
	assert.Nil(t, document.lookup([]string{"a", "18446744073709551615"}))
}

func TestCheckPreservesEmptyParentPathJoining(t *testing.T) {
	validator := Compile([]byte(`[{"name":"Empty","input_schema":{"type":"object","properties":{"":{"type":"object","additionalProperties":false},"extra":{"type":"integer"}},"required":[""]}}]`))
	args := `{"":{"extra":1},"extra":2}`
	verdict := validator.Check("Empty", args)
	require.NotNil(t, verdict.Issue)
	assert.False(t, verdict.Issue.Repaired)
	assert.Equal(t, args, verdict.Args)
	assert.Empty(t, verdict.Issue.Actions)
}

func TestNormalizeLargeFirstMemberMatchesSJSON(t *testing.T) {
	large := `"` + strings.Repeat("x", 8192) + `"`
	for _, fixture := range []struct{ args, key string }{
		{`{"optional":"","large":` + large + `}`, "optional"},
		{" \n{ \"optional\" : null , \"large\" : " + large + " }\r\n", "optional"},
		{`{"x":"","x":` + large + `}`, "x"},
		{`{"a.b":null,"large":` + large + `}`, "a.b"},
		{`{"\u00e9":"","large":` + large + `}`, "é"},
		{`{"optional":null` + strings.Repeat(" ", 8192) + `}`, "optional"},
	} {
		want, err := sjson.Delete(fixture.args, argumentOraclePath([]string{fixture.key}))
		require.NoError(t, err)
		got, actions := normalizeArgs(fixture.args, nil)
		assert.Equal(t, want, got)
		assert.Len(t, actions, 1)
	}
}

func TestArgumentDocumentExpandedSnapshotDoesNotAliasNestedChildren(t *testing.T) {
	document := newArgumentDocument(`{"a":{"x":{"n":"1"}},"a":{"y":{"inner":{"n":"2"}}}}`)
	require.True(t, document.replace([]string{"a", "x", "n"}, `1`))
	require.NotNil(t, document.lookup([]string{"a", "y", "inner", "n"}))
	require.True(t, document.wrap([]string{"a", "y"}, document.lookup([]string{"a", "y"})))
	require.True(t, document.replace([]string{"a", "y", "0", "inner", "n"}, `3`))
	assert.Equal(t, `{"a":{"x":{"n":1},"y":[{"inner":{"n":3}}]},"a":{"y":{"inner":{"n":"2"}}}}`, document.materialize())
}
