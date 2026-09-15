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
	for _, fieldCount := range []int{128, 256, 512, 1024} {
		b.Run("normalize/"+strconv.Itoa(fieldCount), func(b *testing.B) {
			validator := benchmarkValidator(fieldCount, benchmarkNormalize)
			args := benchmarkArgs(fieldCount, benchmarkNormalize)
			benchmarkToolcheck(b, validator, args, func(verdict Verdict) bool {
				return verdict.OK && verdict.Args == `{"known":"ok"}`
			})
		})
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

func benchmarkValidator(fieldCount int, kind benchmarkToolKind) *Validator {
	var schema strings.Builder
	schema.WriteString(`{"type":"object","properties":{"known":{"type":"string"}`)
	if kind != benchmarkUnknownKeys {
		for i := 0; i < fieldCount; i++ {
			schema.WriteString(`,"`)
			schema.WriteString(benchmarkFieldName(kind, i))
			schema.WriteString(`":{"type":`)
			if kind == benchmarkCoercions {
				schema.WriteString(`"integer"`)
			} else {
				schema.WriteString(`"string"`)
			}
			schema.WriteByte('}')
		}
	}
	schema.WriteString(`},"required":["known"],"additionalProperties":false}`)
	return Compile([]byte(`[{"name":"Bench","input_schema":` + schema.String() + `}]`))
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
