package sse

type Scanner struct {
	resumeAt int
}

func (s *Scanner) Next(buf []byte) (event []byte, consumed int) {
	event, consumed = splitFrom(buf, s.resumeAt)
	if consumed > 0 {
		s.resumeAt = 0
		return event, consumed
	}
	s.resumeAt = max(0, len(buf)-(maxRecordSepLen-1))
	return nil, 0
}

func (s *Scanner) Reset() {
	s.resumeAt = 0
}
