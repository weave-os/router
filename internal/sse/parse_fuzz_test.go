package sse_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Seed bodies cover both delimiters, the mixed candidates the grammar
// rejects, zero-length events, and unterminated tails; the fuzzer mutates
// from there.
var splitSeedBodies = []string{
	"",
	"\n",
	"\r\n",
	"\n\n",
	"\r\n\r\n",
	"a\n\r\n",
	"a\r\n\n",
	"a\n\nb",
	"a\r\n\r\nb",
	"a\n\nb\r\n\r\nc",
	"a\r\n\r\nb\n\nc",
	"payload\r\n\r",
	"event: message\ndata: {\"ok\":true}\n\nrest",
	"data: a\ndata: b\r\ndata: c\n\ndata: next\n\n",
	mixedDelimiterBody,
}

func FuzzSplitNext_MatchesLegacyOracle(f *testing.F) {
	for _, seed := range splitSeedBodies {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, buf []byte) {
		requireSameSplitAsLegacy(t, buf)
	})
}

// chunkBySizes cuts body into consecutive chunks whose lengths follow sizes
// (a zero size means one byte); whatever sizes do not cover becomes the last
// chunk.
func chunkBySizes(body []byte, sizes []byte) [][]byte {
	var chunks [][]byte
	start := 0
	for _, size := range sizes {
		if start >= len(body) {
			break
		}
		end := min(start+max(int(size), 1), len(body))
		chunks = append(chunks, body[start:end])
		start = end
	}
	if start < len(body) {
		chunks = append(chunks, body[start:])
	}
	return chunks
}

// FuzzScanner_IncrementalDrainMatchesLegacyOracle checks that framing a body
// delivered in arbitrary chunks through one Scanner yields the same events
// and the same unterminated tail as the frozen stateless splitter over the
// whole body.
func FuzzScanner_IncrementalDrainMatchesLegacyOracle(f *testing.F) {
	for _, seed := range splitSeedBodies {
		f.Add([]byte(seed), []byte{1})
		f.Add([]byte(seed), []byte{3, 1, 7})
		f.Add([]byte(seed), []byte{0, 0, 0, 0, 2})
	}
	f.Fuzz(func(t *testing.T, body []byte, sizes []byte) {
		wantEvents, wantTail := drainWithLegacyOracle(body)
		gotEvents, gotTail := drainIncrementally(chunkBySizes(body, sizes))
		require.Equal(t, wantEvents, gotEvents)
		require.Equal(t, wantTail, gotTail)
	})
}
