package sse_test

import (
	"bytes"
	"testing"

	"weave-os/router/internal/sse"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// drainWithLegacyOracle frames every complete event in body with the frozen
// legacy splitter and returns the events plus the unterminated tail.
func drainWithLegacyOracle(body []byte) (events []string, tail string) {
	remaining := body
	for {
		event, n := legacySplitNext(remaining)
		if n == 0 {
			return events, string(remaining)
		}
		events = append(events, string(event))
		remaining = remaining[n:]
	}
}

// drainIncrementally appends chunks one write at a time into a growing
// buffer and drains complete events through a single Scanner after each
// append, the way the buffered translators do.
func drainIncrementally(chunks [][]byte) (events []string, tail string) {
	var scanner sse.Scanner
	var buf []byte
	for _, chunk := range chunks {
		buf = append(buf, chunk...)
		for {
			event, n := scanner.Next(buf)
			if n == 0 {
				break
			}
			events = append(events, string(event))
			buf = buf[n:]
		}
	}
	return events, string(buf)
}

// splitIntoChunks cuts body at every offset listed in cuts (ascending) and
// returns the pieces, dropping empty ones.
func splitIntoChunks(body []byte, cuts ...int) [][]byte {
	var chunks [][]byte
	start := 0
	for _, cut := range cuts {
		if cut > start {
			chunks = append(chunks, body[start:cut])
			start = cut
		}
	}
	if start < len(body) {
		chunks = append(chunks, body[start:])
	}
	return chunks
}

// mixedDelimiterBody exercises both delimiters, a delimiter at offset zero,
// the mixed candidates the grammar rejects, and an unterminated tail.
const mixedDelimiterBody = "event: a\ndata: 1\n\nb\r\n\r\n\n\nc\n\r\nd\r\n\ne\n\nf: incomplete\r\n\r"

func TestScanner_ZeroValueMatchesSplitNext(t *testing.T) {
	tests := []struct {
		name string
		buf  string
	}{
		{"LF delimiter", "event: message\ndata: {}\n\nrest"},
		{"CRLF delimiter", "event: message\r\ndata: {}\r\n\r\nrest"},
		{"earlier LF wins over later CRLF", "a\n\nb\r\n\r\nc"},
		{"earlier CRLF wins over later LF", "a\r\n\r\nb\n\nc"},
		{"LF-CRLF mix is not a delimiter", "a\n\r\n"},
		{"CRLF-LF mix ends at the LF pair", "a\r\n\n"},
		{"delimiter at offset zero", "\n\nrest"},
		{"incomplete", "event: message\ndata: {}"},
		{"empty", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var scanner sse.Scanner
			buf := []byte(tc.buf)

			wantEvent, wantN := sse.SplitNext(buf)
			gotEvent, gotN := scanner.Next(buf)

			assert.Equal(t, wantN, gotN)
			assert.Equal(t, string(wantEvent), string(gotEvent))
			assert.Equal(t, wantEvent == nil, gotEvent == nil)
		})
	}
}

func TestScanner_EveryTwoWaySplitMatchesStatelessDrain(t *testing.T) {
	body := []byte(mixedDelimiterBody)
	wantEvents, wantTail := drainWithLegacyOracle(body)
	require.Equal(t, []string{"event: a\ndata: 1", "b", "", "c\n\r\nd\r", "e"}, wantEvents,
		"fixture must contain both delimiters, a zero-length event, and rejected mixed candidates")
	require.Equal(t, "f: incomplete\r\n\r", wantTail)

	for cut := 1; cut < len(body); cut++ {
		gotEvents, gotTail := drainIncrementally(splitIntoChunks(body, cut))
		require.Equal(t, wantEvents, gotEvents, "split at %d", cut)
		require.Equal(t, wantTail, gotTail, "split at %d", cut)
	}
}

