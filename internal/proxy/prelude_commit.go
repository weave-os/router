package proxy

import (
	"net/http"

	"weave-os/router/internal/sse"
	"weave-os/router/internal/translate"
)

const maxHeldCommitBytes = 64 << 10

// streamCommitFormat enables commit-on-output only for streamed responses;
// a buffered non-streamed response keeps the classic first-write commit.
func streamCommitFormat(env *translate.RequestEnvelope, format translate.Format) []translate.Format {
	if env.Stream() {
		return []translate.Format{format}
	}
	return nil
}

// CommitOnOutput defers a sealed prelude until output or terminal data arrives.
func (b *preludeBuffer) CommitOnOutput(format translate.Format) {
	b.commitOnOutput = true
	b.commitFormat = format
}

func (b *preludeBuffer) commit() error {
	if b.committed.Swap(true) {
		return nil
	}
	if !b.preludeSent {
		if b.bufStatus != 0 {
			sse.PrepareJSONResponse(b.inner)
			b.inner.WriteHeader(b.bufStatus)
		}
		if b.bufBody.Len() > 0 {
			if _, err := sse.WriteJSONChunk(b.inner, b.bufBody.Bytes()); err != nil {
				return err
			}
		}
		b.preludeSent = true
	}
	b.bufStatus = 0
	b.bufBody.Reset()
	if f, ok := b.inner.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

// releaseHeldResult flushes events a served attempt held short of commit.
// A release failure is the attempt's outcome, not a lease abort: the attempt
// stays booked with the state the buffer reached.
func (b *preludeBuffer) releaseHeldResult(winnerIdx int, err error) (int, error) {
	if err != nil || b == nil {
		return winnerIdx, err
	}
	return winnerIdx, b.releaseHeld()
}

func (b *preludeBuffer) writeCommitOnOutput(p []byte) (int, error) {
	if b.held.Len()+len(b.pending)+len(p) > maxHeldCommitBytes {
		if err := b.commitAndWriteHeld(); err != nil {
			return 0, err
		}
		if _, err := sse.WriteJSONChunk(b.inner, p); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	b.pending = append(b.pending, p...)
	for len(b.pending) > 0 {
		event, consumed := sse.SplitNext(b.pending)
		if consumed == 0 {
			break
		}
		if !translate.StreamEventCommitsOutput(b.commitFormat, event) {
			_, _ = b.held.Write(b.pending[:consumed])
			b.pending = b.pending[consumed:]
			continue
		}
		if err := b.commitAndWriteHeld(); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	return len(p), nil
}

func (b *preludeBuffer) releaseHeld() error {
	if b.held.Len() == 0 && len(b.pending) == 0 {
		return nil
	}
	if err := b.commitAndWriteHeld(); err != nil {
		return err
	}
	b.Flush()
	return nil
}

func (b *preludeBuffer) commitAndWriteHeld() error {
	output := append([]byte(nil), b.held.Bytes()...)
	output = append(output, b.pending...)
	b.held.Reset()
	b.pending = nil
	if err := b.commit(); err != nil {
		return err
	}
	_, err := sse.WriteJSONChunk(b.inner, output)
	return err
}
