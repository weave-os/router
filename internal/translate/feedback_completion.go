package translate

import "context"

// FeedbackIntent is the client-facing copy selected by proxy policy. It never
// enters provider requests, usage summaries, or provider progress observation.
type FeedbackIntent struct {
	Text string
}

// CompletionOutcome describes provider semantics independently of client wire
// format. A successful dispatch alone does not establish a natural answer.
type CompletionOutcome uint8

const (
	CompletionUndetermined CompletionOutcome = iota
	CompletionNatural
	CompletionSuppressed
)

// CompletionFacts belong to one provider attempt. Terminal closure and natural
// completion are distinct: an error or a tool step may close a stream too.
type CompletionFacts struct {
	Outcome CompletionOutcome
	Closed  bool
	Answer  bool
	Tool    bool
	Refusal bool
	Failed  bool
	Echoed  bool
}

// Eligible requires a closed natural answer without client tools, refusals, failures, or echoed feedback.
func (f CompletionFacts) Eligible() bool {
	return f.Closed && f.Outcome == CompletionNatural && f.Answer && !f.Tool && !f.Refusal && !f.Failed && !f.Echoed
}

// FeedbackDelivery owns the request's exactly-once completion phase, separately
// from NoticeDelivery's pre-content phase. Each new observer replaces only the
// attempt facts; retries never reset the request's delivered latch.
type FeedbackDelivery struct {
	ctx       context.Context
	intent    FeedbackIntent
	facts     CompletionFacts
	delivered bool
}

// NewFeedbackDelivery uses the caller context across retries; an attempt timeout must not cancel later delivery.
func NewFeedbackDelivery(ctx context.Context, intent FeedbackIntent) *FeedbackDelivery {
	return &FeedbackDelivery{ctx: ctx, intent: intent}
}

// SetIntent resolves policy after routing for a Responses adapter constructed at ingress.
func (d *FeedbackDelivery) SetIntent(intent FeedbackIntent) { d.intent = intent }

// Enabled lets adapters bypass buffering and preserve bytes when feedback is disabled.
func (d *FeedbackDelivery) Enabled() bool { return d != nil && d.intent.Text != "" }

// Take commits one eligible suffix at the downstream adapter's closure boundary.
func (d *FeedbackDelivery) Take() string {
	if !d.Enabled() || d.delivered || d.ctx.Err() != nil || !d.facts.Eligible() {
		return ""
	}
	d.delivered = true
	return d.intent.Text
}
