package proxy

import (
	"errors"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// Chunk sizes every buffered SSE consumer is exercised at: one byte, odd sizes
// that land boundaries mid-line and mid-delimiter, and the provider adapters'
// read size.
var fragmentationChunkSizes = []int{1, 3, 7, 13, 4096}

// largeFixtureBytes is the size past which a fixture is fed only in the
// adapters' 4 KiB reads: byte-at-a-time writes of a large body exercise nothing
// further and make the test's runtime a function of the scanner's speed.
const largeFixtureBytes = 2048

func chunkSizesFor(body string) []int {
	if len(body) > largeFixtureBytes {
		return []int{4096}
	}
	return fragmentationChunkSizes
}

// chunkEvery splits body into consecutive writes of at most size bytes.
func chunkEvery(body string, size int) []string {
	var chunks []string
	for start := 0; start < len(body); start += size {
		chunks = append(chunks, body[start:min(start+size, len(body))])
	}
	return chunks
}

// writeChunks writes each chunk as its own Write call, failing the test on the
// first error.
func writeChunks(t *testing.T, w io.Writer, chunks []string) {
	t.Helper()
	for _, chunk := range chunks {
		_, err := w.Write([]byte(chunk))
		require.NoError(t, err)
	}
}

// errSinkClosed is the error a failingRecorder returns while it still owes
// failures.
var errSinkClosed = errors.New("sink closed")

// failingRecorder rejects the first failures writes so a wrapper's behavior on
// a forward-write error can be observed before any byte is consumed.
type failingRecorder struct {
	*httptest.ResponseRecorder
	failures int
}

func (r *failingRecorder) Write(p []byte) (int, error) {
	if r.failures > 0 {
		r.failures--
		return 0, errSinkClosed
	}
	return r.ResponseRecorder.Write(p)
}
