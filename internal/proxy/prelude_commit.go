package proxy

import (
	"weave-os/router/internal/sse"
	"weave-os/router/internal/translate"
)

const maxHeldCommitBytes = 64 << 10

// CommitOnOutput defers a sealed prelude until output or terminal data arrives.
func (b *preludeBuffer) CommitOnOutput(format translate.Format) {
	b.isCommitEvent = func(event []byte) bool {
		return translate.StreamEventCommitsOutput(format, event)
	}
}

func (b *preludeBuffer) writeCommitOnOutput(p []byte) (int, error) {
	b.pending = append(b.pending, p...)
	if b.held.Len() > maxHeldCommitBytes || len(b.pending) > maxHeldCommitBytes-b.held.Len() {
		if err := b.commitAndWriteHeld(); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	for len(b.pending) > 0 {
		event, consumed := sse.SplitNext(b.pending)
		if consumed == 0 {
			break
		}
		if !b.isCommitEvent(event) {
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
