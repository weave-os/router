package toolcheck

import (
	"strconv"
	"strings"
	"testing"
)

func BenchmarkArgumentDocumentEdits(b *testing.B) {
	for _, size := range []int{1, 8, 128, 512, 2048, 4096, 8192, 16384} {
		args := benchmarkArgs(size, benchmarkCoercions)
		paths := make([][]string, size)
		for i := range paths {
			paths[i] = []string{benchmarkFieldName(benchmarkCoercions, i)}
		}
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			document := newArgumentDocument(args)
			var want strings.Builder
			want.WriteString(`{"known":"ok"`)
			for _, path := range paths {
				if !document.replace(path, `1`) {
					b.Fatal("fixture path could not be replaced")
				}
				want.WriteString(`,"` + path[0] + `":1`)
			}
			want.WriteByte('}')
			if document.materialize() != want.String() {
				b.Fatal("fixture did not replace each field")
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(args)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				document := newArgumentDocument(args)
				for _, path := range paths {
					document.replace(path, `1`)
				}
				toolcheckNormalizationBenchmarkSink = document.materialize()
			}
		})
	}
}

func BenchmarkArgumentDocumentDeepEdits(b *testing.B) {
	for _, depth := range []int{1, 8, 128, 512, 2048} {
		for _, siblingSize := range []int{0, 65536} {
			open, close := strings.Repeat(`{"child":`, depth), strings.Repeat(`}`, depth)
			sibling := `,"sibling":[` + strings.Repeat(`1,`, siblingSize) + `0]`
			args := open + `{"n":"2"` + sibling + `}` + close
			want := open + `{"n":2` + sibling + `}` + close
			path := make([]string, depth+1)
			for i := range path {
				path[i] = "child"
			}
			path[depth] = "n"
			b.Run(strconv.Itoa(depth)+"/sibling-"+strconv.Itoa(siblingSize), func(b *testing.B) {
				document := newArgumentDocument(args)
				if !document.replace(path, `2`) || document.materialize() != want {
					b.Fatal("fixture did not repair deep path")
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					document := newArgumentDocument(args)
					document.replace(path, `2`)
					toolcheckNormalizationBenchmarkSink = document.materialize()
				}
			})
		}
	}
}

func BenchmarkArgumentSpanIndex(b *testing.B) {
	for _, depth := range []int{128, 512, 2048} {
		raw := strings.Repeat(`{"child":`, depth) + `1e+06` + strings.Repeat(`}`, depth)
		b.Run(strconv.Itoa(depth), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				source := &argumentSource{raw: raw}
				if err := source.indexContainers(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
