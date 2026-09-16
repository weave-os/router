package proxy_test

import (
	"context"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"weave-os/router/internal/proxy"
)

const (
	feedbackRequestedModel = "claude-sonnet-4-6"
	feedbackServedModel    = "claude-opus-4-7"
	feedbackAlternateModel = "claude-haiku-4-5"
)

// fakeFeedbackStore is an in-memory RouterFeedbackStore + RouterFeedbackQueue
// that models the semantics the Postgres implementation owes: per-scope
// completion numbering, one-shot selector resolution at acceptance, and
// token-fenced claims. Tests assert on what it saved, not on how it was called.
type fakeFeedbackStore struct {
	mu sync.Mutex
	// history holds completed requests per logical scope, in commit order.
	history map[string][]proxy.FeedbackRequest
	// events is every accepted command, keyed by its durable id.
	events map[string]*proxy.RouterFeedbackEvent
	// accepted preserves acceptance order for assertions.
	accepted []string
	// ratings records the local request ratings the acceptance transaction
	// applied, keyed by rated request id. A note-only command applies none,
	// so a previously stored thumb survives.
	ratings map[string]string
	// leases maps an event id to the token that currently owns it.
	leases     map[string]string
	leaseUntil map[string]time.Time
	nextDue    map[string]time.Time

	completeErr error
	acceptErr   error
	claimErr    error
	// trainingDenied answers the delivery-time permission recheck. It is
	// negative so the zero value permits delivery, as a live installation
	// with training enabled does.
	trainingDenied bool
	trainingErr    error
	now            func() time.Time
}

func newFakeFeedbackStore() *fakeFeedbackStore {
	return &fakeFeedbackStore{}
}

// init allocates on first write so the zero value is a usable store.
func (f *fakeFeedbackStore) init() {
	if f.history == nil {
		f.history = make(map[string][]proxy.FeedbackRequest)
		f.events = make(map[string]*proxy.RouterFeedbackEvent)
		f.ratings = make(map[string]string)
		f.leases = make(map[string]string)
		f.leaseUntil = make(map[string]time.Time)
		f.nextDue = make(map[string]time.Time)
	}
}

func (f *fakeFeedbackStore) clock() time.Time {
	if f.now != nil {
		return f.now()
	}
	return time.Now()
}

func feedbackScopeKey(installationID string, sessionKey []byte, role string) string {
	return installationID + "|" + hex.EncodeToString(sessionKey) + "|" + role
}

// CompleteFeedbackRequest appends one completed response to its scope. A repeat
// for the same request id is a no-op, as the real transaction's uniqueness
// constraint makes it.
func (f *fakeFeedbackStore) CompleteFeedbackRequest(_ context.Context, req proxy.FeedbackRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.completeErr != nil {
		return f.completeErr
	}
	f.init()
	scope := feedbackScopeKey(req.InstallationID, req.SessionKey, req.Role)
	for _, existing := range f.history[scope] {
		if existing.RequestID == req.RequestID {
			return nil
		}
	}
	req.Sequence = int64(len(f.history[scope])) + 1
	if req.CompletedAt.IsZero() {
		req.CompletedAt = f.clock()
	}
	f.history[scope] = append(f.history[scope], req)
	return nil
}

// AcceptRouterFeedback resolves the selector once against committed history and
// saves the command with its exact target, or with no target when the selector
// is out of range. Re-accepting the same id returns the stored row unchanged
// and never reapplies the rating.
func (f *fakeFeedbackStore) AcceptRouterFeedback(_ context.Context, event proxy.RouterFeedbackEvent) (proxy.RouterFeedbackEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.acceptErr != nil {
		return proxy.RouterFeedbackEvent{}, f.acceptErr
	}
	f.init()
	if stored, ok := f.events[event.ID]; ok {
		return *stored, nil
	}
	scope := feedbackScopeKey(event.InstallationID, event.SessionKey, event.Role)
	entries := f.history[scope]
	if target, ok := resolveFeedbackSelector(entries, event.Sequence); ok {
		event.TargetSequence = target.Sequence
		event.RequestID = target.RequestID
		event.ServedModel = target.ServedModel
		event.ServedProvider = target.ServedProvider
		event.Strategy = target.Strategy
		event.RouteID = target.RouteID
		event.TrainingAllowed = event.TrainingAllowed && target.TrainingAllowed
		// The up/down-only rating table rejects an empty verdict; a note-only
		// command must leave any earlier thumb in place.
		if event.Rating != "" {
			f.ratings[target.RequestID] = event.Rating
		}
	} else {
		event.TrainingAllowed = false
	}
	event.DeliveryStatus = proxy.RouterFeedbackPending
	event.CreatedAt = f.clock()
	saved := event
	f.events[event.ID] = &saved
	f.accepted = append(f.accepted, event.ID)
	return saved, nil
}

