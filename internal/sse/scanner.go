package sse

// Scanner frames an append-only buffer without rescanning rejected bytes. Its
// zero value is ready to use. It owns a cursor, not the payload: appending or
// reallocating an unchanged prefix is valid; replacing it requires Reset.
// A successful Next resets progress, so the caller may present the unconsumed
// suffix, or retry the same event after an error before consumption.
type Scanner struct {
	// resumeAt is the offset in the next presented buffer where a delimiter
	// can still begin; every LF candidate before it was already rejected.
	resumeAt int
}

// Next returns the next complete SSE event in buf (without its delimiter) and
// the total bytes consumed, with exactly SplitNext's grammar, aliasing, and
// capacity semantics; consumed == 0 means no complete event has arrived yet.
func (s *Scanner) Next(buf []byte) (event []byte, consumed int) {
	event, consumed = splitFrom(buf, s.resumeAt)
	if consumed > 0 {
		s.resumeAt = 0
		return event, consumed
	}
	// Keep enough overlap for a delimiter split across writes.
	s.resumeAt = max(0, len(buf)-(maxRecordSepLen-1))
	return nil, 0
}

// Reset forgets scan progress. Call it whenever the buffer's unconsumed bytes
// are discarded or replaced rather than appended to.
func (s *Scanner) Reset() {
	s.resumeAt = 0
}
