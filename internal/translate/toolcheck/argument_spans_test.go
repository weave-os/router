package toolcheck

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestArgumentSpansMatchContainerBoundaries(t *testing.T) {
	raw := `{"escaped":"}\\\"[","dup":[1e+06,{"x":true}],"dup":{},"tail":null}`
	source := &argumentSource{raw: raw}
	require.NoError(t, source.indexContainers())
	var containers []string
	for start, end := range source.spans {
		containers = append(containers, raw[start:end])
	}
	assert.ElementsMatch(t, []string{raw, `[1e+06,{"x":true}]`, `{"x":true}`, `{}`}, containers)
}

func TestArgumentSpansRejectMalformedJSON(t *testing.T) {
	for _, raw := range []string{`{"x":[`, `{"x":{]}`, `{"x": [1,]}`} {
		source := &argumentSource{raw: raw}
		err := source.indexContainers()
		require.Error(t, err)
		assert.Nil(t, source.spans)
		assert.Equal(t, err, source.indexContainers())
	}
}

func TestArgumentSpansDoNotIntroduceDepthLimit(t *testing.T) {
	raw := `{"x":` + strings.Repeat(`[`, 20000) + `0` + strings.Repeat(`]`, 20000) + `}`
	document := newArgumentDocument(raw)
	assert.True(t, document.replace([]string{"x", "0"}, `1`))
	assert.Equal(t, `{"x":[1]}`, document.materialize())
}

func TestArgumentSpansOnlyBuildOnNestedTraversal(t *testing.T) {
	raw := `{"optional":"","x":{"n":"1","large":[1,2,3]},"other": [4,5]}`
	document := newArgumentDocument(raw)
	changed, ok := document.delete([]string{"optional"})
	require.True(t, changed)
	require.True(t, ok)
	assert.Equal(t, `{"x":{"n":"1","large":[1,2,3]},"other": [4,5]}`, document.materialize())
	assert.Nil(t, document.root.source.spans, "root-only edits must not index nested containers")
	require.True(t, document.replace([]string{"x", "n"}, `1`))
	assert.Equal(t, `{"x":{"n":1,"large":[1,2,3]},"other": [4,5]}`, document.materialize())
	assert.Nil(t, document.root.source.spans, "a single sparse descent must not index the whole source")
	assert.False(t, document.lookup([]string{"other"}).expanded, "unvisited sibling remains raw")
}

func TestArgumentSpansAmortizeRepeatedDeepTraversal(t *testing.T) {
	const depth = 512
	raw := strings.Repeat(`{"child":`, depth) + `{"n":"2"}` + strings.Repeat(`}`, depth)
	document := newArgumentDocument(raw)
	path := make([]string, depth+1)
	for i := range path {
		path[i] = "child"
	}
	path[depth] = "n"
	require.True(t, document.replace(path, `2`))
	assert.Equal(t, strings.Replace(raw, `"2"`, `2`, 1), document.materialize())
	assert.Len(t, document.root.source.spans, depth+1)
}
