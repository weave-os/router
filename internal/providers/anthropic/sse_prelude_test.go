package anthropic

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/httputil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type chunkReader struct {
	chunks [][]byte
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	chunk := r.chunks[0]
	n := copy(p, chunk)
	if n == len(chunk) {
		r.chunks = r.chunks[1:]
	} else {
		r.chunks[0] = chunk[n:]
	}
	return n, nil
}

func TestInspectSSEPrelude_ClassifiesSplitOverloadAs529(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	input := &chunkReader{chunks: [][]byte{
		[]byte("event: er"),
		[]byte("ror\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\","),
		[]byte("\"message\":\"Overloaded\"}}\n\n"),
	}}

	reader, err := inspectSSEPrelude(ctx, cancel, time.Second, input, nil)

	assert.Nil(t, reader)
	var upstreamErr *providers.UpstreamErrorResponse
	require.ErrorAs(t, err, &upstreamErr)
	assert.Equal(t, 529, upstreamErr.Status)
	assert.True(t, providers.IsRetryable(err))
	assert.JSONEq(t, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, string(upstreamErr.Body))
}

func TestInspectSSEPrelude_DoesNotMatchErrorTextInsideData(t *testing.T) {
	frame := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"event: error\"}}\n\n"
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)

	reader, err := inspectSSEPrelude(ctx, cancel, time.Second, &chunkReader{chunks: [][]byte{[]byte(frame)}}, nil)

	require.NoError(t, err)
	got, readErr := io.ReadAll(reader)
	require.NoError(t, readErr)
	assert.Equal(t, frame, string(got))
}

const preludeMessageStart = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"usage\":{\"input_tokens\":10}}}\n\n"

const preludeOverloadedFrame = "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"

// preludeHealthyStream is a short healthy stream: three frames, so bytes past
// the first event are on the wire before inspection finishes.
const preludeHealthyStream = preludeMessageStart +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: ping\ndata: {\"type\":\"ping\"}\n\n"

// Fixture chunks must not exceed the adapters' read size so each chunk models
// one provider read during inspection.
func chunkBytes(t *testing.T, body string, size int) [][]byte {
	t.Helper()
	require.LessOrEqual(t, size, httputil.FlushChunk)
	var chunks [][]byte
	for start := 0; start < len(body); start += size {
		chunks = append(chunks, []byte(body[start:min(start+size, len(body))]))
	}
	return chunks
}

func inspectChunks(t *testing.T, chunks [][]byte) (io.Reader, error) {
	t.Helper()
	ctx, cancel := context.WithCancelCause(context.Background())
	t.Cleanup(func() { cancel(nil) })
	return inspectSSEPrelude(ctx, cancel, time.Second, &chunkReader{chunks: chunks}, nil)
}

func readAll(t *testing.T, reader io.Reader) string {
	t.Helper()
	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	return string(got)
}

// The reader handed back must replay every byte inspected, not only the first
// event: the read that completes the first frame routinely carries the start
// of the next one, and dropping it would corrupt the stream.
func TestInspectSSEPrelude_ReplaysAllBufferedBytesThenBody(t *testing.T) {
	firstEnd := len(preludeMessageStart)
	chunks := [][]byte{
		[]byte(preludeHealthyStream[:10]),
		[]byte(preludeHealthyStream[10 : firstEnd+20]),
		[]byte(preludeHealthyStream[firstEnd+20:]),
	}

	reader, err := inspectChunks(t, chunks)

	require.NoError(t, err)
	assert.Equal(t, preludeHealthyStream, readAll(t, reader))
}

// However the upstream's bytes arrive, a healthy stream is replayed whole and
// an in-stream error is classified the same way.
func TestInspectSSEPrelude_FragmentationMatchesSingleRead(t *testing.T) {
	sizes := []int{1, 3, 7, 13}

	for _, stream := range []struct {
		name string
		body string
	}{
		{name: "lf framing", body: preludeHealthyStream},
		{name: "crlf framing", body: strings.ReplaceAll(preludeHealthyStream, "\n", "\r\n")},
		{name: "unterminated single frame", body: strings.TrimSuffix(preludeMessageStart, "\n\n")},
	} {
		t.Run(stream.name, func(t *testing.T) {
			reader, err := inspectChunks(t, [][]byte{[]byte(stream.body)})
			require.NoError(t, err)
			require.Equal(t, stream.body, readAll(t, reader))

			for _, size := range sizes {
				reader, err := inspectChunks(t, chunkBytes(t, stream.body, size))
				require.NoError(t, err, "chunk size %d", size)
				assert.Equal(t, stream.body, readAll(t, reader), "chunk size %d", size)
			}
			for i := 1; i < len(stream.body); i++ {
				reader, err := inspectChunks(t, [][]byte{[]byte(stream.body[:i]), []byte(stream.body[i:])})
				require.NoError(t, err, "split at %d", i)
				require.Equal(t, stream.body, readAll(t, reader), "split at %d", i)
			}
		})
	}

	t.Run("overloaded error frame", func(t *testing.T) {
		assertOverloaded := func(t *testing.T, chunks [][]byte, label string) {
			t.Helper()
			reader, err := inspectChunks(t, chunks)
			require.Nil(t, reader, label)
			var upstreamErr *providers.UpstreamErrorResponse
			require.ErrorAs(t, err, &upstreamErr, label)
			require.Equal(t, 529, upstreamErr.Status, label)
			require.JSONEq(t, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, string(upstreamErr.Body), label)
		}
		assertOverloaded(t, [][]byte{[]byte(preludeOverloadedFrame)}, "single read")
		for _, size := range sizes {
			assertOverloaded(t, chunkBytes(t, preludeOverloadedFrame, size), fmt.Sprintf("chunk size %d", size))
		}
		for i := 1; i < len(preludeOverloadedFrame); i++ {
			assertOverloaded(t, [][]byte{[]byte(preludeOverloadedFrame[:i]), []byte(preludeOverloadedFrame[i:])}, fmt.Sprintf("split at %d", i))
		}
	})
}

// A first frame far larger than one read is replayed whole whether it completes
// under the inspection cap or the cap ends inspection first.
func TestInspectSSEPrelude_LargeFirstFrameInProviderReads(t *testing.T) {
	for _, tc := range []struct {
		name string
		text int
	}{
		{name: "completes under the cap", text: providers.MaxBufferedErrorBytes / 2},
		{name: "outgrows the cap", text: providers.MaxBufferedErrorBytes + 4*httputil.FlushChunk},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"" +
				strings.Repeat("x", tc.text) + "\"}}\n\n" + preludeMessageStart

			reader, err := inspectChunks(t, chunkBytes(t, body, httputil.FlushChunk))

			require.NoError(t, err)
			assert.Equal(t, body, readAll(t, reader))
		})
	}
}

// An upstream that closes before the first frame completes yields the bytes it
// did send, so the caller can still render them rather than a synthetic error.
func TestInspectSSEPrelude_EOFBeforeFirstFrameReplaysPartialBytes(t *testing.T) {
	partial := preludeMessageStart[:len(preludeMessageStart)-5]

	reader, err := inspectChunks(t, chunkBytes(t, partial, 7))

	require.NoError(t, err)
	assert.Equal(t, partial, readAll(t, reader))
}
