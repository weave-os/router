package proxy

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/tidwall/sjson"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/observability"
)

type attemptTraceContextKey struct{}

func attemptOwner(a dispatch.TracedAttempt) FailureOwner {
	return FailureOwner(a.Diagnosis.FailureOwner)
}

// withAttemptTrace starts tracing the attempts that dispatch inbound. A
// surface that delegates to another keeps the outer surface's inbound body.
func withAttemptTrace(ctx context.Context, inbound []byte, h http.Header) context.Context {
	if attemptTraceFromContext(ctx) != nil {
		return ctx
	}
	return context.WithValue(ctx, attemptTraceContextKey{}, dispatch.NewAttemptTrace(inbound, suppressMarkerIfRequested(ctx, h, "x") == ""))
}

func attemptTraceFromContext(ctx context.Context) *dispatch.AttemptTrace {
	trace, _ := ctx.Value(attemptTraceContextKey{}).(*dispatch.AttemptTrace)
	return trace
}

// observeAttemptSent records each prepared body with whether the attempt
// authenticated with a customer-owned credential.
func observeAttemptSent(trace *dispatch.AttemptTrace) func(context.Context, []byte) {
	return func(ctx context.Context, body []byte) {
		trace.ObserveSent(ctx, body, customerOwnedCredential(ctx))
	}
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

// describeAttempt diagnoses the attempt dispatch just recorded and logs a
// failure with its owner; an unowned failure is logged loudly so a rule can be
// added.
func describeAttempt(ctx context.Context, trace *dispatch.AttemptTrace, attempt dispatch.Attempt, err error) inference.AttemptDiagnosis {
	return trace.Record(attempt, err, func(inbound, sent []byte, customerCredential bool) inference.AttemptDiagnosis {
		diagnosis := inference.AttemptDiagnosis{BodyTranslated: bodyRewritten(inbound, sent)}
		if err != nil {
			diagnosis.ErrorClass = string(classifyTurnError(err, "", 0))
			diagnosis.FailureOwner = string(classifyFailureOwner(failureOwnerInput{
				Err:                err,
				BodyTranslated:     diagnosis.BodyTranslated,
				CustomerCredential: customerCredential,
			}))
			logAttemptFailure(ctx, attempt, err, diagnosis)
		}
		return diagnosis
	})
}

// lastFailureOn is the newest failed attempt on model's base model, so a marker
// cites that model's own failure rather than a later candidate's. Models match
// by base model (baseModelOf), so catalog variants of one model count.
func lastFailureOn(trace *dispatch.AttemptTrace, model string) (dispatch.TracedAttempt, bool) {
	return trace.LastFailureWhere(func(target inference.Target) bool {
		return baseModelOf(target.CatalogID) == baseModelOf(model)
	})
}

// failedOn reports whether any attempt on model failed in this request.
func failedOn(trace *dispatch.AttemptTrace, model string) bool {
	_, ok := lastFailureOn(trace, model)
	return ok
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
	if last, ok := attemptTraceFromContext(ctx).LastFailure(); ok && errors.Is(err, last.Err) {
		return attemptOwner(last)
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
