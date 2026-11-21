package conversation

import "weave-os/router/internal/feedback"

type Lifecycle struct {
	noProgress          *NoProgressTracker
	compaction          *CompactionTracker
	textRepetitionBreak bool
	routerFeedback      feedback.RouterFeedbackStore
}

func NewLifecycle() *Lifecycle {
	return &Lifecycle{noProgress: NewNoProgressTracker(), compaction: NewCompactionTracker(), textRepetitionBreak: true}
}

func (l *Lifecycle) NoProgress() *NoProgressTracker {
	if l == nil {
		return nil
	}
	return l.noProgress
}

func (l *Lifecycle) TextRepetitionBreak() bool {
	return l != nil && l.textRepetitionBreak
}

func (l *Lifecycle) SetTextRepetitionBreak(enabled bool) {
	l.textRepetitionBreak = enabled
}

func (l *Lifecycle) Compaction() *CompactionTracker {
	if l == nil {
		return nil
	}
	return l.compaction
}

func (l *Lifecycle) RouterFeedbackStore() feedback.RouterFeedbackStore {
	if l == nil {
		return nil
	}
	return l.routerFeedback
}

func (l *Lifecycle) SetRouterFeedbackStore(store feedback.RouterFeedbackStore) {
	l.routerFeedback = store
}
