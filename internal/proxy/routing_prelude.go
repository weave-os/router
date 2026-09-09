package proxy

import "weave-os/router/internal/translate"

type anthropicPreludeState struct {
	visibleBlocks int64
	lastMarker    string
}

type openAIPreludeState struct {
	lastMarker string
}

func (s *openAIPreludeState) emit(
	streaming bool,
	marker string,
	writer *translate.OpenAIRoutingMarkerWriter,
	prelude *preludeBuffer,
) error {
	if prelude == nil || !prelude.PreludeSent() || marker != s.lastMarker {
		if err := writer.Prelude(streaming); err != nil {
			return err
		}
		if !streaming || marker == "" || prelude == nil {
			return nil
		}
		if err := prelude.CommitPrelude(); err != nil {
			return err
		}
		s.lastMarker = marker
		return nil
	}
	writer.ContinueAfterPrelude(streaming)
	return nil
}

func (s *anthropicPreludeState) markerForAttempt(marker string, prelude *preludeBuffer) string {
	if prelude == nil || !prelude.PreludeSent() || marker != s.lastMarker {
		return marker
	}
	return ""
}

func (s *anthropicPreludeState) emit(
	streaming bool,
	marker string,
	writer interface {
		Prelude(bool) error
		ContinueAfterPrelude(bool, int64) error
	},
	prelude *preludeBuffer,
) error {
	if prelude == nil || !prelude.PreludeSent() {
		if err := writer.Prelude(streaming); err != nil {
			return err
		}
		if !streaming || marker == "" || prelude == nil {
			return nil
		}
		if err := prelude.CommitPrelude(); err != nil {
			return err
		}
		s.visibleBlocks = 1
		s.lastMarker = marker
		return nil
	}
	if err := writer.ContinueAfterPrelude(streaming, s.visibleBlocks); err != nil {
		return err
	}
	if !streaming || marker == "" || marker == s.lastMarker {
		return nil
	}
	if err := prelude.CommitPrelude(); err != nil {
		return err
	}
	s.visibleBlocks++
	s.lastMarker = marker
	return nil
}

var (
	_ interface {
		Prelude(bool) error
		ContinueAfterPrelude(bool, int64) error
	} = (*translate.AnthropicRoutingMarkerWriter)(nil)
	_ interface {
		Prelude(bool) error
		ContinueAfterPrelude(bool, int64) error
	} = (*translate.AnthropicSSETranslator)(nil)
	_ interface {
		Prelude(bool) error
		ContinueAfterPrelude(bool, int64) error
	} = (*translate.ResponsesToAnthropicWriter)(nil)
)
