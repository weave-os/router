package toolcheck

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheck_NestedOptionalNulls(t *testing.T) {
	const schema = `{"type":"object","properties":{
		"entries":{"type":"array","items":{"type":"object","properties":{
			"summary":{"type":"string"},"outcome":{"type":"string","enum":["accepted","skipped"]},
			"details":{"type":"object","properties":{"count":{"type":"integer"},"note":{"type":"string"}}},
			"nullable":{"type":["string","null"]},"enabled":{"type":"boolean"},"tags":{"type":"array"}
		},"required":["summary"]}},"nullable":{"enum":[null,"clear"]}
	},"required":["entries"]}`
	v := Compile([]byte(`[{"name":"RecordEntries","input_schema":` + schema + `}]`))
	const args = ` {"entries":[{"summary":"one","outcome":null,"details":{"count":null,"note":""},"nullable":null,"enabled":false,"tags":[]},{"summary":"two","outcome":"accepted","details":{"count":0}}],"nullable":null} `
	const want = ` {"entries":[{"summary":"one","details":{"note":""},"nullable":null,"enabled":false,"tags":[]},{"summary":"two","outcome":"accepted","details":{"count":0}}],"nullable":null} `
	got := v.Check("RecordEntries", args)
	assert.True(t, got.OK, "%+v", got.Issue)
	assert.Nil(t, got.Issue)
	assert.Equal(t, want, got.Args)
	assert.Equal(t, got, v.Check("RecordEntries", got.Args), "normalization must be idempotent")
}