func TestScanner_EveryThreeWaySplitMatchesStatelessDrain(t *testing.T) {
	body := []byte(mixedDelimiterBody)
	wantEvents, wantTail := drainWithLegacyOracle(body)

	for first := 1; first < len(body); first++ {
		for second := first + 1; second < len(body); second++ {
			gotEvents, gotTail := drainIncrementally(splitIntoChunks(body, first, second))
			require.Equal(t, wantEvents, gotEvents, "splits at %d,%d", first, second)
			require.Equal(t, wantTail, gotTail, "splits at %d,%d", first, second)
		}
	}
}

func TestScanner_ByteAtATimeMatchesStatelessDrain(t *testing.T) {
	body := []byte(mixedDelimiterBody)
	wantEvents, wantTail := drainWithLegacyOracle(body)

	chunks := make([][]byte, 0, len(body))
	for i := range body {
		chunks = append(chunks, body[i:i+1])
	}
	gotEvents, gotTail := drainIncrementally(chunks)

	assert.Equal(t, wantEvents, gotEvents)
	assert.Equal(t, wantTail, gotTail)
}

func TestScanner_EveryShortDelimiterMixMatchesLegacyAcrossAllSplits(t *testing.T) {
	forEachShortDelimiterMix(6, func(buf []byte) {
		wantEvents, wantTail := drainWithLegacyOracle(buf)
		for cut := 0; cut <= len(buf); cut++ {
			gotEvents, gotTail := drainIncrementally(splitIntoChunks(buf, cut))
			require.Equal(t, wantEvents, gotEvents, "buf=%q cut=%d", buf, cut)
			require.Equal(t, wantTail, gotTail, "buf=%q cut=%d", buf, cut)
		}
	})
}

// TestScanner_DelimiterSplitAcrossWritesCompletesOnlyWhenWhole pins the
// overlap the cursor must retain: a delimiter whose first bytes arrived in an
// earlier write is recognized once the rest lands, and never before.
func TestScanner_DelimiterSplitAcrossWritesCompletesOnlyWhenWhole(t *testing.T) {
	for _, delimiter := range []string{"\n\n", "\r\n\r\n"} {
		for cut := 1; cut < len(delimiter); cut++ {
			var scanner sse.Scanner
			buf := []byte("payload" + delimiter[:cut])

			event, n := scanner.Next(buf)
			require.Nil(t, event, "delimiter=%q cut=%d", delimiter, cut)
			require.Equal(t, 0, n, "delimiter=%q cut=%d", delimiter, cut)

			buf = append(buf, delimiter[cut:]...)
			buf = append(buf, "next"...)
			event, n = scanner.Next(buf)
			assert.Equal(t, "payload", string(event), "delimiter=%q cut=%d", delimiter, cut)
			assert.Equal(t, len("payload")+len(delimiter), n, "delimiter=%q cut=%d", delimiter, cut)
		}
	}
}

func TestScanner_NoEventBeforeFullDelimiterArrivesByteAtATime(t *testing.T) {
	body := []byte("data: {}\r\n\r\n")
	var scanner sse.Scanner
	var buf []byte
	for i, c := range body {
		buf = append(buf, c)
		event, n := scanner.Next(buf)
		if i < len(body)-1 {
			require.Nil(t, event, "byte %d must not complete the event", i)
			require.Equal(t, 0, n, "byte %d must not complete the event", i)
			continue
		}
		assert.Equal(t, "data: {}", string(event))
		assert.Equal(t, len(body), n)
	}
}

// TestScanner_SuccessResetsCursorForTheUnconsumedSuffix covers both ways an
// owner follows a hit: dropping the consumed bytes and presenting the rest,
// or (after a write error before consumption) presenting the same buffer
// again and expecting the same event.
func TestScanner_SuccessResetsCursorForTheUnconsumedSuffix(t *testing.T) {
	var scanner sse.Scanner
	buf := []byte("first\n\nsecond\r\n\r\nthird")

	event, n := scanner.Next(buf)
	require.Equal(t, "first", string(event))
	require.Equal(t, 7, n)

	event, n = scanner.Next(buf)
	assert.Equal(t, "first", string(event), "an unconsumed buffer must yield the same event again")
	assert.Equal(t, 7, n)

	buf = buf[n:]
	event, n = scanner.Next(buf)
	assert.Equal(t, "second", string(event))
	assert.Equal(t, len("second\r\n\r\n"), n)

	buf = buf[n:]
	event, n = scanner.Next(buf)
	assert.Nil(t, event)
	assert.Equal(t, 0, n)
	assert.Equal(t, "third", string(buf))
}

