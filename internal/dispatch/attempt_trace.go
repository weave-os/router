package dispatch

import (
	"context"
	"sync"
	"time"

	"weave-os/router/internal/inference"
	"weave-os/router/internal/requestcontext"
)

type AttemptTrace struct {
	mu                 sync.Mutex
	inbound            []byte
	sent               []byte
	customerCredential bool
	subscription       bool
	baseURL            string
	deploymentBaseURL  string
	current            inference.Target
	firstAttemptAt     time.Time
	attemptAt          time.Time
	attempts           []TracedAttempt
	hideRouterDetails  bool
	slowStartMuted     bool
}

type TracedAttempt struct {
	Target       inference.Target
	BaseURL      string
	Err          error
	Diagnosis    inference.AttemptDiagnosis
	At           time.Time
	Subscription bool
}

func NewAttemptTrace(inbound []byte, hideRouterDetails bool) *AttemptTrace {
	return &AttemptTrace{
		inbound:           inbound,
		hideRouterDetails: hideRouterDetails,
	}
}

func (t *AttemptTrace) Inbound() []byte {
	return t.inbound
}

func (t *AttemptTrace) HideDetails() bool {
	return t != nil && t.hideRouterDetails
}

func (t *AttemptTrace) MuteSlowStart() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.slowStartMuted = true
	t.mu.Unlock()
}

func (t *AttemptTrace) ServingBaseURL() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.baseURL
}

func (t *AttemptTrace) BeginAttempt(target inference.Target, deploymentBaseURL string, now time.Time) {
	t.mu.Lock()
	t.current, t.attemptAt = target, now
	t.deploymentBaseURL = deploymentBaseURL
	if t.firstAttemptAt.IsZero() {
		t.firstAttemptAt = now
	}
	t.sent, t.customerCredential, t.subscription, t.baseURL = nil, false, false, deploymentBaseURL
	t.mu.Unlock()
}

func (t *AttemptTrace) ObserveSent(ctx context.Context, body []byte, customerCredential bool) {
	t.mu.Lock()
	baseURL := requestcontext.EffectiveBaseURL(ctx, t.deploymentBaseURL)
	t.sent = body
	t.customerCredential = customerCredential
	creds := requestcontext.CredentialsFromContext(ctx)
	t.subscription = creds != nil && creds.OAuth
	t.baseURL = baseURL
	t.mu.Unlock()
}

func (t *AttemptTrace) Record(attempt Attempt, err error, diagnose func(inbound, sent []byte, customerCredential bool) inference.AttemptDiagnosis) inference.AttemptDiagnosis {
	t.mu.Lock()
	defer t.mu.Unlock()
	diagnosis := diagnose(t.inbound, t.sent, t.customerCredential)
	t.attempts = append(t.attempts, TracedAttempt{Target: attempt.Target, BaseURL: t.baseURL, Err: err, Diagnosis: diagnosis, At: t.attemptAt, Subscription: t.subscription || t.unsentAfterSubscription(attempt.Target)})
	return diagnosis
}

func (t *AttemptTrace) unsentAfterSubscription(target inference.Target) bool {
	if t.sent != nil || len(t.attempts) == 0 {
		return false
	}
	prior := t.attempts[len(t.attempts)-1]
	return prior.Subscription && prior.Target.Provider == target.Provider && prior.Target.CatalogID == target.CatalogID
}

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

type AttemptSnapshot struct {
	Current        TracedAttempt
	AttemptAt      time.Time
	FirstAttemptAt time.Time
	LastFailure    TracedAttempt
	Failed         bool
}

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
