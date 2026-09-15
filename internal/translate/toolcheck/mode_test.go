package toolcheck

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const multiTool = `[{"name":"Multi","input_schema":{"type":"object","properties":{"files":{"type":"array","items":{"type":"string"}},"opts":{"type":"object"}},"required":["files"],"additionalProperties":false}}]`

func TestWithMode_NilValidator(t *testing.T) {
	var v *Validator
	assert.Nil(t, v.WithMode(ModeStandard))
	assert.Nil(t, v.WithMode(""))
	off := v.WithMode(ModeOff)
	require.NotNil(t, off)
	assert.Equal(t, ModeOff, off.Mode())
	assert.Equal(t, ModeStandard, v.Mode())
}

func TestWithMode_SharesSchemasAcrossCopies(t *testing.T) {
	v := Compile([]byte(multiTool))
	require.NotNil(t, v)
	sem := v.WithMode(ModeSemantic)
	assert.NotSame(t, v, sem)
	assert.Same(t, v, v.WithMode(ModeStandard))
	assert.Same(t, sem, sem.WithMode(ModeSemantic))
	assert.Equal(t, ModeStandard, v.Mode(), "original must be untouched")
	assert.Equal(t, ModeSemantic, sem.Mode())
}

func TestModeOff_SkipsNormalizeValidateRepair(t *testing.T) {
	v := Compile([]byte(multiTool)).WithMode(ModeOff)
	// Unknown key, scalar-for-array, and an empty optional all survive.
	got := v.Check("Multi", `{"files":"a.go","bogus":1,"opts":""}`)
	assert.True(t, got.OK)
	assert.Nil(t, got.Issue)
	assert.JSONEq(t, `{"files":"a.go","bogus":1,"opts":""}`, got.Args)
	// Unknown tools are not flagged either.
	got = v.Check("Nope", `{"x":1}`)
	assert.True(t, got.OK)
}

func TestModeOff_StillRepairsWireJSON(t *testing.T) {
	v := Compile([]byte(multiTool)).WithMode(ModeOff)
	got := v.Check("Multi", "```json\n{\"files\":[\"a\"]}\n```")
	require.NotNil(t, got.Issue)
	assert.Equal(t, BucketInvalidJSON, got.Issue.Bucket)
	assert.True(t, got.Issue.Repaired)
	assert.JSONEq(t, `{"files":["a"]}`, got.Args)
}

func TestStandard_DoesNotParseJSONString(t *testing.T) {
	v := Compile([]byte(multiTool))
	got := v.Check("Multi", `{"files":"[\"a.go\",\"b.go\"]"}`)
	require.NotNil(t, got.Issue)
	assert.True(t, got.Issue.Repaired)
	assert.Contains(t, got.Issue.Actions, "wrap_scalar_in_array")
	assert.JSONEq(t, `{"files":["[\"a.go\",\"b.go\"]"]}`, got.Args)
}

func TestSemantic_ParsesJSONStringArray(t *testing.T) {
	v := Compile([]byte(multiTool)).WithMode(ModeSemantic)
	got := v.Check("Multi", `{"files":"[\"a.go\",\"b.go\"]"}`)
	require.NotNil(t, got.Issue)
	assert.Equal(t, BucketSchemaMismatch, got.Issue.Bucket)
	assert.True(t, got.Issue.Repaired)
	assert.Equal(t, []string{"parse_json_string"}, got.Issue.Actions)
	assert.JSONEq(t, `{"files":["a.go","b.go"]}`, got.Args)
}

func TestSemantic_ParsesJSONStringObject(t *testing.T) {
	v := Compile([]byte(multiTool)).WithMode(ModeSemantic)
	got := v.Check("Multi", `{"files":["a"],"opts":"{\"k\":1}"}`)
	require.NotNil(t, got.Issue)
	assert.True(t, got.Issue.Repaired)
	assert.Contains(t, got.Issue.Actions, "parse_json_string")
	assert.JSONEq(t, `{"files":["a"],"opts":{"k":1}}`, got.Args)
}

func TestSemantic_NonJSONStringFallsBackToWrap(t *testing.T) {
	v := Compile([]byte(multiTool)).WithMode(ModeSemantic)
	got := v.Check("Multi", `{"files":"a.go"}`)
	require.NotNil(t, got.Issue)
	assert.Contains(t, got.Issue.Actions, "wrap_scalar_in_array")
	assert.JSONEq(t, `{"files":["a.go"]}`, got.Args)
}

func TestSemantic_JSONStringOfWrongShapeNotParsed(t *testing.T) {
	// "{...}" where an array is wanted: parsing would still fail validation,
	// so wrap wins and the object string becomes the single element.
	v := Compile([]byte(multiTool)).WithMode(ModeSemantic)
	got := v.Check("Multi", `{"files":"{\"k\":1}"}`)
	require.NotNil(t, got.Issue)
	assert.NotContains(t, got.Issue.Actions, "parse_json_string")
	assert.JSONEq(t, `{"files":["{\"k\":1}"]}`, got.Args)
}

func TestSemantic_StripsMarkdownAutolinkOnPathKeys(t *testing.T) {
	v := compileRead(t).WithMode(ModeSemantic)
	for _, raw := range []string{
		`{"file_path":"[notes.md](http://notes.md)"}`,
		`{"file_path":"[notes.md](notes.md)"}`,
		`{"file_path":"[notes.md](file://notes.md)"}`,
	} {
		got := v.Check("Read", raw)
		assert.False(t, got.OK, raw)
		require.NotNil(t, got.Issue, raw)
		assert.Equal(t, BucketSemanticRepair, got.Issue.Bucket)
		assert.True(t, got.Issue.Repaired)
		assert.Equal(t, []string{"strip_markdown_autolink"}, got.Issue.Actions)
		assert.JSONEq(t, `{"file_path":"notes.md"}`, got.Args, raw)
	}
}

func TestSemantic_LeavesRealLinksAndNonPathKeys(t *testing.T) {
	v := Compile([]byte(`[{"name":"T","input_schema":{"type":"object","properties":{"file_path":{"type":"string"},"content":{"type":"string"}}}}]`)).WithMode(ModeSemantic)
	for _, raw := range []string{
		`{"file_path":"[docs](https://example.com/guide)"}`,
		`{"content":"[notes.md](http://notes.md)"}`,
		`{"file_path":"see [a](a) and [b](b)"}`,
	} {
		got := v.Check("T", raw)
		assert.True(t, got.OK, raw)
		assert.JSONEq(t, raw, got.Args, raw)
	}
}

func TestStandard_LeavesMarkdownAutolink(t *testing.T) {
	v := compileRead(t)
	got := v.Check("Read", `{"file_path":"[notes.md](http://notes.md)"}`)
	assert.True(t, got.OK)
	assert.JSONEq(t, `{"file_path":"[notes.md](http://notes.md)"}`, got.Args)
}