func TestCheck_OptionalNullSchemaSemantics(t *testing.T) {
	for _, tc := range []struct {
		name, schema, args, want string
		valid                    bool
	}{
		{"required null", `{"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}`, `{"value":null}`, `{"value":null}`, false},
		{"nullable anyOf", `{"type":"object","properties":{"value":{"anyOf":[{"type":"string"},{"type":"null"}]}}}`, `{"value":null}`, `{"value":null}`, true},
		{"unconstrained", `{"type":"object","properties":{"value":{}}}`, `{"value":null,"unknown":null}`, `{"value":null,"unknown":null}`, true},
		{"array null", `{"type":"object","properties":{"values":{"type":"array","items":{"type":["object","null"],"properties":{"value":{"type":"integer"}}}}}}`, `{"values":[null,{"value":null}]}`, `{"values":[null,{}]}`, true},
		{"invalid array null", `{"type":"object","properties":{"values":{"type":"array","items":{"type":"string"}}}}`, `{"values":[null]}`, `{"values":[null]}`, false},
		{"local ref", `{"type":"object","properties":{"entry":{"$ref":"#/definitions/entry"}},"definitions":{"entry":{"type":"object","properties":{"value":{"type":"integer"}}}}}`, `{"entry":{"value":null}}`, `{"entry":{}}`, true},
		{"recursive ref", `{"type":"object","properties":{"value":{"type":"integer"},"next":{"$ref":"#"}}}`, `{"value":null,"next":{"value":null}}`, `{"next":{}}`, true},
		{"referenced nullable", `{"type":"object","properties":{"value":{"$ref":"#/definitions/value"}},"definitions":{"value":{"type":["integer","null"]}}}`, `{"value":null}`, `{"value":null}`, true},
		{"modern ref", `{"$schema":"https://json-schema.org/draft/2020-12/schema","$ref":"#/$defs/entry","$defs":{"entry":{"type":"object","properties":{"value":{"type":"integer"}}}}}`, `{"value":null}`, `{}`, true},
		{"modern ref required sibling", `{"$schema":"https://json-schema.org/draft/2020-12/schema","$ref":"#/$defs/entry","required":["value"],"$defs":{"entry":{"type":"object","properties":{"value":{"type":"integer"}}}}}`, `{"value":null}`, `{"value":null}`, false},
		{"dynamic nullable scope", `{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"urn:root","$dynamicAnchor":"node","type":["object","null"],"properties":{"entry":{"$ref":"#/$defs/entry"}},"$defs":{"entry":{"$id":"urn:entry","$dynamicAnchor":"node","type":"object","properties":{"value":{"$dynamicRef":"#node"}}}}}`, `{"entry":{"value":null}}`, `{"entry":{"value":null}}`, true},
		{"discriminated anyOf", `{"type":"object","properties":{"entry":{"anyOf":[{"type":"object","properties":{"kind":{"const":"a"},"value":{"type":"integer"}},"required":["kind"]},{"type":"object","properties":{"kind":{"const":"b"},"value":{"type":["integer","null"]}},"required":["kind","value"]}]}}}`, `{"entry":{"kind":"a","value":null}}`, `{"entry":{"kind":"a"}}`, true},
		{"valid anyOf branch", `{"type":"object","properties":{"entry":{"anyOf":[{"type":"object","properties":{"value":{"type":"integer"}}},{"type":"object","properties":{"value":{"type":"null"}},"required":["value"]}]}}}`, `{"entry":{"value":null}}`, `{"entry":{"value":null}}`, true},
		{"ambiguous anyOf", `{"type":"object","properties":{"entry":{"anyOf":[{"type":"object","properties":{"a":{"type":"integer"},"b":{"type":"null"}}},{"type":"object","properties":{"a":{"type":"null"},"b":{"type":"integer"}}}]}}}`, `{"entry":{"a":null,"b":null}}`, `{"entry":{"a":null,"b":null}}`, false},
		{"allOf required", `{"type":"object","properties":{"value":{"type":"integer"}},"allOf":[{"required":["value"]}]}`, `{"value":null}`, `{"value":null}`, false},
		{"conditional required", `{"type":"object","properties":{"value":{"type":"integer"}},"if":{"required":["value"]},"then":{"required":["value"]}}`, `{"value":null}`, `{"value":null}`, false},
		{"minProperties", `{"type":"object","properties":{"value":{"type":"integer"}},"minProperties":1}`, `{"value":null}`, `{"value":null}`, false},
		{"duplicate keys", `{"type":"object","properties":{"entry":{"type":"object","properties":{"value":{"type":"integer"}}}}}`, `{"entry":{"value":null},"entry":{"value":1}}`, `{"entry":{"value":null},"entry":{"value":1}}`, true},
		{"root ref required empty", `{"$ref":"#/definitions/entry","definitions":{"entry":{"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}}}`, `{"value":""}`, `{"value":""}`, true},
		{"allOf required empty", `{"type":"object","properties":{"value":{"type":"string"}},"allOf":[{"required":["value"]}]}`, `{"value":""}`, `{"value":""}`, true},
		{"broken schema", `{"type":"object","properties":{"value":{"$ref":"#/missing"}}}`, `{"value":null}`, `{"value":null}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := Compile([]byte(`[{"name":"Tool","input_schema":` + tc.schema + `}]`))
			require.NotNil(t, v)
			if tc.name != "broken schema" {
				require.NotNil(t, v.tools["Tool"].compiled)
			}
			got := v.Check("Tool", tc.args)
			assert.Equal(t, tc.want, got.Args)
			assert.Equal(t, tc.valid, got.OK, "%+v", got.Issue)
		})
	}
}

func TestCheck_NullNormalizationAndSafeRepair(t *testing.T) {
	v := Compile([]byte(`[{"name":"Tool","input_schema":{"type":"object","properties":{"entry":{"type":"object","properties":{"value":{"type":"integer"},"count":{"type":"integer"}},"required":["count"]}}}}]`))
	got := v.Check("Tool", `{"entry":{"value":null,"count":"2"}}`)
	assert.Equal(t, `{"entry":{"count":2}}`, got.Args)
	require.NotNil(t, got.Issue)
	assert.True(t, got.Issue.Repaired)
	assert.Equal(t, []string{"drop_null_optional", "coerce_string_to_number"}, got.Issue.Actions)
}

func TestCheck_NullNormalizationSharedValidator(t *testing.T) {
	v := CompileCached([]byte(`[{"name":"Tool","input_schema":{"type":"object","properties":{"entry":{"type":"object","properties":{"value":{"type":"integer"}}}}}}]`))
	for i := range 16 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, Verdict{OK: true, Args: `{"entry":{}}`}, v.Check("Tool", `{"entry":{"value":null}}`))
		})
	}
}

func TestNormalizeNullOptionals_BoundsWork(t *testing.T) {
	v := Compile([]byte(`[{"name":"Tool","input_schema":{"type":"object","properties":{"value":{"type":"integer"},"entries":{"type":"array","items":{"$ref":"#"}},"padding":{"type":"string"}}}}]`))
	for _, args := range []string{
		`{"value":null,"padding":"` + strings.Repeat("x", maxArgsBytes) + `"}`,
		`{"value":null,"entries":[` + strings.Repeat(`{"value":null},`, maxNormalizationNodes) + `{}` + `]}`,
	} {
		got, actions := normalizeNullOptionals(args, v.tools["Tool"].compiled)
		assert.Equal(t, args, got, "exhausting the work budget must discard partial normalization")
		assert.Empty(t, actions)
	}
}

func BenchmarkCheck_NestedOptionalNulls(b *testing.B) {
	v := Compile([]byte(`[{"name":"Tool","input_schema":{"type":"object","properties":{"entries":{"type":"array","items":{"type":"object","properties":{"summary":{"type":"string"},"outcome":{"type":"string","enum":["accepted","skipped"]}},"required":["summary"]}}}}]`))
	for _, count := range []int{1, 100, 1000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			args := `{"entries":[` + strings.Repeat(`{"summary":"entry","outcome":null},`, count-1) + `{"summary":"entry","outcome":null}]}`
			if got := v.Check("Tool", args); !got.OK || strings.Contains(got.Args, "outcome") {
				b.Fatalf("nested optional argument was not omitted: %+v", got)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				v.Check("Tool", args)
			}
		})
	}
}

func TestCheck_OptionalNullsPreserveWireValuesAndLiteralKeys(t *testing.T) {
	const schema = `{"type":"object","properties":{"entry":{"type":"object","properties":{"":{"type":"integer"},":x":{"type":"integer"},"a.b":{"type":"integer"},"*":{"type":"integer"},"id":{"type":"integer"}}}}}`
	v := Compile([]byte(`[{"name":"Tool","input_schema":` + schema + `}]`))
	const args = ` {"entry":{"":null,":x":null,"a.b":null,"*":null,"id":9007199254740993,"exponent":1e+06,"text":"\u0061"}} `
	got := v.Check("Tool", args)
	assert.True(t, got.OK, "%+v", got.Issue)
	assert.Equal(t, ` {"entry":{"id":9007199254740993,"exponent":1e+06,"text":"\u0061"}} `, got.Args)
}
