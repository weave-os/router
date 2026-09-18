package toolcheck

import (
	"strconv"
	"strings"
	"testing"
)

var toolcheckEditsBenchmarkSink Verdict
var toolcheckNormalizationBenchmarkSink string

func BenchmarkToolcheckNestedNormalization(b *testing.B) {
	for _, depth := range []int{128, 512, 2048, 8192} {
		b.Run(strconv.Itoa(depth), func(b *testing.B) {
			nested := strings.Repeat(`{"child":`, depth) + `1e+06` + strings.Repeat(`}`, depth)
			args := `{"optional":"","nested":` + nested + `}`
			want := `{"nested":` + nested + `}`
			if normalized, _ := normalizeArgs(args, nil); normalized != want {
				b.Fatal("fixture did not normalize the top-level optional field")
			}
			b.SetBytes(int64(len(args)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				toolcheckNormalizationBenchmarkSink, _ = normalizeArgs(args, nil)
			}
		})
	}
}

func BenchmarkToolcheckEdits(b *testing.B) {
	for _, fieldCount := range []int{0, 1, 8, 128, 256, 512, 1024, 2048, 4096, 8192, 16384} {
		b.Run("normalize/"+strconv.Itoa(fieldCount), func(b *testing.B) {
			validator := benchmarkValidator(fieldCount, benchmarkNormalize)
			args := benchmarkArgs(fieldCount, benchmarkNormalize)
			benchmarkToolcheck(b, validator, args, func(verdict Verdict) bool {
				return verdict.OK && verdict.Args == `{"known":"ok"}`
			})
		})
		if fieldCount == 0 {
			continue
		}
		b.Run("unknown-keys/"+strconv.Itoa(fieldCount), func(b *testing.B) {
			validator := benchmarkValidator(fieldCount, benchmarkUnknownKeys)
			args := benchmarkArgs(fieldCount, benchmarkUnknownKeys)
			benchmarkToolcheck(b, validator, args, func(verdict Verdict) bool {
				return verdict.Issue != nil && verdict.Issue.Repaired
			})
		})
		b.Run("coercions/"+strconv.Itoa(fieldCount), func(b *testing.B) {
			validator := benchmarkValidator(fieldCount, benchmarkCoercions)
			args := benchmarkArgs(fieldCount, benchmarkCoercions)
			benchmarkToolcheck(b, validator, args, func(verdict Verdict) bool {
				return verdict.Issue != nil && verdict.Issue.Repaired
			})
		})
	}
}

type benchmarkToolKind uint8

const (
	benchmarkNormalize benchmarkToolKind = iota
	benchmarkUnknownKeys
	benchmarkCoercions
)

func benchmarkToolcheck(b *testing.B, validator *Validator, args string, valid func(Verdict) bool) {
	b.Helper()
	if verdict := validator.Check("Bench", args); !valid(verdict) {
		b.Fatalf("benchmark fixture produced unexpected verdict: %+v", verdict)
	}
	b.SetBytes(int64(len(args)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		toolcheckEditsBenchmarkSink = validator.Check("Bench", args)
	}
}

func benchmarkValidator(_ int, kind benchmarkToolKind) *Validator {
	schema := `{"type":"object","properties":{"known":{"type":"string"}},"required":["known"],"additionalProperties":false}`
	switch kind {
	case benchmarkNormalize:
		schema = `{"type":"object","properties":{"known":{"type":"string"}},"patternProperties":{"^optional_":{"type":"string"}},"required":["known"],"additionalProperties":false}`
	case benchmarkCoercions:
		schema = `{"type":"object","properties":{"known":{"type":"string"}},"patternProperties":{"^number_":{"type":"integer"}},"required":["known"],"additionalProperties":false}`
	}
	return Compile([]byte(`[{"name":"Bench","input_schema":` + schema + `}]`))
}

func benchmarkArgs(fieldCount int, kind benchmarkToolKind) string {
	var args strings.Builder
	args.WriteString(`{"known":"ok"`)
	for i := 0; i < fieldCount; i++ {
		args.WriteString(`,"`)
		args.WriteString(benchmarkFieldName(kind, i))
		args.WriteString(`":`)
		switch kind {
		case benchmarkNormalize:
			args.WriteString(`""`)
		case benchmarkUnknownKeys:
			args.WriteString(`true`)
		case benchmarkCoercions:
			args.WriteString(`"`)
			args.WriteString(strconv.Itoa(i))
			args.WriteString(`"`)
		}
	}
	args.WriteByte('}')
	return args.String()
}

func benchmarkFieldName(kind benchmarkToolKind, index int) string {
	switch kind {
	case benchmarkNormalize:
		return "optional_" + strconv.Itoa(index)
	case benchmarkUnknownKeys:
		return "extra_" + strconv.Itoa(index)
	default:
		return "number_" + strconv.Itoa(index)
	}
}

func BenchmarkToolcheckCleanNormalization(b *testing.B) {
	for _, size := range []int{0, 1, 8, 128, 1024, 16384} {
		args := benchmarkArgs(size, benchmarkCoercions)
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(args)))
			for i := 0; i < b.N; i++ {
				toolcheckNormalizationBenchmarkSink, _ = normalizeArgs(args, nil)
			}
		})
	}
}

