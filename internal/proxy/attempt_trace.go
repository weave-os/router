package proxy

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"

	"github.com/tidwall/sjson"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/observability"
)

type attemptTraceContextKey struct{}

// attemptTrace collects one request's upstream attempts so telemetry, routing
// badges and terminal errors can name who owns each failure.
type attemptTrace struct {
	mu                 sync.Mutex
	inbound            []byte
	sent               []byte
	customerCredential bool
	attempts           []tracedAttempt
	// hideRouterDetails is set when the request opted out of routing badges
	// or the installation hides terminal surfaces.
	hideRouterDetails bool
}

type tracedAttempt struct {
	Target    inference.Target
	Err       error
	Diagnosis inference.AttemptDiagnosis
}

func (a tracedAttempt) owner() FailureOwner { return FailureOwner(a.Diagnosis.FailureOwner) }

// withAttemptTrace starts tracing the attempts that dispatch inbound. A
// surface that delegates to another keeps the outer surface's inbound body.
func withAttemptTrace(ctx context.Context, inbound []byte, h http.Header) context.Context {
	if attemptTraceFromContext(ctx) != nil {
		return ctx
	}
	return context.WithValue(ctx, attemptTraceContextKey{}, &attemptTrace{
		inbound:           inbound,
		hideRouterDetails: suppressMarkerIfRequested(ctx, h, "x") == "",
	})
}

func (t *attemptTrace) hideDetails() bool {
	return t != nil && t.hideRouterDetails
}

func attemptTraceFromContext(ctx context.Context) *attemptTrace {
	trace, _ := ctx.Value(attemptTraceContextKey{}).(*attemptTrace)
	return trace
}

func (t *attemptTrace) beginAttempt() {
	t.mu.Lock()
	t.sent, t.customerCredential = nil, false
	t.mu.Unlock()
}

func (t *attemptTrace) observeSent(ctx context.Context, body []byte) {
	customer := customerOwnedCredential(ctx)
	t.mu.Lock()
	t.sent = body
	t.customerCredential = customer
	t.mu.Unlock()
}

// customerOwnedCredential reports whether the attempt authenticated with the
// customer's own key or subscription rather than a Weave-managed key or a
// Weave-leased subscription account.
func customerOwnedCredential(ctx context.Context) bool {
	if managedSubscriptionCredentialSource(ctx) != "" {
		return false
	}
	creds := CredentialsFromContext(ctx)
	if creds == nil || len(creds.APIKey) == 0 || creds.SubscriptionAccountID != "" {
		return false
	}
	switch creds.Source {
	case credSourceBYOK, credSourceClient, credSourceSubscription, credSourceCodexSubscription:
		return true
	}
	return false
}

// describe diagnoses the attempt dispatch just recorded and logs a failure
// with its owner; an unowned failure is logged loudly so a rule can be added.
func (t *attemptTrace) describe(ctx context.Context, attempt dispatch.Attempt, err error) inference.AttemptDiagnosis {
	t.mu.Lock()
	defer t.mu.Unlock()
	diagnosis := inference.AttemptDiagnosis{BodyTranslated: bodyRewritten(t.inbound, t.sent)}
	if err != nil {
		diagnosis.ErrorClass = string(classifyTurnError(err, "", 0))
		diagnosis.FailureOwner = string(classifyFailureOwner(failureOwnerInput{
			Err:                err,
			BodyTranslated:     diagnosis.BodyTranslated,
			CustomerCredential: t.customerCredential,
		}))
		logAttemptFailure(ctx, attempt, err, diagnosis)
	}
	t.attempts = append(t.attempts, tracedAttempt{Target: attempt.Target, Err: err, Diagnosis: diagnosis})
	return diagnosis
}

// lastFailure skips served attempts: badges and terminal errors describe the
// failure that forced a switch, not the attempt that recovered.
func (t *attemptTrace) lastFailure() (tracedAttempt, bool) {
	if t == nil {
		return tracedAttempt{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := len(t.attempts) - 1; i >= 0; i-- {
		if t.attempts[i].Err != nil {
			return t.attempts[i], true
		}
	}
	return tracedAttempt{}, false
}

func logAttemptFailure(ctx context.Context, attempt dispatch.Attempt, err error, diagnosis inference.AttemptDiagnosis) {
	fields := []any{
		"model", attempt.Target.CatalogID,
		"provider", attempt.Target.Provider,
		"attempt_index", attempt.Index,
		"same_binding_retry", attempt.SameBindingRetry,
		"upstream_status", upstreamStatus(err),
		"error_class", diagnosis.ErrorClass,
		"failure_owner", diagnosis.FailureOwner,
		"body_translated", diagnosis.BodyTranslated,
		"err", err,
	}
	level, msg := slog.LevelInfo, "Upstream attempt failed"
	if FailureOwner(diagnosis.FailureOwner) == FailureOwnerUnknown {
		level, msg = slog.LevelWarn, "Upstream attempt failed with unknown failure owner"
	}
	observability.FromContext(ctx).Log(ctx, level, msg, fields...)
}

// turnFailureOwnerFor names the owner of a turn's final outcome: the last
// failed attempt's owner when dispatch failed, else the error class's owner.
func turnFailureOwnerFor(ctx context.Context, err error, class TurnErrorClass) FailureOwner {
	if err == nil {
		return turnFailureOwner(class)
	}
	if last, ok := attemptTraceFromContext(ctx).lastFailure(); ok && errors.Is(err, last.Err) {
		return last.owner()
	}
	return classifyFailureOwner(failureOwnerInput{Err: err})
}

// bodyRewritten reports whether sent differs from the inbound body beyond the
// top-level model field, which every routed dispatch rewrites. A nil sent
// body means nothing reached the adapter.
func bodyRewritten(inbound, sent []byte) bool {
	if sent == nil || bytes.Equal(inbound, sent) {
		return false
	}
	return !bytes.Equal(withoutTopLevelModel(inbound), withoutTopLevelModel(sent))
}

func withoutTopLevelModel(body []byte) []byte {
	out, err := sjson.DeleteBytes(body, "model")
	if err != nil {
		return body
	}
	return out
}
