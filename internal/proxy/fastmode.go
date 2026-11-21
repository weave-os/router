package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/telemetry"
	"weave-os/router/internal/translate"
)

type anthropicTierAttempt struct {
	s             *Service
	log           *slog.Logger
	env           *translate.RequestEnvelope
	r             *http.Request
	opts          translate.EmitOptions
	native        dispatch.UpstreamAttempt
	sink          http.ResponseWriter
	preludeBuf    *preludeBuffer
	preludeState  *requestNoticeStream
	streamCut     *streamCutObserver
	notice        translate.RouterNotice
	setExtractor  func(*telemetry.UsageExtractor)
	setStreamCost func(router.Decision, bool, func() bool)
	logBody       func(router.Decision, []byte)
}

func (a *anthropicTierAttempt) forBinding(actx context.Context, d router.Decision) (translate.EmitOptions, dispatch.UpstreamAttempt, bool, error) {
	fast := fastModeForAttempt(actx, d.Model, d.Provider)
	if fast == a.opts.FastMode {
		return a.opts, a.native, fast, nil
	}
	attemptOpts := a.opts
	attemptOpts.TargetProvider = d.Provider
	attemptOpts.FastMode = fast
	prep, err := a.env.PrepareAnthropic(a.r.Header, attemptOpts)
	if err != nil {
		a.log.Error("Failed to re-emit Anthropic body for fast-tier change", "err", err)
		return attemptOpts, nil, fast, fmt.Errorf("emit body: %w", err)
	}
	a.logBody(d, prep.Body)
	native := a.s.anthropicNativeAttempt(a.env, a.r, prep, a.sink, a.preludeBuf, a.preludeState, a.streamCut, a.notice, a.setExtractor, a.setStreamCost)
	return attemptOpts, native, fast, nil
}

func (a *anthropicTierAttempt) dispatch(actx context.Context, d router.Decision, p dispatch.Upstream, recordFast func(bool)) (translate.EmitOptions, error) {
	attemptOpts, native, fast, err := a.forBinding(actx, d)
	recordFast(fast)
	if err != nil {
		return attemptOpts, err
	}
	err = native(actx, d, p)
	err, retried := retryAnthropicStandardSpeed(a.log, a.preludeBuf, d, attemptOpts, err, func(standardOpts translate.EmitOptions) (func() error, error) {
		prep, emitErr := a.env.PrepareAnthropic(a.r.Header, standardOpts)
		if emitErr != nil {
			return nil, emitErr
		}
		return func() error {
			a.logBody(d, prep.Body)
			recordFast(false)
			standard := a.s.anthropicNativeAttempt(a.env, a.r, prep, a.sink, a.preludeBuf, a.preludeState, a.streamCut, a.notice, a.setExtractor, a.setStreamCost)
			return standard(actx, d, p)
		}, nil
	})
	if retried {
		attemptOpts.FastMode = false
	}
	return attemptOpts, err
}

func retryAnthropicStandardSpeed(log *slog.Logger, buffer *preludeBuffer, target router.Decision, opts translate.EmitOptions,
	upstreamErr error, prepare func(translate.EmitOptions) (func() error, error),
) (error, bool) {
	if upstreamErr == nil || !opts.FastMode || committed(buffer) || !providers.IsAnthropicFastModeQuotaRejection(upstreamErr) {
		return upstreamErr, false
	}
	opts.FastMode = false
	send, emitErr := prepare(opts)
	if emitErr != nil {
		log.Error("Failed to re-emit Anthropic body at standard speed", "err", emitErr)
		return upstreamErr, false
	}
	log.Warn("Retrying Anthropic request at standard speed after fast-mode quota rejection",
		"model", target.Model, "provider", target.Provider)
	if buffer != nil {
		buffer.Discard()
	}
	return send(), true
}

func (a *anthropicTierAttempt) attempt(recordFast func(bool)) dispatch.UpstreamAttempt {
	return func(actx context.Context, d router.Decision, p dispatch.Upstream) error {
		_, err := a.dispatch(actx, d, p, recordFast)
		return err
	}
}

func fastModeForAttempt(ctx context.Context, model, provider string) bool {
	if !slices.Contains(installationFastModeModelsFromContext(ctx), model) {
		return false
	}
	if _, ok := catalog.FastPriceFor(provider, model); !ok {
		return false
	}
	return !servedOnSubscription(ctx)
}

func servedPricing(provider, model string, fast bool) (catalog.Pricing, bool) {
	if fast {
		if fastPrice, ok := catalog.FastPriceFor(provider, model); ok {
			return fastPrice, true
		}
	}
	if price, ok := catalog.PriceFor(provider, model); ok {
		return price, true
	}
	price, ok := catalog.PrimaryPriceFor(model)
	return price.WithoutInferenceGeoPremium(), ok
}
