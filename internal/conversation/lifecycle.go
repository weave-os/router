package conversation

import (
	"context"
	"time"

	"weave-os/router/internal/feedback"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/router/handover"
	"weave-os/router/internal/router/sessionstrategy"
)

type Lifecycle struct {
	noProgress          *NoProgressTracker
	compaction          *CompactionTracker
	textRepetitionBreak bool
	summarizer          handover.Summarizer
	routerFeedback      feedback.RouterFeedbackStore
	sessionStrategies   sessionstrategy.Store
	piHandoffSecret     []byte
	feedbackLinks       *feedback.Links
}

type Config struct {
	Summarizer          handover.Summarizer
	RouterFeedbackStore feedback.RouterFeedbackStore
	SessionStrategies   sessionstrategy.Store
	FeedbackLinks       *feedback.Links
	PiHandoffSecret     string
	TextRepetitionBreak *bool
}

func (l *Lifecycle) Configure(cfg Config) {
	l.summarizer = cfg.Summarizer
	l.routerFeedback = cfg.RouterFeedbackStore
	l.sessionStrategies = cfg.SessionStrategies
	l.feedbackLinks = cfg.FeedbackLinks
	if len(cfg.PiHandoffSecret) >= 32 {
		l.piHandoffSecret = []byte(cfg.PiHandoffSecret)
	}
	if cfg.TextRepetitionBreak != nil {
		l.textRepetitionBreak = *cfg.TextRepetitionBreak
	}
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

func (l *Lifecycle) TextRepetitionBreakEnabled(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyTextRepetitionBreak, l.TextRepetitionBreak())
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