func BenchmarkToolcheckDuplicateNormalization(b *testing.B) {
	for _, size := range []int{128, 512, 2048, 8192, 16384} {
		args := `{"known":"ok"` + strings.Repeat(`,"x":null`, size) + `}`
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			if got, actions := normalizeArgs(args, nil); got != `{"known":"ok"}` || len(actions) != size {
				b.Fatal("fixture did not delete each duplicate")
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				toolcheckNormalizationBenchmarkSink, _ = normalizeArgs(args, nil)
			}
		})
	}
}

func BenchmarkToolcheckUnionAndDuplicateRepairs(b *testing.B) {
	for _, fixture := range []struct{ name, schema, args, want string }{
		{name: "nested-union", schema: nestedBenchmarkSchema(3), args: `{"x":{"n":{"n":{"n":"1","extra":true},"extra":true},"extra":true}}`, want: `{"x":[{"n":[{"n":[{"n":1}]}]}]}`},
		{name: "duplicate-parent", schema: `{"type":"object","properties":{"a":{"type":"object","properties":{"x":{"type":"integer"}}}}}`, args: `{"a":{"y":1},"a":{"x":"2"}}`, want: `{"a":{"y":1},"a":{"x":"2"}}`},
	} {
		b.Run(fixture.name, func(b *testing.B) {
			validator := Compile([]byte(`[{"name":"Bench","input_schema":` + fixture.schema + `}]`))
			benchmarkToolcheck(b, validator, fixture.args, func(v Verdict) bool { return v.Args == fixture.want && v.Issue != nil })
		})
	}
}

func nestedBenchmarkSchema(depth int) string {
	schema := `{"type":"integer"}`
	for i := 0; i < depth; i++ {
		object := `{"type":"object","properties":{"n":` + schema + `},"required":["n"],"additionalProperties":false}`
		schema = `{"anyOf":[` + object + `,{"type":"array","items":` + object + `}]}`
	}
	return `{"type":"object","properties":{"x":` + schema + `},"required":["x"]}`
}

func BenchmarkToolcheckDeepSparseRepairs(b *testing.B) {
	for _, depth := range []int{1, 8, 128} {
		for _, siblingSize := range []int{0, 65536} {
			schema := `{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"]}`
			args := `{"n":"2","sibling":[` + strings.Repeat(`1,`, siblingSize) + `0]}`
			for i := 0; i < depth; i++ {
				schema = `{"type":"object","properties":{"child":` + schema + `},"required":["child"]}`
				args = `{"child":` + args + `}`
			}
			want := strings.Replace(args, `"n":"2"`, `"n":2`, 1)
			validator := Compile([]byte(`[{"name":"Bench","input_schema":` + schema + `}]`))
			b.Run(strconv.Itoa(depth)+"/sibling-"+strconv.Itoa(siblingSize), func(b *testing.B) {
				benchmarkToolcheck(b, validator, args, func(verdict Verdict) bool {
					return verdict.Issue != nil && verdict.Issue.Repaired && verdict.Args == want
				})
			})
		}
	}
}