// resolveFeedbackSelector counts positive selectors from the beginning and
// negative selectors from the end of the scope's completion order.
func resolveFeedbackSelector(entries []proxy.FeedbackRequest, selector int) (proxy.FeedbackRequest, bool) {
	index := -1
	switch {
	case selector > 0:
		index = selector - 1
	case selector < 0:
		index = len(entries) + selector
	}
	if index < 0 || index >= len(entries) {
		return proxy.FeedbackRequest{}, false
	}
	return entries[index], true
}

// ClaimRouterFeedback leases the oldest due pending command. An unexpired lease
// held by another worker is skipped, which is what makes an abandoned claim
// recoverable only after it expires.
func (f *fakeFeedbackStore) ClaimRouterFeedback(_ context.Context, token string, lease time.Duration) (proxy.RouterFeedbackEvent, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claimErr != nil {
		return proxy.RouterFeedbackEvent{}, false, f.claimErr
	}
	now := f.clock()
	for _, id := range f.accepted {
		event := f.events[id]
		if event.DeliveryStatus != proxy.RouterFeedbackPending {
			continue
		}
		if due, ok := f.nextDue[id]; ok && due.After(now) {
			continue
		}
		if until, leased := f.leaseUntil[id]; leased && until.After(now) {
			continue
		}
		event.Attempts++
		event.LeaseToken = token
		f.leases[id] = token
		f.leaseUntil[id] = now.Add(lease)
		return *event, true, nil
	}
	return proxy.RouterFeedbackEvent{}, false, nil
}

// FinishRouterFeedback applies a terminal state only for the current lease
// owner, so a worker whose lease expired cannot overwrite the new owner's work.
func (f *fakeFeedbackStore) FinishRouterFeedback(_ context.Context, id, token, status, lastError string, nextAttempt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	event, ok := f.events[id]
	if !ok {
		return errors.New("unknown router feedback id " + id)
	}
	if f.leases[id] != token {
		return proxy.ErrFeedbackLeaseLost
	}
	event.DeliveryStatus = status
	event.LastError = lastError
	event.LeaseToken = ""
	delete(f.leases, id)
	delete(f.leaseUntil, id)
	if nextAttempt.IsZero() {
		delete(f.nextDue, id)
	} else {
		f.nextDue[id] = nextAttempt
	}
	return nil
}

func (f *fakeFeedbackStore) RouterFeedbackTrainingAllowed(_ context.Context, _, _ string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.trainingErr != nil {
		return false, f.trainingErr
	}
	return !f.trainingDenied, nil
}

// expireLeases makes every outstanding claim immediately reclaimable, standing
// in for a worker that died without finishing.
func (f *fakeFeedbackStore) expireLeases() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id := range f.leaseUntil {
		f.leaseUntil[id] = f.clock().Add(-time.Second)
	}
	for id := range f.nextDue {
		delete(f.nextDue, id)
	}
}

// clearBackoff makes rescheduled work due now without disturbing leases.
func (f *fakeFeedbackStore) clearBackoff() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id := range f.nextDue {
		delete(f.nextDue, id)
	}
}

func (f *fakeFeedbackStore) event(id string) proxy.RouterFeedbackEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	if stored, ok := f.events[id]; ok {
		return *stored
	}
	return proxy.RouterFeedbackEvent{}
}

// events returns every accepted command in acceptance order.
func (f *fakeFeedbackStore) acceptedEvents() []proxy.RouterFeedbackEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]proxy.RouterFeedbackEvent, 0, len(f.accepted))
	for _, id := range f.accepted {
		out = append(out, *f.events[id])
	}
	return out
}

func (f *fakeFeedbackStore) ratingFor(requestID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ratings[requestID]
}

func (f *fakeFeedbackStore) setRating(requestID, rating string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	f.ratings[requestID] = rating
}

// complete records one successful response in the scope a /rf command from the
// same session and tier will resolve against.
func (f *fakeFeedbackStore) complete(installationID string, sessionKey []byte, role string, req proxy.FeedbackRequest) {
	req.InstallationID = installationID
	req.SessionKey = append([]byte(nil), sessionKey...)
	req.Role = role
	if err := f.CompleteFeedbackRequest(context.Background(), req); err != nil {
		panic(err)
	}
}
