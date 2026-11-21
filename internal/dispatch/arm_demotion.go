package dispatch

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

type ArmDemotionPolicy struct {
	CommittedStream bool
	RescuedFailure  bool
}

func (p ArmDemotionPolicy) CommittedStreamEnabled(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyCommittedStreamArmDemotion, p.CommittedStream)
}

func (p ArmDemotionPolicy) RescuedFailureEnabled(ctx context.Context) bool {
	return flags.BoolOr(ctx, flags.KeyRescuedFailureArmDemotion, p.RescuedFailure)
}

type SessionArmDemoter interface {
	DemoteArm(ctx context.Context, model string, reason sessionpin.DemotionReason, cooldownUntil time.Time, upstreamStatus int) bool
}

type ArmFailure struct {
	Session    SessionArmDemoter
	HardPinned bool
	Err        error
	Model      string
	Reason     string
}

type RescuedFailureStrike struct {
	ArmFailure
	RescueRan             bool
	RescuedPrimaryFailure bool
	RateLimit             RateLimitPolicy
	Now                   func() time.Time
	RecordTurnFailure     func(ctx context.Context, model string)
}

func (p ArmDemotionPolicy) AfterCommittedStreamFailure(ctx context.Context, committedStream bool, f ArmFailure) string {
	if !p.CommittedStreamEnabled(ctx) || f.Session == nil {
		return ""
	}
	if f.Model == "" || f.HardPinned {
		return ""
	}
	if strings.HasPrefix(f.Reason, translate.ReasonUserForceModel) {
		return ""
	}
	if !committedStream || !IsCommittedStreamFailure(ctx, f.Err) {
		return ""
	}
	if !f.Session.DemoteArm(ctx, f.Model, sessionpin.DemotionReasonCommittedStreamFailure, time.Time{}, UpstreamStatus(f.Err)) {
		return ""
	}
	return f.Model
}

func (p ArmDemotionPolicy) StrikeAfterRescuedFailure(ctx context.Context, s RescuedFailureStrike) (string, sessionpin.DemotionReason) {
	if s.Session == nil {
		return "", ""
	}
	if s.Model == "" || s.HardPinned {
		return "", ""
	}
	if strings.HasPrefix(s.Reason, translate.ReasonUserForceModel) {
		return "", ""
	}
	headerTimeout := providers.IsResponseHeaderTimeout(s.Err)
	if (!s.RescueRan && !headerTimeout) || (s.RescueRan && !s.RescuedPrimaryFailure) {
		return "", ""
	}
	if !p.RescuedFailureEnabled(ctx) {
		if s.RescueRan {
			s.RecordTurnFailure(ctx, s.Model)
		}
		return "", ""
	}
	reason, cooldownUntil := sessionpin.DemotionReasonRescuedFailure, time.Time{}
	if headerTimeout {
		reason = sessionpin.DemotionReasonResponseHeaderTimeout
	} else if s.RateLimit.Transient(ctx) && IsTransientGatewayFailure(s.Err) {
		cooldown := s.RateLimit.Cooldown(ctx)
		reason, cooldownUntil = sessionpin.DemotionReasonTransientFailure, s.Now().Add(cooldown)
		ThrottleLedgerFromContext(ctx).RecordCooldown(cooldownUntil, cooldown)
	} else if s.RateLimit.Transient(ctx) && providers.IsUpstreamRateLimited(s.Err) {
		cooldown := s.RateLimit.Cooldown(ctx)
		reason, cooldownUntil = sessionpin.DemotionReasonRateLimited, s.Now().Add(cooldown)
		ThrottleLedgerFromContext(ctx).RecordCooldown(cooldownUntil, cooldown)
	}
	if !s.Session.DemoteArm(ctx, s.Model, reason, cooldownUntil, UpstreamStatus(s.Err)) {
		return "", ""
	}
	return s.Model, reason
}

func (p ArmDemotionPolicy) AfterUnrescuedStall(ctx context.Context, rescueRan, committed bool, f ArmFailure) string {
	if !p.RescuedFailureEnabled(ctx) || f.Session == nil ||
		f.Model == "" || f.HardPinned || rescueRan || committed || ctx.Err() != nil ||
		strings.HasPrefix(f.Reason, translate.ReasonUserForceModel) || !IsUpstreamWatchdogError(f.Err) {
		return ""
	}
	if !f.Session.DemoteArm(ctx, f.Model, sessionpin.DemotionReasonUnrescuedStall, time.Time{}, UpstreamStatus(f.Err)) {
		return ""
	}
	return f.Model
}

func IsTransientGatewayFailure(err error) bool {
	switch UpstreamStatus(err) {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func IsCommittedStreamFailure(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	err = ErrorBehindFrames(err)
	if IsUpstreamWatchdogError(err) || IsUpstreamWatchdogError(context.Cause(ctx)) {
		return true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	switch status := UpstreamStatus(err); {
	case status == ProviderOverloadedStatus:
		return false
	case status >= http.StatusInternalServerError:
		return true
	case status != 0:
		return false
	}
	return ctx.Err() == nil
}

func ErrorBehindFrames(err error) error {
	for {
		var synthetic *providers.UpstreamStatusError
		if !errors.As(err, &synthetic) || synthetic.Cause == nil {
			return err
		}
		err = synthetic.Cause
	}
}

func IsUpstreamWatchdogError(err error) bool {
	return errors.Is(err, providers.ErrUpstreamIdleTimeout) ||
		errors.Is(err, providers.ErrUpstreamOutputStall) ||
		errors.Is(err, providers.ErrUpstreamSlowThroughput)
}

func UpstreamStatus(err error) int {
	if err == nil {
		return http.StatusOK
	}
	var statusErr *providers.UpstreamStatusError
	if errors.As(err, &statusErr) {
		return statusErr.Status
	}
	var bufferedErr *providers.UpstreamErrorResponse
	if errors.As(err, &bufferedErr) {
		return bufferedErr.Status
	}
	return 0
}
