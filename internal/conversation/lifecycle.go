package conversation

type Lifecycle struct {
	noProgress          *NoProgressTracker
	textRepetitionBreak bool
	routerFeedback      RouterFeedbackStore
}

func NewLifecycle() *Lifecycle {
	return &Lifecycle{noProgress: NewNoProgressTracker(), textRepetitionBreak: true}
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

func (l *Lifecycle) RouterFeedbackStore() RouterFeedbackStore {
	if l == nil {
		return nil
	}
	return l.routerFeedback
}

func (l *Lifecycle) SetRouterFeedbackStore(store RouterFeedbackStore) {
	l.routerFeedback = store
}
