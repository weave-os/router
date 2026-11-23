package proxy

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/tidwall/sjson"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/ingress"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/telemetry/turnrecord"
)

type attemptTraceContextKey struct{}

func attemptOwner(a dispatch.TracedAttempt) turnrecord.FailureOwner {
	return turnrecord.FailureOwner(a.Diagnosis.FailureOwner)
}

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

func customerOwnedCredential(ctx context.Context) bool {
	if managedSubscriptionCredentialSource(ctx) != "" {
		return false
	}
	creds := credentials.CredentialsFromContext(ctx)
	if creds == nil || len(creds.APIKey) == 0 || creds.SubscriptionAccountID != "" {
		return false
	}
	switch creds.Source {
	case requestcontext.SourceBYOK, requestcontext.SourceClient, requestcontext.SourceSubscription, requestcontext.SourceCodexSubscription:
		return true
	}
	return false
}

func observeAttemptSent(trace *dispatch.AttemptTrace) func(context.Context, []byte) {
	return func(ctx context.Context, body []byte) {
		trace.ObserveSent(ctx, body, customerOwnedCredential(ctx))
	}
}

func describeAttempt(ctx context.Context, trace *dispatch.AttemptTrace, attempt dispatch.Attempt, err error) inference.AttemptDiagnosis {
	return trace.Record(attempt, err, func(inbound, sent []byte, customerCredential bool) inference.AttemptDiagnosis {
		diagnosis := inference.AttemptDiagnosis{BodyTranslated: bodyRewritten(inbound, sent)}
		if err != nil {
			diagnosis.ErrorClass = string(classifyTurnError(err, "", 0))
			diagnosis.FailureOwner = string(classifyFailureOwner(failureOwnerInput{
				Err:                    err,
				BodyTranslated:         diagnosis.BodyTranslated,
				CustomerCredential:     customerCredential,
				RejectedFieldUnchanged: ingress.RejectedFieldUnchanged(err, inbound, sent),
			}))
			logAttemptFailure(ctx, attempt, err, diagnosis)
		}
		return diagnosis
	})
}

func lastFailureOn(trace *dispatch.AttemptTrace, model string) (dispatch.TracedAttempt, bool) {
	return trace.LastFailureWhere(func(target inference.Target) bool {
		return candidates.BaseModelOf(target.CatalogID) == candidates.BaseModelOf(model)
	})
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
	if turnrecord.FailureOwner(diagnosis.FailureOwner) == turnrecord.FailureOwnerUnknown {
		level, msg = slog.LevelWarn, "Upstream attempt failed with unknown failure owner"
	}
	observability.FromContext(ctx).Log(ctx, level, msg, fields...)
}

func turnFailureOwnerFor(ctx context.Context, err error, class turnrecord.TurnErrorClass) turnrecord.FailureOwner {
	if err == nil {
		return turnFailureOwner(class)
	}
	if last, ok := attemptTraceFromContext(ctx).LastFailure(); ok && errors.Is(err, last.Err) {
		return attemptOwner(last)
	}
	return classifyFailureOwner(failureOwnerInput{Err: err})
}

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
