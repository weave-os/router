package dispatch

import (
	"context"
	"sync"
	"time"

	"weave-os/router/internal/inference"
	"weave-os/router/internal/requestcontext"
)

// AttemptTrace collects one request's upstream attempts so telemetry, routing
// badges and terminal errors can name who owns each failure.
type AttemptTrace struct {
	mu                 sync.Mutex
	inbound            []byte
	sent               []byte
	customerCredential bool
	baseURL            string
	deploymentBaseURL  string
	current            inference.Target
	firstAttemptAt     time.Time
	attemptAt          time.Time
	attempts           []TracedAttempt
	// hideRouterDetails is set when the request opted out of routing badges
	// or the installation hides terminal surfaces.
	hideRouterDetails bool
	// slowStartMuted is set for turns whose output a client parses, where
	// router text would corrupt the reply.
	slowStartMuted bool
}

// TracedAttempt is one finished upstream attempt and its diagnosis.
type TracedAttempt struct {
	Target inference.Target
	// BaseURL is the customer gateway the attempt reached, empty for a
	// provider's own endpoint.
	BaseURL   string
	Err       error
	Diagnosis inference.AttemptDiagnosis
}

// NewAttemptTrace starts a trace for the attempts that dispatch inbound.
func NewAttemptTrace(inbound []byte, hideRouterDetails bool) *AttemptTrace {
	return &AttemptTrace{
		inbound:           inbound,
		hideRouterDetails: hideRouterDetails,
	}
}

// Inbound is the client body before any translation, for comparison with the
// sent body.
func (t *AttemptTrace) Inbound() []byte {
	return t.inbound
}

// HideDetails reports whether router-authored text must stay off the response.
func (t *AttemptTrace) HideDetails() bool {
	return t != nil && t.hideRouterDetails
}

// MuteSlowStart suppresses the slow-start notice for the rest of the request.
func (t *AttemptTrace) MuteSlowStart() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.slowStartMuted = true
	t.mu.Unlock()
}

// ServingBaseURL is the gateway endpoint of the latest attempt.
func (t *AttemptTrace) ServingBaseURL() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.baseURL
}

// BeginAttempt marks target as the attempt in flight and clears what the
// previous attempt sent.
func (t *AttemptTrace) BeginAttempt(target inference.Target, deploymentBaseURL string, now time.Time) {
	t.mu.Lock()
	t.current, t.attemptAt = target, now
	t.deploymentBaseURL = deploymentBaseURL
	if t.firstAttemptAt.IsZero() {
		t.firstAttemptAt = now
	}
	t.sent, t.customerCredential, t.baseURL = nil, false, deploymentBaseURL
	t.mu.Unlock()
}

// ObserveSent records the prepared body and the endpoint it is sent to.
func (t *AttemptTrace) ObserveSent(ctx context.Context, body []byte, customerCredential bool) {
	t.mu.Lock()
	baseURL := requestcontext.EffectiveBaseURL(ctx, t.deploymentBaseURL)
	t.sent = body
	t.customerCredential = customerCredential
	t.baseURL = baseURL
	t.mu.Unlock()
}

// Record diagnoses the attempt dispatch just finished and appends it to the
// trace; err is nil on a served attempt.
func (t *AttemptTrace) Record(attempt Attempt, err error, diagnose func(inbound, sent []byte, customerCredential bool) inference.AttemptDiagnosis) inference.AttemptDiagnosis {
	t.mu.Lock()
	defer t.mu.Unlock()
	diagnosis := diagnose(t.inbound, t.sent, t.customerCredential)
	t.attempts = append(t.attempts, TracedAttempt{Target: attempt.Target, BaseURL: t.baseURL, Err: err, Diagnosis: diagnosis})
	return diagnosis
}

// LastFailure skips served attempts: badges and terminal errors describe the
// failure that forced a switch, not the attempt that recovered.
func (t *AttemptTrace) LastFailure() (TracedAttempt, bool) {
	if t == nil {
		return TracedAttempt{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := len(t.attempts) - 1; i >= 0; i-- {
		if t.attempts[i].Err != nil {
			return t.attempts[i], true
		}
	}
	return TracedAttempt{}, false
}

// LastFailureWhere is the newest failed attempt whose target match accepts;
// later failures on other targets are skipped.
func (t *AttemptTrace) LastFailureWhere(match func(inference.Target) bool) (TracedAttempt, bool) {
	if t == nil {
		return TracedAttempt{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := len(t.attempts) - 1; i >= 0; i-- {
		if t.attempts[i].Err != nil && match(t.attempts[i].Target) {
			return t.attempts[i], true
		}
	}
	return TracedAttempt{}, false
}

// RetriesOf counts the failed same-target attempts before a, so a terminal
// error can say how often the router retried the binding.
func (t *AttemptTrace) RetriesOf(a TracedAttempt) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for _, prior := range t.attempts {
		if prior.Err != nil && prior.Target.Provider == a.Target.Provider && prior.Target.CatalogID == a.Target.CatalogID {
			n++
		}
	}
	return max(n-1, 0)
}

// AttemptSnapshot is the in-flight attempt plus the newest failure before it.
type AttemptSnapshot struct {
	Current        TracedAttempt
	AttemptAt      time.Time
	FirstAttemptAt time.Time
	LastFailure    TracedAttempt
	Failed         bool
}

// SlowStartSnapshot reports false before the first attempt or once slow-start
// notices are muted.
func (t *AttemptTrace) SlowStartSnapshot() (AttemptSnapshot, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.attemptAt.IsZero() || t.slowStartMuted {
		return AttemptSnapshot{}, false
	}
	snap := AttemptSnapshot{
		Current:        TracedAttempt{Target: t.current, BaseURL: t.baseURL},
		AttemptAt:      t.attemptAt,
		FirstAttemptAt: t.firstAttemptAt,
	}
	for i := len(t.attempts) - 1; i >= 0; i-- {
		if t.attempts[i].Err != nil {
			snap.LastFailure, snap.Failed = t.attempts[i], true
			break
		}
	}
	return snap, true
}
