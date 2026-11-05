package proxy

import (
	"weave-os/router/internal/sse"
	"weave-os/router/internal/translate"
)

// CommitOnOutput defers a sealed prelude until output or terminal data arrives.
func (b *preludeBuffer) CommitOnOutput(format translate.Format) {
	b.commitOutput = func(event []byte) bool {
		return translate.StreamEventCommitsOutput(format, event)
	}
}

func (b *preludeBuffer) writeCommitOnOutput(p []byte) (int, error) {
	b.pending = append(b.pending, p...)
	for len(b.pending) > 0 {
		event, consumed := sse.SplitNext(b.pending)
		if consumed == 0 {
			break
		}
		if !b.commitOutput(event) {
			_, _ = b.held.Write(b.pending[:consumed])
			b.pending = b.pending[consumed:]
			continue
		}
		output := make([]byte, 0, b.held.Len()+len(b.pending))
		output = append(output, b.held.Bytes()...)
		output = append(output, b.pending...)
		b.held.Reset()
		b.pending = nil
		if err := b.commit(); err != nil {
			return 0, err
		}
		if _, err := sse.WriteJSONChunk(b.inner, output); err != nil {
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
	output := make([]byte, 0, b.held.Len()+len(b.pending))
	output = append(output, b.held.Bytes()...)
	output = append(output, b.pending...)
	b.held.Reset()
	b.pending = nil
	if err := b.commit(); err != nil {
		return err
	}
	if _, err := sse.WriteJSONChunk(b.inner, output); err != nil {
		return err
	}
	b.Flush()
	return nil
}

func (b *preludeBuffer) discardCommitOnOutput() {
	b.held.Reset()
	b.pending = nil
}
