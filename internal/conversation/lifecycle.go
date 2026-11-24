package conversation

import (
	"time"

	"weave-os/router/internal/feedback"
	"weave-os/router/internal/router/handover"
	"weave-os/router/internal/router/sessionstrategy"
)

type Lifecycle struct {
	compaction        *CompactionTracker
	summarizer        handover.Summarizer
	routerFeedback    feedback.RouterFeedbackStore
	sessionStrategies sessionstrategy.Store
	piHandoffSecret   []byte
	feedbackLinks     *feedback.Links
}

type Config struct {
	Summarizer          handover.Summarizer
	RouterFeedbackStore feedback.RouterFeedbackStore
	SessionStrategies   sessionstrategy.Store
	FeedbackLinks       *feedback.Links
	PiHandoffSecret     string
}

func (l *Lifecycle) Configure(cfg Config) {
	l.summarizer = cfg.Summarizer
	l.routerFeedback = cfg.RouterFeedbackStore
	l.sessionStrategies = cfg.SessionStrategies
	l.feedbackLinks = cfg.FeedbackLinks
	if len(cfg.PiHandoffSecret) >= 32 {
		l.piHandoffSecret = []byte(cfg.PiHandoffSecret)
	}
}

func NewLifecycle() *Lifecycle {
	return &Lifecycle{compaction: NewCompactionTracker()}
}

func (l *Lifecycle) Compaction() *CompactionTracker {
	if l == nil {
		return nil
	}
	return l.compaction
}

func (l *Lifecycle) Summarizer() handover.Summarizer {
	if l == nil {
		return nil
	}
	return l.summarizer
}

func (l *Lifecycle) SetSummarizer(summarizer handover.Summarizer) {
	l.summarizer = summarizer
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

func (l *Lifecycle) SessionStrategies() sessionstrategy.Store {
	if l == nil {
		return nil
	}
	return l.sessionStrategies
}

func (l *Lifecycle) SetSessionStrategies(store sessionstrategy.Store) {
	l.sessionStrategies = store
}

func (l *Lifecycle) PiHandoff(now func() time.Time) PiHandoff {
	if l == nil {
		return PiHandoff{Now: now}
	}
	return PiHandoff{Secret: l.piHandoffSecret, Now: now}
}

func (l *Lifecycle) SetPiHandoffSecret(secret []byte) {
	l.piHandoffSecret = secret
}

func (l *Lifecycle) FeedbackLinks() *feedback.Links {
	if l == nil {
		return nil
	}
	return l.feedbackLinks
}

func (l *Lifecycle) SetFeedbackLinks(links *feedback.Links) {
	l.feedbackLinks = links
}
