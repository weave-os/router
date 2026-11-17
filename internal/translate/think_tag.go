package translate

import "strings"

type thinkKind int

const (
	segThinking thinkKind = iota
	segText
)

type thinkSegment struct {
	kind thinkKind
	text string
}

type thinkState int

const (
	thScanningOpen thinkState = iota
	thInsideThink
	thPassthrough
)

const (
	thinkOpenTag  = "<think>"
	thinkCloseTag = "</think>"
)

type thinkTagSplitter struct {
	state thinkState
	accum strings.Builder
	tail  strings.Builder
}

func (s *thinkTagSplitter) Feed(content string) []thinkSegment {
	if content == "" {
		return nil
	}
	if s.state == thPassthrough {
		return []thinkSegment{{kind: segText, text: content}}
	}

	var segments []thinkSegment
	for i := 0; i < len(content); i++ {
		if s.state == thScanningOpen {
			if isSpace(content[i]) && s.accum.Len() == 0 {
				continue
			}
			s.accum.WriteByte(content[i])
			cur := s.accum.String()
			if cur == thinkOpenTag {
				s.state = thInsideThink
				s.accum.Reset()
				continue
			}
			if strings.HasPrefix(thinkOpenTag, cur) {
				continue
			}
			s.state = thPassthrough
			s.accum.Reset()
			segments = append(segments, thinkSegment{kind: segText, text: cur + content[i+1:]})
			return segments
		}

		combined := s.tail.String() + content[i:]
		s.tail.Reset()
		if idx := strings.Index(combined, thinkCloseTag); idx >= 0 {
			if idx > 0 {
				segments = append(segments, thinkSegment{kind: segThinking, text: combined[:idx]})
			}
			s.state = thPassthrough
			if rem := combined[idx+len(thinkCloseTag):]; rem != "" {
				segments = append(segments, thinkSegment{kind: segText, text: rem})
			}
			return segments
		}
		keep := len(thinkCloseTag) - 1
		if len(combined) > keep {
			segments = append(segments, thinkSegment{kind: segThinking, text: combined[:len(combined)-keep]})
			s.tail.WriteString(combined[len(combined)-keep:])
		} else {
			s.tail.WriteString(combined)
		}
		return segments
	}
	return segments
}

func (s *thinkTagSplitter) Flush() []thinkSegment {
	var segments []thinkSegment
	switch s.state {
	case thScanningOpen:
		if s.accum.Len() > 0 {
			segments = append(segments, thinkSegment{kind: segText, text: s.accum.String()})
		}
	case thInsideThink:
		if s.tail.Len() > 0 {
			segments = append(segments, thinkSegment{kind: segThinking, text: s.tail.String()})
		}
	case thPassthrough:
	}
	s.accum.Reset()
	s.tail.Reset()
	s.state = thPassthrough
	if len(segments) == 0 {
		return nil
	}
	return segments
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}
