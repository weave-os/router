package translate

import (
	"strings"
	"testing"
)

func BenchmarkCompletedEmptyTurnDisqualified(b *testing.B) {
	data := []byte(`{"type":"response.output_text.delta","delta":"` + strings.Repeat("x", 32<<10) + `"}`)
	var state completedEmptyTurn
	state.observe(data)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		state.observe(data)
	}
}