// TestScanner_ResumesPastRejectedPrefixUntilReset proves the cursor advances:
// a delimiter written into bytes the scanner already rejected (a deliberate
// contract violation) stays invisible until Reset forgets that progress.
func TestScanner_ResumesPastRejectedPrefixUntilReset(t *testing.T) {
	var scanner sse.Scanner
	buf := []byte("0123456789")
	event, n := scanner.Next(buf)
	require.Nil(t, event)
	require.Equal(t, 0, n)

	buf[1], buf[2] = '\n', '\n'
	buf = append(buf, 'z')
	event, n = scanner.Next(buf)
	assert.Nil(t, event, "the rewritten prefix lies before the resume offset and must be skipped")
	assert.Equal(t, 0, n)

	scanner.Reset()
	event, n = scanner.Next(buf)
	assert.Equal(t, "0", string(event), "Reset must make the whole buffer visible again")
	assert.Equal(t, 3, n)
}

func TestScanner_ResetBeforeShrinkingBufferFramesCorrectly(t *testing.T) {
	var scanner sse.Scanner
	_, n := scanner.Next(bytes.Repeat([]byte{'p'}, 100))
	require.Equal(t, 0, n)

	scanner.Reset()
	event, n := scanner.Next([]byte("a\n\nb"))
	assert.Equal(t, "a", string(event), "Reset must forget the discarded prefix")
	assert.Equal(t, 3, n)
}

func TestScanner_EventAliasesBufferAndKeepsCapacity(t *testing.T) {
	buf := make([]byte, len("event\n\nrest"), len("event\n\nrest")+16)
	copy(buf, "event\n\nrest")
	var scanner sse.Scanner

	event, n := scanner.Next(buf)

	require.Equal(t, "event", string(event))
	require.Equal(t, len("event\n\n"), n)
	assert.Equal(t, cap(buf), cap(event), "owners re-slice event[:n] to forward the delimiter, so capacity must survive")
	assert.True(t, &buf[0] == &event[0])
	assert.Equal(t, "event\n\n", string(event[:n]))
}

func TestScanner_NextDoesNotAllocate(t *testing.T) {
	complete := []byte("data: {\"ok\":true}\n\nrest")
	incomplete := bytes.Repeat([]byte("data: partial\n"), 64)
	var scanner sse.Scanner

	assert.Zero(t, testing.AllocsPerRun(100, func() {
		scanner.Next(complete)
	}))
	assert.Zero(t, testing.AllocsPerRun(100, func() {
		scanner.Next(incomplete)
	}))
}

// TestScanner_LargeFrameInFixedChunksDrainsOnce feeds a frame far larger than
// a provider write in 4 KiB pieces and checks the event arrives whole and
// exactly once, with the tail after it intact.
func TestScanner_LargeFrameInFixedChunksDrainsOnce(t *testing.T) {
	payload := bytes.Repeat([]byte{'x'}, 300*1024)
	body := append(append([]byte("data: "), payload...), "\n\ndata: tail"...)
	const chunkSize = 4096
	var chunks [][]byte
	for start := 0; start < len(body); start += chunkSize {
		chunks = append(chunks, body[start:min(start+chunkSize, len(body))])
	}

	events, tail := drainIncrementally(chunks)

	require.Len(t, events, 1)
	assert.Equal(t, len("data: ")+len(payload), len(events[0]))
	assert.Equal(t, "data: tail", tail)
}
