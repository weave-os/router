package translate

import "errors"

type StreamState uint8

const (
	StreamIdle StreamState = iota
	StreamStarted
	StreamTerminal
	StreamFailed
	StreamCanceled
)

var (
	ErrStreamIncomplete = errors.New("upstream stream ended before a terminal event")
	ErrStreamEmpty      = errors.New("upstream stream ended without output")
	ErrStreamOrder      = errors.New("invalid stream lifecycle transition")
)

type StreamLifecycle struct {
	state         StreamState
	outputStarted bool
	hasOutputIdx  bool
	lastOutputIdx int
}

func NewStreamLifecycle() *StreamLifecycle { return &StreamLifecycle{} }

func (l *StreamLifecycle) State() StreamState { return l.state }

func (l *StreamLifecycle) OutputStarted() bool { return l.outputStarted }

func (l *StreamLifecycle) Start() error {
	if l.state != StreamIdle {
		return ErrStreamOrder
	}
	l.state = StreamStarted
	return nil
}

func (l *StreamLifecycle) Output(index int) error {
	if l.state != StreamStarted {
		return ErrStreamOrder
	}
	if l.hasOutputIdx && index < l.lastOutputIdx {
		return ErrStreamOrder
	}
	l.hasOutputIdx = true
	l.lastOutputIdx = index
	l.outputStarted = true
	return nil
}

func (l *StreamLifecycle) RewindOutput(lastIndex int) {
	l.lastOutputIdx = lastIndex
}

func (l *StreamLifecycle) Terminal() error {
	if l.state != StreamStarted {
		return ErrStreamOrder
	}
	l.state = StreamTerminal
	return nil
}

func (l *StreamLifecycle) Fail() error {
	if l.state != StreamStarted {
		return ErrStreamOrder
	}
	l.state = StreamFailed
	return nil
}

func (l *StreamLifecycle) Cancel() error {
	if l.state != StreamStarted {
		return ErrStreamOrder
	}
	l.state = StreamCanceled
	return nil
}

func (l *StreamLifecycle) EOF() error {
	switch l.state {
	case StreamTerminal, StreamFailed, StreamCanceled:
		return nil
	case StreamIdle:
		return ErrStreamEmpty
	case StreamStarted:
		return ErrStreamIncomplete
	default:
		return ErrStreamOrder
	}
}
